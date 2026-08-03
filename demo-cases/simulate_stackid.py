#!/usr/bin/env python3
"""bpf_get_stackid 定量对照：真实样本流上模拟 32 位截断 ID 的冲突率。

bpf_get_stackid 对原始栈做 jhash2 后取 32 位截断作为 ID（bucket 索引），
有损：不同栈共享同一 ID 时要么拒绝（-EEXIST）要么静默覆盖
（BPF_F_REUSE_STACKID）。本脚本在 llm-prof 的真实样本流上：
  1) 计算每个 distinct 栈的 32 位截断 ID 与 64 位指纹；
  2) 统计 32 位 ID 冲突（不同栈同 ID）的栈对数/比例——量化"有损"的
     实际发生频率；64 位指纹冲突作为对照（理论 ~0）；
  3) 输出：冲突栈对数、受影响样本比例（若按覆盖语义，冲突栈中较晚
     的样本会被错误归并）。
用法: simulate_stackid.py <stream.txt>...
"""
import sys
from collections import Counter, defaultdict


def fp_step(h, v):
    h ^= (v * 0x9E3779B97F4A7C15) & 0xFFFFFFFFFFFFFFFF
    h = ((h << 31) | (h >> 33)) & 0xFFFFFFFFFFFFFFFF
    h = (h * 0xC2B2AE3D27D4EB4F) & 0xFFFFFFFFFFFFFFFF
    h ^= h >> 29
    return h & 0xFFFFFFFFFFFFFFFF


SEED = 0x6c6c70666c6c7066


def jhash32(words, initval):
    """内核 jhash2 的 32 位输出（v6.8 stackmap.c 语义：JHASH_INITVAL +
    (length<<2) + initval，3 字一轮 mix）。"""
    JHASH_INITVAL = 0xdeadbeef
    length = len(words)
    a = b = c = (JHASH_INITVAL + (length << 2) + initval) & 0xFFFFFFFF
    i = 0
    while length > 3:
        a = (a + words[i]) & 0xFFFFFFFF
        b = (b + words[i + 1]) & 0xFFFFFFFF
        c = (c + words[i + 2]) & 0xFFFFFFFF
        # jhash_mix
        a = (a - c) & 0xFFFFFFFF; a ^= ((c << 4) | (c >> 28)) & 0xFFFFFFFF; c = (c + b) & 0xFFFFFFFF
        b = (b - a) & 0xFFFFFFFF; b ^= ((a << 6) | (a >> 26)) & 0xFFFFFFFF; a = (a + c) & 0xFFFFFFFF
        c = (c - b) & 0xFFFFFFFF; c ^= ((b << 8) | (b >> 24)) & 0xFFFFFFFF; b = (b + a) & 0xFFFFFFFF
        a = (a - c) & 0xFFFFFFFF; a ^= ((c << 16) | (c >> 16)) & 0xFFFFFFFF; c = (c + b) & 0xFFFFFFFF
        b = (b - a) & 0xFFFFFFFF; b ^= ((a << 19) | (a >> 13)) & 0xFFFFFFFF; a = (a + c) & 0xFFFFFFFF
        c = (c - b) & 0xFFFFFFFF; c ^= ((b << 4) | (b >> 28)) & 0xFFFFFFFF; b = (b + a) & 0xFFFFFFFF
        length -= 3
        i += 3
    if length == 3:
        c = (c + words[i + 2]) & 0xFFFFFFFF
    if length >= 2:
        b = (b + words[i + 1]) & 0xFFFFFFFF
    if length >= 1:
        a = (a + words[i]) & 0xFFFFFFFF
        # jhash_final
        c ^= b; c = (c - ((b << 14) | (b >> 18))) & 0xFFFFFFFF
        a ^= c; a = (a - ((c << 11) | (c >> 21))) & 0xFFFFFFFF
        b ^= a; b = (b - ((a << 25) | (a >> 7))) & 0xFFFFFFFF
        c ^= b; c = (c - ((b << 16) | (b >> 16))) & 0xFFFFFFFF
        a ^= c; a = (a - ((c << 4) | (c >> 28))) & 0xFFFFFFFF
        b ^= a; b = (b - ((a << 14) | (a >> 18))) & 0xFFFFFFFF
        c ^= b; c = (c - ((b << 24) | (b >> 8))) & 0xFFFFFFFF
    return c


def analyze(path):
    counts = Counter()          # 用户栈 -> 样本数
    for line in open(path):
        p = line.split()
        if len(p) < 6:
            continue
        nw, nk = int(p[3]), int(p[4])
        frames = [int(v, 16) for v in p[5:5 + nw]]
        user = tuple(frames[nk:])
        counts[user] += 1
    # 64 位指纹（llm-prof）
    fp64 = {}
    for st in counts:
        h = SEED
        for w in st:
            h = fp_step(h, w)
        fp64[st] = h
    # 32 位截断（bpf_get_stackid 风格：栈字节的 jhash2）
    words_cache = {}
    def words_of(st):
        if st not in words_cache:
            ws = []
            for w in st:
                ws.append(w & 0xFFFFFFFF)
                ws.append((w >> 32) & 0xFFFFFFFF)
            words_cache[st] = ws
        return words_cache[st]
    total = sum(counts.values())
    print(f"{path}: distinct 用户栈 {len(counts)} / 样本 {total}")
    # bpf_get_stackid 的 ID = jhash2(...) & (n_buckets-1)：buckets 由
    # stack map 容量决定（perf 默认值级）。64 位指纹作为对照。
    by64 = defaultdict(list)
    for st, f in fp64.items():
        by64[f].append(st)
    coll64 = sum(1 for v in by64.values() if len(v) > 1)
    for nb in (4096, 16384, 65536, 1 << 32):
        mask = nb - 1 if nb < (1 << 32) else 0xFFFFFFFF
        by32 = defaultdict(list)
        for st in counts:
            by32[jhash32(words_of(st), 0) & mask].append(st)
        coll = {i: v for i, v in by32.items() if len(v) > 1}
        affected = sum(sum(counts[st] for st in v[1:]) for v in coll.values())
        pct = affected / total * 100
        tag = " (full 32-bit)" if nb >= (1 << 32) else ""
        print(f"  n_buckets={nb:>7}{tag}: {len(coll):>4} 冲突组, "
              f"{pct:5.2f}% 样本受影响（覆盖语义）")
    print(f"  64-bit 指纹冲突组: {coll64}（理论 ~0）")


def main():
    for p in sys.argv[1:]:
        analyze(p)
        print()


if __name__ == "__main__":
    main()

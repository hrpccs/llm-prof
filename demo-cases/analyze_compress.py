#!/usr/bin/env python3
"""M0 数据画像：从 llm-prof -sample-stream 导出的样本流计算压缩相关特征。

用法:
    analyze_compress.py <stream.txt> [window_sec] [sampling_rate]

输入格式（每行）:
    <KTime> <PID> <TID> <numFrames> <frame0> <frame1> ...

输出:
    1. 总样本 / 地址级 distinct 栈 / 冗余比
    2. distinct 收敛曲线（每窗口累计 distinct + 新增）
    3. top-K 覆盖率（K=1,5,20）
    4. 栈长分布
    5. 相邻窗口加权 Jaccard（时间局部性，差分收益的上界线索）
    6. 熵 / 转移熵（Miller-Madow 校正）
"""
import sys
from collections import Counter, defaultdict


def parse(path):
    samples = []  # (t_sec, pid, tid, frames_tuple)
    with open(path) as f:
        for line in f:
            parts = line.split()
            if len(parts) < 5:
                continue
            ktime = int(parts[0])
            pid, tid = int(parts[1]), int(parts[2])
            nf = int(parts[3])
            frames = tuple(parts[4:4 + nf])
            samples.append((ktime, pid, tid, frames))
    return samples


def miller_madow(hist_counts, n):
    """Miller-Madow 偏置校正的经验熵（bit/样本）。"""
    import math
    k = len(hist_counts)  # 支撑集大小（观测到的 distinct）
    h = -sum(c / n * math.log2(c / n) for c in hist_counts)
    return h + (k - 1) / (2 * n * math.log(2))


def main():
    path = sys.argv[1]
    win_sec = float(sys.argv[2]) if len(sys.argv) > 2 else 1.0
    rate = float(sys.argv[3]) if len(sys.argv) > 3 else 1000.0

    samples = parse(path)
    if not samples:
        print("empty stream")
        return
    t0 = samples[0][0] / 1e9
    total = len(samples)

    # 地址级 distinct 与 top-K
    cnt = Counter(s for _, _, _, s in samples)
    distinct = len(cnt)
    print(f"样本 {total} | 地址级 distinct 栈 {distinct} | 冗余比 {total/distinct:.1f}:1")
    top = cnt.most_common()
    for k in (1, 5, 20):
        cov = sum(c for _, c in top[:k]) / total * 100
        print(f"  top{k} 覆盖率: {cov:.1f}%")
    h = miller_madow(list(cnt.values()), total)
    print(f"熵 H(栈): {h:.2f} bit/样本 (Miller-Madow)")
    print(f"  定长 32bit ID 冗余: {32/h:.1f}x | 熵下界带宽: {rate*h/8/1024:.1f} KB/s @ {rate:.0f}Hz")

    # 收敛曲线 + 窗口 Jaccard（按 ktime 分窗口）
    n_win = max(1, int((samples[-1][0] - samples[0][0]) / 1e9 / win_sec))
    wins = defaultdict(Counter)
    for t, _, _, s in samples:
        wi = min(int((t / 1e9 - t0) / win_sec), n_win - 1)
        wins[wi][s] += 1
    cum = Counter()
    prev_cum_len = 0
    print(f"\n收敛曲线（窗口 {win_sec}s，共 {n_win} 窗口）:")
    print(f"  {'窗口':>4} {'样本':>6} {'累计distinct':>12} {'新增':>6} {'窗口Jaccard':>11}")
    prev = None
    jacs = []
    for wi in range(n_win):
        w = wins[wi]
        if not w:
            continue
        cum.update(w)
        jac = 0.0
        if prev is not None and prev and w:
            inter = sum(min(prev[k], w[k]) for k in prev.keys() & w.keys())
            uni = sum(prev.values()) + sum(w.values()) - inter
            jac = inter / uni if uni else 1.0
            jacs.append(jac)
        new = len(cum) - prev_cum_len
        prev_cum_len = len(cum)
        print(f"  {wi:>4} {sum(w.values()):>6} {len(cum):>12} {new:>6} {jac:>11.3f}")
        prev = w
    if jacs:
        print(f"\n相邻窗口加权 Jaccard: 均值 {sum(jacs)/len(jacs):.3f} （1.0 = 完全稳定，差分收益上界）")
        print(f"  最后一窗口 distinct {len(wins[n_win-1])}（稳态窗口活跃栈数）")


if __name__ == "__main__":
    main()

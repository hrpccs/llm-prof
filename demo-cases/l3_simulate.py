#!/usr/bin/env python3
"""L3 模拟：把 llm-prof 符号化 txt 的栈按"函数粒度"规约，量化函数级 distinct。

输入：llm-prof -topn 0 的 txt 输出（每行: count 叶<-...<-根，帧已符号化）。
帧规约规则（模拟内核侧 L3 offset->funcID 归一化 + python 行号->函数）：
  - "funcname (file:line/offset)" -> funcname
  - "0xADDR"（未符号化，JIT/匿名）-> 保留原地址（L3 无法归一化的部分，如实呈现）
输出：
  - 地址级(P C/行粒度) vs 函数级 distinct 与冗余比
  - 函数级 top-K 覆盖率
  - 无法归一化（JIT/匿名）帧占比
用法：l3_simulate.py <llm-prof.txt>
"""
import re
import sys
from collections import Counter


def normalize(fr):
    """帧 -> 函数粒度 key。返回 (是否归一化成功, key)。"""
    fr = fr.strip()
    m = re.match(r"^0x[0-9a-f]+$", fr)
    if m:
        return False, fr  # 未符号化帧（JIT/匿名/内核未解析）
    # "funcname (file:...)" 或 "<module> (file:line)" 或纯内核符号
    m2 = re.match(r"^(.*?)\s+\(.*\)$", fr)
    if m2:
        return True, m2.group(1)
    return True, fr  # 内核符号等


def main():
    path = sys.argv[1]
    addr_stacks = Counter()   # 原始符号化栈（PC/行粒度）
    func_stacks = Counter()   # 函数粒度栈
    norm_frames = 0
    unnorm_frames = 0
    total_frames = 0

    with open(path) as f:
        lines = f.readlines()
    m = re.match(r"total samples: (\d+), distinct stacks: (\d+)", lines[0])
    total = int(m.group(1))
    print(f"样本 {total} | 符号化 distinct（PC/行粒度）{int(m.group(2))}")

    for line in lines[2:]:
        mm = re.match(r"\s*(\d+)\s+(.*)", line.strip())
        if not mm:
            continue
        n = int(mm.group(1))
        frames = [fr.strip() for fr in mm.group(2).split("<-") if fr.strip()]
        key = tuple(frames)
        addr_stacks[key] += n
        fkey = []
        for fr in frames:
            ok, k = normalize(fr)
            if ok:
                norm_frames += n
            else:
                unnorm_frames += n
            total_frames += n
            fkey.append(k)
        func_stacks[tuple(fkey)] += n

    print(f"地址级 distinct {len(addr_stacks)} | 冗余比 {total/len(addr_stacks):.1f}:1")
    print(f"函数级 distinct {len(func_stacks)} | 冗余比 {total/len(func_stacks):.1f}:1")
    print(f"归一化收益: distinct {len(addr_stacks)} -> {len(func_stacks)} "
          f"({len(addr_stacks)/len(func_stacks):.2f}x)")
    top = func_stacks.most_common()
    for k in (1, 5, 20):
        cov = sum(c for _, c in top[:k]) / total * 100
        print(f"  函数级 top{k} 覆盖率: {cov:.1f}%")
    print(f"未归一化帧（JIT/匿名/未解析）: {unnorm_frames/total_frames*100:.1f}%")
    # 函数级 distinct 内，纯地址帧栈占比（JIT 主导的栈）
    pure_jit = sum(1 for k in func_stacks if any(re.match(r"^0x[0-9a-f]+$", f) for f in k))
    print(f"含未解析帧的 distinct 栈: {pure_jit}/{len(func_stacks)}")


if __name__ == "__main__":
    main()

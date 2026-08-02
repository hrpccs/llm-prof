#!/usr/bin/env python3
"""栈深度负载：每轮递归 depth 层，每层做 work → 采样点均匀分布在深栈中。
用法: stack_bench.py <depth> <total_iters>"""
import sys
import time


def work(n):
    x = 1
    for _ in range(n):
        x = (x * 2654435761 + 11) % (2**31 - 1)
    return x


def recurse(depth, per):
    if depth <= 1:
        return work(per)
    return work(per) + recurse(depth - 1, per)


def main():
    depth = int(sys.argv[1])
    total = int(sys.argv[2])
    per = max(1, total // (300 * depth))  # 每层 work 量，300 轮 × depth 层
    t0 = time.perf_counter()
    for _ in range(300):
        recurse(depth, per)
    print(f"elapsed={time.perf_counter()-t0:.6f}")


if __name__ == "__main__":
    main()

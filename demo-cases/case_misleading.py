#!/usr/bin/env python3
"""Case D: 误导性热点。noisy() 被高频调用（20k/s × 5µs）但总时间少；
real_bottleneck() 低频调用（10/s × 25ms）但总时间多（真实瓶颈 ~80%）。
测工具能否正确区分"调用频率"与"真实耗时"（看时间占比而非出现次数）。"""
import time


def noisy(x):
    # 单次 ~5µs：高频短调用
    return (x * 31 + 7) & 0xFFFF


def real_bottleneck(n):
    # 单次 ~25ms：低频长调用
    x = 1
    for _ in range(n):
        x = (x * 2654435761 + 11) % (2**31 - 1)
    return x


def main():
    n_heavy = 200        # 200 × 25ms ≈ 5s（真实热点 80%）
    n_noisy = 700 * 400  # 高频调用
    t0 = time.perf_counter()
    x = 1
    for i in range(n_heavy):
        x = real_bottleneck(650_000)
        for _ in range(400):
            x = noisy(x)
    print(f"elapsed={time.perf_counter()-t0:.6f} x={x}")


if __name__ == "__main__":
    main()

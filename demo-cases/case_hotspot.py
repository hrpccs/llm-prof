#!/usr/bin/env python3
"""Case A: 单线程 CPU 热点。真实问题：bottleneck() 占 ~90% 时间。
期望定位：栈顶帧 ~90% 命中 bottleneck。"""
import time


def bottleneck(n):
    x = 1
    for _ in range(n):
        x = (x * 2654435761 + 11) % (2**31 - 1)
    return x


def other_work(n):
    x = 0
    for _ in range(n):
        x += n & 0xFF
    return x


def main():
    total = 140_000_000  # 标定后 ~30s
    t0 = time.perf_counter()
    b = bottleneck(int(total * 0.9))
    o = other_work(int(total * 0.1))
    print(f"elapsed={time.perf_counter()-t0:.6f} b={b} o={o}")


if __name__ == "__main__":
    main()

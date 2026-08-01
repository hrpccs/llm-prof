#!/usr/bin/env python3
"""Case C: IO/等待主导。真实问题：~90% 时间在 sleep（模拟 IO/网络等待），
计算只占 ~10%。期望定位（llm-prof off-cpu）：等待点占 ~90%。"""
import time


def calc(n):
    x = 1
    for _ in range(n):
        x = (x * 2654435761 + 11) % (2**31 - 1)
    return x


def main():
    n_sleeps = 5200     # 5200 * 5ms = 26s sleep
    calc_per = 3_000_000  # 约 0.5s 计算
    t0 = time.perf_counter()
    for i in range(n_sleeps):
        time.sleep(0.005)
        if i % 50 == 0:
            calc(calc_per // 17)
    print(f"elapsed={time.perf_counter()-t0:.6f}")


if __name__ == "__main__":
    main()

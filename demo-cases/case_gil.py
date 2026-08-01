#!/usr/bin/env python3
"""Case B: 多线程 GIL 争用。真实问题：4 线程纯 Python 计算被 GIL 串行化，
大量时间在 _wait_for_tstate_lock 等待锁（真实瓶颈是"等 GIL"而非计算本身）。
期望定位（llm-prof off-cpu）：futex/_wait_for_tstate_lock 等待占大头。"""
import sys
import threading
import time


def cpu_work(n):
    x = 1
    for _ in range(n):
        x = (x * 2654435761 + 11) % (2**31 - 1)
    return x


def main():
    nthreads = int(sys.argv[1]) if len(sys.argv) > 1 else 4
    per = 11_000_000  # 每线程工作量，标定后总 ~32s（GIL 串行化）
    threads = [threading.Thread(target=cpu_work, args=(per,)) for _ in range(nthreads)]
    t0 = time.perf_counter()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    print(f"elapsed={time.perf_counter()-t0:.6f} threads={nthreads}")


if __name__ == "__main__":
    main()

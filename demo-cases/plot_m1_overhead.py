#!/usr/bin/env python3
"""M1 开销曲线绘图：CPU 开销（目标进程 elapsed 增长%）与内存带宽（ringbuf MB/s）。

输入：/tmp/m1ov.txt（每行: <rate> <mode> <elapsed> <bytes>）
输出：两张图
  /tmp/m1_cpu_overhead.png   CPU 开销 vs 采样率（off/on 两条线）
  /tmp/m1_bandwidth.png      ringbuf 带宽 vs 采样率（off/on 两条线）
  + 控制台表格
"""
import statistics
import sys
from collections import defaultdict

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

PATH = sys.argv[1] if len(sys.argv) > 1 else "/tmp/m1ov.txt"

data = defaultdict(list)  # (rate, mode) -> [(elapsed, bytes)]
for line in open(PATH):
    p = line.split()
    if len(p) != 4:
        continue
    rate, mode, elapsed, nbytes = p[0], p[1], float(p[2]), int(p[3])
    data[(rate, mode)].append((elapsed, nbytes))

base = statistics.median(e for e, _ in data[("base", "base")])
print(f"基线 elapsed: {base:.3f}s")

rates = ["100", "1000", "10000"]
fig, axes = plt.subplots(1, 2, figsize=(13, 5))

# --- 左图：CPU 开销 ---
for mode, style in (("off", "o-"), ("on", "s--")):
    xs, ys = [], []
    for r in rates:
        pts = data[(r, mode)]
        med = statistics.median(e for e, _ in pts)
        xs.append(int(r))
        ys.append((med - base) / base * 100)
        print(f"  rate={r:>6} mode={mode}: elapsed 中位 {med:.3f}s "
              f"开销 {(med-base)/base*100:+.2f}%  (n={len(pts)})")
    axes[0].plot(xs, ys, style, label=f"stack-compress={mode}")
axes[0].set_xscale("log")
axes[0].set_xticks([100, 1000, 10000])
axes[0].set_xticklabels(["100", "1000", "10000"])
axes[0].set_xlabel("sampling rate (Hz)")
axes[0].set_ylabel("CPU overhead vs baseline (%)")
axes[0].set_title("CPU overhead: target process elapsed")
axes[0].grid(True, which="both", alpha=0.3)
axes[0].legend()

# --- 右图：ringbuf 内存带宽 ---
# 采样窗口 ≈ 12s（-d 12s，attach 后 ~9s）——用实际样本数？字节/秒 = bytes / 12
for mode, style in (("off", "o-"), ("on", "s--")):
    xs, ys = [], []
    for r in rates:
        pts = data[(r, mode)]
        med_b = statistics.median(b for _, b in pts)
        xs.append(int(r))
        ys.append(med_b / 12 / 1e6)  # MB/s
        print(f"  rate={r:>6} mode={mode}: ringbuf {med_b/1e6:.2f} MB/12s "
              f"= {med_b/12/1e6:.2f} MB/s")
    axes[1].plot(xs, ys, style, label=f"stack-compress={mode}")
axes[1].set_xscale("log")
axes[1].set_xticks([100, 1000, 10000])
axes[1].set_xticklabels(["100", "1000", "10000"])
axes[1].set_xlabel("sampling rate (Hz)")
axes[1].set_ylabel("ringbuf bandwidth (MB/s)")
axes[1].set_title("Memory bandwidth: kernel->userspace ringbuf")
axes[1].grid(True, which="both", alpha=0.3)
axes[1].legend()

plt.tight_layout()
plt.savefig("/tmp/m1_cpu_overhead.png", dpi=110)
plt.savefig("/tmp/m1_bandwidth.png", dpi=110)
print("saved /tmp/m1_cpu_overhead.png /tmp/m1_bandwidth.png")

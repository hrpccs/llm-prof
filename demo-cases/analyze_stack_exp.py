#!/usr/bin/env python3
"""栈深实验分析：分解 中断/过滤 vs unwind 成本，随栈深变化。
用法: sudo python3 analyze_stack_exp.py"""
import re
import statistics
import sys

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

DEPTHS = [5, 20, 50, 100, 200]


def load(path="stack_exp.txt"):
    data = {}
    for line in open(path):
        parts = line.split()
        if len(parts) >= 3 and "samples" in parts[1]:
            data.setdefault(parts[0], []).append(int(re.search(r"\d+", parts[2]).group()))
            continue
        if len(parts) == 2:
            try:
                data.setdefault(parts[0], []).append(float(parts[1]))
            except ValueError:
                pass
    return data


def avg_frames(path):
    """统计 llm-prof txt 的平均栈帧数（加权）。"""
    try:
        lines = open(path).readlines()
    except OSError:
        return None
    total = frames_sum = 0
    for line in lines[2:]:
        m = re.match(r"\s*(\d+)\s+(.*)", line.strip())
        if not m:
            continue
        n = int(m.group(1))
        total += n
        frames_sum += n * (m.group(2).count("<-") + 1)
    return frames_sum / total if total else None


def main():
    data = load()
    print(f"{'depth':>6} {'基线s':>7} {'中断+过滤':>10} {'完整unwind':>11} {'中断成本':>8} "
          f"{'unwind成本':>10} {'样本':>6} {'平均帧数':>8}")
    rows = []
    for d in DEPTHS:
        base = statistics.median(data.get(f"d{d}-base1", []) + data.get(f"d{d}-base2", []))
        ints = statistics.median(data.get(f"d{d}-int1", []) + data.get(f"d{d}-int2", []))
        unw = statistics.median(data.get(f"d{d}-unw1", []) + data.get(f"d{d}-unw2", []))
        samples = data.get(f"d{d}-samples1", []) + data.get(f"d{d}-samples2", [])
        n = samples[0] if samples else 0
        # 帧数：取第一个 unw 输出文件
        import glob
        f = glob.glob(f"/tmp/se_d{d}_1.txt")
        avg = avg_frames(f[0]) if f else None
        int_oh = (ints - base) / base * 100
        unw_oh = (unw - base) / base * 100
        avg_s = f"{avg:.1f}" if avg else "n/a"
        rows.append((d, base, int_oh, unw_oh, unw_oh - int_oh, n, avg))
        print(f"{d:>6} {base:>7.2f} {int_oh:>9.2f}% {unw_oh:>10.2f}% "
              f"{int_oh:>7.2f}% {unw_oh - int_oh:>9.2f}% {n:>6} {avg_s:>8}")

    # 曲线
    ds = [r[0] for r in rows]
    fig, ax = plt.subplots(figsize=(9, 5.5))
    ax.plot(ds, [r[2] for r in rows], "o-", label="interrupt + pid-filter", color="#888888")
    ax.plot(ds, [r[3] - r[2] for r in rows], "s-", label="unwind (incl. collect/submit)", color="#1f77b4")
    ax.plot(ds, [r[3] for r in rows], "^-", label="total", color="#d62728")
    ax.set_xscale("log")
    ax.set_xticks(ds)
    ax.set_xticklabels([str(d) for d in ds])
    ax.set_xlabel("recursion depth (stack depth)")
    ax.set_ylabel("overhead vs baseline (%)")
    ax.set_title("Interrupt vs unwind cost vs stack depth (@10000Hz, 12s sample)")
    ax.legend()
    ax.grid(alpha=0.3)
    plt.tight_layout()
    plt.savefig("stack_exp.svg")
    print("saved stack_exp.svg")


if __name__ == "__main__":
    main()

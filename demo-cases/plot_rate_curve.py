#!/usr/bin/env python3
"""开销-采样率曲线图：llm-prof vs py-spy（10Hz-10000Hz）。
用法: python3 plot_rate_curve.py [rate_curve.txt]"""
import re
import statistics
import sys

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

RATES = [10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000]

def parse(path):
    data = {}
    for line in open(path):
        parts = line.split()
        if len(parts) != 2:
            continue
        key, val = parts[0], float(parts[1])
        # key like pyspy100-1 / llmprof1000-2 / base1
        m = re.match(r"([a-z]+)(\d+)?(?:-(\d))?", key)
        if m:
            data.setdefault(key, []).append(val)
    return data

def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "rate_curve.txt"
    data = parse(path)
    base = statistics.median(data.get("base1", []) + data.get("base2", []) + data.get("base3", []))
    print(f"baseline median: {base:.3f}s")

    lp, ps = [], []
    for r in RATES:
        lpv = [v[0] for k, v in data.items() if k.startswith(f"llmprof{r}")]
        psv = [v[0] for k, v in data.items() if k.startswith(f"pyspy{r}")]
        if lpv:
            med = statistics.median(lpv)
            lp.append((r, (med - base) / base * 100))
        if psv:
            med = statistics.median(psv)
            ps.append((r, (med - base) / base * 100))
        print(f"rate {r:6d}: llm-prof {'n/a' if not lpv else f'{statistics.median(lpv):.2f}s ({(statistics.median(lpv)-base)/base*100:+.1f}%)'}  "
              f"py-spy {'n/a' if not psv else f'{statistics.median(psv):.2f}s ({(statistics.median(psv)-base)/base*100:+.1f}%)'}")

    fig, ax = plt.subplots(figsize=(9, 5.5))
    if lp:
        x, y = zip(*lp)
        ax.plot(x, y, "o-", color="#1f77b4", lw=2, label="llm-prof (eBPF, per-CPU)")
    if ps:
        x, y = zip(*ps)
        ax.plot(x, y, "s-", color="#d62728", lw=2, label="py-spy (ptrace, per-process)")
    ax.set_xscale("log")
    ax.set_xticks(RATES)
    ax.set_xticklabels([str(r) for r in RATES], rotation=45)
    ax.set_xlabel("sampling rate (Hz, log scale)")
    ax.set_ylabel("overhead vs baseline (%)")
    ax.set_title("Overhead vs sampling rate: llm-prof vs py-spy\n"
                 "(single-thread CPU-bound Python, 12s attach sample, 2-run median)")
    ax.legend()
    ax.grid(True, which="both", alpha=0.3)
    plt.tight_layout()
    plt.savefig("rate_curve.svg")
    plt.savefig("rate_curve.png", dpi=150)
    print("saved rate_curve.svg / rate_curve.png")

if __name__ == "__main__":
    main()

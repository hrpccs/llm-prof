#!/usr/bin/env python3
"""分析矩阵结果：开销（elapsed 相对基线增长）+ 定位能力。
口径：
- py-spy：栈顶帧 = 叶帧（Python 语义层调用点）
- llm-prof：采样点 PC 在 eval-loop 内部（原生帧），Python 函数名在栈下层——
  统计每个样本"第一个 Python 帧"（含 .py: / site-packages / <frozen）作为 Python 层栈顶；
  另统计等待帧（futex/_wait_for_tstate_lock、clock_nanosleep）在栈中的出现率。
用法: sudo python3 analyze_matrix.py"""
import os
import re
import statistics
from collections import Counter

BASE = "/home/ubuntu/ai-infra"
CASES = ["hotspot", "gil", "io", "misleading"]
PY_FRAME = re.compile(r"\.py:\d+|site-packages|<frozen|<interpreter|<module>")
WAIT_GIL = re.compile(r"futex|_wait_for_tstate_lock")
WAIT_SLEEP = re.compile(r"clock_nanosleep|hrtimer_nanosleep|time\.sleep|case_io\.py:19")


def parse_elapsed(path=BASE + "/mx_results.txt"):
    data = {}
    if not os.path.exists(path):
        return data
    for line in open(path):
        line = line.strip()
        if not line or "=" in line:
            continue
        key, _, val = line.rpartition(" ")
        try:
            data.setdefault(key, []).append(float(val))
        except ValueError:
            continue
    return data


def pyspy_stacks(path):
    """py-spy raw: 每行 根;...;叶（可带 count）。返回 (叶分布, 等待帧出现率)。"""
    leaf = Counter()
    n = 0
    wait_gil = wait_sleep = 0
    if not os.path.exists(path):
        return leaf, 0, 0, 0
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        parts = line.rsplit(" ", 1)
        try:
            cnt = int(parts[1])
        except (ValueError, IndexError):
            cnt = 1
        frames = [f.strip() for f in parts[0].split(";") if f.strip()]
        if not frames:
            continue
        n += cnt
        leaf[frames[-1]] += cnt
        if WAIT_GIL.search(parts[0]):
            wait_gil += cnt
        if WAIT_SLEEP.search(parts[0]):
            wait_sleep += cnt
    return leaf, n, wait_gil, wait_sleep


def llmprof_stacks(path):
    """llm-prof txt: 每行 'count 叶<-...<-根'（左=栈底 右=栈顶）。
    返回 (首个 Python 帧分布, 样本数, GIL 等待率, sleep 等待率)。"""
    py_top = Counter()
    n = 0
    wait_gil = wait_sleep = 0
    if not os.path.exists(path):
        return py_top, 0, 0, 0
    lines = open(path).readlines()
    for line in lines[2:]:
        line = line.strip()
        if not line:
            continue
        m = re.match(r"\s*(\d+)\s+(.*)", line)
        if not m:
            continue
        cnt = int(m.group(1))
        frames = [f.strip() for f in m.group(2).split("<-") if f.strip()]
        if not frames:
            continue
        n += cnt
        # 从栈顶（右）往栈底找第一个 Python 帧
        py = next((f for f in reversed(frames) if PY_FRAME.search(f)), None)
        if py:
            py_top[py] += cnt
        if WAIT_GIL.search(m.group(2)):
            wait_gil += cnt
        if WAIT_SLEEP.search(m.group(2)):
            wait_sleep += cnt
    return py_top, n, wait_gil, wait_sleep


def fmt_top(dist, total):
    out = []
    for fr, c in dist.most_common(3):
        out.append(f"{fr[:52]} {c/total*100:.0f}%")
    return " | ".join(out) if out else "(无 Python 帧)"


def main():
    elapsed = parse_elapsed()
    print(f"{'case':<11}{'config':<20}{'elapsed':>8}{'overhead':>9}  n     Python-top(占比)      GIL等待  sleep等待")
    print("-" * 135)
    for case in CASES:
        bl = [elapsed.get(f"{case} baseline1", []), elapsed.get(f"{case} baseline2", [])]
        bl = [v for v in bl if v]
        base = statistics.median(bl[0] + bl[1]) if bl else 0
        print(f"{case:<11}{'baseline':<20}{base:>8.1f}{0:>9.1f}%")

        for key, tool, cfg in [
            (f"{case} pyspy100", "py-spy", "100Hz"),
            (f"{case} pyspy1000", "py-spy", "1000Hz"),
            (f"{case} lp100off0", "llm-prof", "100Hz off=0"),
            (f"{case} lp100off1.0", "llm-prof", "100Hz off=1.0"),
            (f"{case} lp1000off0", "llm-prof", "1000Hz off=0"),
            (f"{case} lp1000off1.0", "llm-prof", "1000Hz off=1.0"),
        ]:
            vals = elapsed.get(key, [])
            if not vals:
                print(f"{case:<11}{tool+' '+cfg:<20}{'FAIL':>8}")
                continue
            med = statistics.median(vals)
            oh = (med - base) / base * 100
            if tool == "py-spy":
                rate = cfg.split("Hz")[0]
                out = f"/tmp/mx_{case}_ps{rate}.txt"
                top, n, wg, ws = pyspy_stacks(out)
            else:
                rate = cfg.split("Hz")[0]
                off = "1.0" if "1.0" in cfg else "0"
                out = f"/tmp/mx_{case}_lp{rate}_off{off}.txt"
                top, n, wg, ws = llmprof_stacks(out)
            print(f"{case:<11}{tool+' '+cfg:<20}{med:>8.1f}{oh:>8.1f}%  {n:<6} "
                  f"{fmt_top(top, max(n,1)):<44} {wg/max(n,1)*100:>4.0f}%   {ws/max(n,1)*100:>4.0f}%")


if __name__ == "__main__":
    main()

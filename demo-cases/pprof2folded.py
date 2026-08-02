#!/usr/bin/env python3
"""pprof -raw 输出 → flamegraph.pl 折叠栈（root;...;leaf count）。
用法: pprof2folded.py <raw.txt> > folded.txt
内核/调度样板帧（与之前 -ignore 相同集合）从栈中剔除。"""
import re
import sys

IGNORE = re.compile(
    r"asm_sysvec|sysvec_|irqentry|entry_SYSCALL|do_syscall_64|x64_sys_call|"
    r"finish_task_switch|schedule|__schedule|_raw_spin_unlock_irqrestore|"
    r"wake_up_q|try_to_wake_up")

def parse(path):
    locs = {}      # loc_id -> 帧名
    samples = []   # (count, [loc_ids leaf-first])
    section = None
    for line in open(path):
        line = line.rstrip()
        if not line:
            section = None
            continue
        if line.startswith("Samples:") or line == "Samples":
            section = "samples"; continue
        if line == "Locations":
            section = "locs"; continue
        if line == "Functions":
            section = "funcs"; continue
        if section == "locs":
            # 固定格式（空格分隔）:
            #   id: 0xADDR M=N [NAME] FILE:LINE s=LINE[(EXTRA)]    （有名字）
            #   id: 0xADDR M=N 0xPC :0 s=0                          （无名字）
            #   id: 0xADDR M=N FILE:LINE s=LINE(<module>)           （名字在 EXTRA）
            parts = line.split()
            if len(parts) >= 5 and parts[2].startswith("M="):
                loc_id = int(parts[0][:-1])
                tok = parts[3]
                if re.match(r"^0x[0-9a-f]+$", tok):
                    # 无名字帧：名字取 PC 地址；带名字的帧 parts[3] 是函数名
                    locs[loc_id] = tok
                elif ":" in tok:
                    # 名字为空、文件:行 占位：从 s=LINE(EXTRA) 提取附加名
                    m = re.search(r"s=\d+\((\S+)\)", line)
                    if m:
                        locs[loc_id] = m.group(1)
                    else:
                        locs[loc_id] = tok.split(":")[0]
                else:
                    locs[loc_id] = tok
        elif section == "samples":
            m = re.match(r"\s*(\d+):\s*([\d\s]+)$", line)
            if m:
                ids = [int(x) for x in m.group(2).split()]
                samples.append((int(m.group(1)), ids))
    return locs, samples

def main():
    locs, samples = parse(sys.argv[1])
    agg = {}
    for count, ids in samples:
        # ids 是 leaf-first；折叠栈需要 root-first
        frames = [locs.get(i, str(i)) for i in reversed(ids)]
        frames = [f for f in frames if not IGNORE.search(f)]
        if not frames:
            continue
        key = ";".join(frames)
        agg[key] = agg.get(key, 0) + count
    for key, count in sorted(agg.items(), key=lambda kv: -kv[1]):
        print(f"{key} {count}")

if __name__ == "__main__":
    main()

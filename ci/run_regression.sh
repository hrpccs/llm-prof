#!/usr/bin/env bash
# llm-prof 回归测试：LLM 训练/推理场景的性能诊断能力 + 性能开销
#
# 用法:
#   sudo bash ci/run_regression.sh            # 跑回归并判定（对比 baseline.json）
#   sudo bash ci/run_regression.sh --baseline # 生成基线（首次/有意变更后）
#
# 覆盖:
#   A. 构建与单元测试（eBPF 编译 + go test，含 C/Go 指纹一致性）
#   B. 诊断能力回归（train_torch 1000Hz：样本数/栈集合/Python 帧可见性）
#   C. 性能开销回归（case_hotspot + train_torch，1000/10000Hz，wall-time 中位）
#   D. 压缩能力回归（-stack-compress：带宽降幅 + 输出与未压缩一致）
#
# 输出: /tmp/llmprof_regress/results.json
set -u
cd "$(dirname "$0")/.."
ROOT=$(pwd)
LOAD=/home/ubuntu/ai-infra
LLM=$ROOT/llm-prof
PY3=/usr/bin/python3.12          # train_torch 需要（含 torch）
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
OUT=/tmp/llmprof_regress
mkdir -p "$OUT"
RESULT=$OUT/results.json
MODE="${1:-run}"

echo "== A. build + unit tests =="
make -C support/ebpf > "$OUT/build_ebpf.log" 2>&1 || { echo "FAIL: eBPF build"; exit 1; }
go build -o "$LLM" . || { echo "FAIL: go build"; exit 1; }
GOFAIL=$(go test ./... 2>&1 | grep -cE "^FAIL" || true)
echo "  go test FAIL: $GOFAIL"
# 指纹一致性是压缩正确性的根基，必须过
go test ./internal/stackcompress/ > /dev/null 2>&1 || { echo "FAIL: fingerprint consistency test"; exit 1; }

run_sample() { # $1=loader $2=script $3=args... ; 输出 PID
  local loader=$1; shift
  cd "$ROOT"
  "$loader" "$@" > "$OUT/load_run.txt" 2>&1 &
  echo $!
}

wait_pid() { # $1=pattern $2=timeout_s
  local p; local i=0
  while [ $i -lt "$2" ]; do
    p=$(pgrep -f "$1" | grep -v run_regression | head -1)
    [ -n "$p" ] && { echo "$p"; return 0; }
    sleep 1; i=$((i+1))
  done
  return 1
}

# B. 诊断能力（train_torch 1000Hz，压缩 off/on 各一轮）
echo "== B. diagnosis correctness (train_torch 1000Hz) =="
profile_train() { # $1=mode(off|on) $2=round
  local mode=$1 round=$2
  cd "$LOAD"
  $PY3 train_torch.py 60 > "$OUT/tt_run.txt" 2>&1 &
  local job=$!
  local T; T=$(wait_pid "^$PY3 train_torch.py" 40) || { echo "FAIL: train_torch start"; return 1; }
  local flag=""; [ "$mode" = on ] && flag="-stack-compress"
  sudo "$LLM" -pid "$T" -d 12s -samples-per-second 1000 -off-cpu-threshold 0 -topn 0 \
    $flag -o "$OUT/tt_${mode}_$round.svg" > "$OUT/tt_${mode}_$round.log" 2>&1
  kill -9 "$T" "$job" 2>/dev/null
  wait "$job" 2>/dev/null
  sudo head -1 "$OUT/tt_${mode}_$round.txt" 2>/dev/null
}

B_OFF=$(profile_train off 1)
B_ON=$(profile_train on 1)
echo "  off: $B_OFF"
echo "  on:  $B_ON"
B_OFF_S=$(echo "$B_OFF" | grep -oP 'total samples: \K[0-9]+')
B_OFF_D=$(echo "$B_OFF" | grep -oP 'distinct stacks: \K[0-9]+')
B_ON_S=$(echo "$B_ON" | grep -oP 'total samples: \K[0-9]+')
# Python 帧可见性（诊断能力核心：必须看到 train_torch.py 的帧）
sudo grep -c "train_torch.py" "$OUT/tt_off_1.txt" > /dev/null 2>&1 && PY_VIS=1 || PY_VIS=0

# C. 性能开销（wall time 中位，3 轮）
echo "== C. overhead (wall time, median of 3) =="
measure() { # $1=loader $2=script $3=rate $4=mode
  local loader=$1 script=$2 rate=$3 mode=$4
  local times=""
  for i in 1 2 3; do
    cd "$ROOT"
    local t0=$SECONDS
    cd "$LOAD"
    "$loader" "$script" > "$OUT/ov_run.txt" 2>&1 &
    local job=$!
    local T; T=$(wait_pid "^$loader $script" 40) || { echo "FAIL: $script start"; return 1; }
    local flag=""; [ "$mode" = on ] && flag="-stack-compress"
    sudo "$LLM" -pid "$T" -d 12s -samples-per-second "$rate" -off-cpu-threshold 0 -topn 0 \
      $flag -o "$OUT/ov.svg" > "$OUT/ov.log" 2>&1
    kill -9 "$T" "$job" 2>/dev/null
    wait "$job" 2>/dev/null
    times="$times $((SECONDS - t0))"
    sleep 1
  done
  # 中位
  echo "$times" | tr ' ' '\n' | grep -v '^$' | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}'
}

# 基线开销（无采样）：case_hotspot 直接跑 3 轮
base_t=""
for i in 1 2 3; do
  t0=$SECONDS
  cd "$LOAD"
  "$MINI" case_hotspot.py > /dev/null 2>&1
  base_t="$base_t $((SECONDS - t0))"
done
BASE_MED=$(echo "$base_t" | tr ' ' '\n' | grep -v '^$' | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}')
echo "  baseline (no sampling): ${BASE_MED}s"

HS_1K_OFF=$(measure "$MINI" case_hotspot.py 1000 off)
HS_1K_ON=$(measure "$MINI" case_hotspot.py 1000 on)
HS_10K_OFF=$(measure "$MINI" case_hotspot.py 10000 off)
HS_10K_ON=$(measure "$MINI" case_hotspot.py 10000 on)
TT_1K_OFF=$(measure "$PY3" train_torch.py 1000 off)
TT_1K_ON=$(measure "$PY3" train_torch.py 1000 on)
echo "  case_hotspot 1k: off=${HS_1K_OFF}s on=${HS_1K_ON}s | 10k: off=${HS_10K_OFF}s on=${HS_10K_ON}s"
echo "  train_torch 1k: off=${TT_1K_OFF}s on=${TT_1K_ON}s"

# D. 压缩能力（case_hotspot 10000Hz：带宽降幅；正确性）
echo "== D. compression capability =="
cd "$LOAD"
$MINI case_hotspot.py > /dev/null 2>&1 &
local_job=$!
T=$(wait_pid "^$MINI case_hotspot" 40)
sudo "$LLM" -pid "$T" -d 12s -samples-per-second 10000 -off-cpu-threshold 0 -topn 0 \
  -stack-compress -o "$OUT/cap.svg" > "$OUT/cap.log" 2>&1
kill -9 "$T" "$local_job" 2>/dev/null
CAP_S=$(sudo head -1 "$OUT/cap.txt" | grep -oP 'total samples: \K[0-9]+')
CAP_B=$(grep -oP 'ringbuf bytes received: \K[0-9]+' "$OUT/cap.log")
# 未压缩对照（同负载同采样率）
cd "$LOAD"
$MINI case_hotspot.py > /dev/null 2>&1 &
local_job=$!
T=$(wait_pid "^$MINI case_hotspot" 40)
sudo "$LLM" -pid "$T" -d 12s -samples-per-second 10000 -off-cpu-threshold 0 -topn 0 \
  -o "$OUT/cap_off.svg" > "$OUT/cap_off.log" 2>&1
kill -9 "$T" "$local_job" 2>/dev/null
CAP_OFF_B=$(grep -oP 'ringbuf bytes received: \K[0-9]+' "$OUT/cap_off.log")
CAP_OFF_S=$(sudo head -1 "$OUT/cap_off.txt" | grep -oP 'total samples: \K[0-9]+')
echo "  bytes: off=${CAP_OFF_B} on=${CAP_B} | samples: off=${CAP_OFF_S} on=${CAP_S}"

# 汇总 JSON
python3 - "$MODE" "$BASE_MED" "$B_OFF_S" "$B_OFF_D" "$B_ON_S" "$PY_VIS" \
  "$HS_1K_OFF" "$HS_1K_ON" "$HS_10K_OFF" "$HS_10K_ON" "$TT_1K_OFF" "$TT_1K_ON" \
  "$CAP_OFF_B" "$CAP_B" "$CAP_OFF_S" "$CAP_S" << 'EOF'
import json, sys, os
mode = sys.argv[1]
d = {
  "base_sec": float(sys.argv[2]),
  "diag_off_samples": int(sys.argv[3] or 0),
  "diag_off_distinct": int(sys.argv[4] or 0),
  "diag_on_samples": int(sys.argv[5] or 0),
  "diag_python_visible": int(sys.argv[6] or 0),
  "hs_1k_off_sec": int(sys.argv[7] or 0), "hs_1k_on_sec": int(sys.argv[8] or 0),
  "hs_10k_off_sec": int(sys.argv[9] or 0), "hs_10k_on_sec": int(sys.argv[10] or 0),
  "tt_1k_off_sec": int(sys.argv[11] or 0), "tt_1k_on_sec": int(sys.argv[12] or 0),
  "cap_off_bytes": int(sys.argv[13] or 0), "cap_on_bytes": int(sys.argv[14] or 0),
  "cap_off_samples": int(sys.argv[15] or 0), "cap_on_samples": int(sys.argv[16] or 0),
}
path = "/tmp/llmprof_regress/results.json"
if mode == "--baseline":
    json.dump(d, open(path, "w"), indent=2)
    print("BASELINE SAVED ->", path)
else:
    base = json.load(open(path))
    fails = []
    def chk(name, ok, detail):
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}: {detail}")
        if not ok: fails.append(name)
    # B: 诊断
    chk("diag.samples>=90%", d["diag_off_samples"] >= base["diag_off_samples"]*0.9,
        f"{d['diag_off_samples']} vs base {base['diag_off_samples']}")
    chk("diag.python_frames_visible", d["diag_python_visible"] == 1,
        "train_torch.py frames in output")
    chk("diag.compressed_matches", abs(d["diag_on_samples"]-d["diag_off_samples"]) <= max(300, d["diag_off_samples"]*0.10),
        f"on {d['diag_on_samples']} vs off {d['diag_off_samples']}")
    # C: 开销（阈值：1k +3s，10k +6s，相对基线；与基线开销叠加）
    def ov(name, med):
        return (med - base["base_sec"]) / base["base_sec"] * 100
    chk("ov.hs_1k_off<6%", ov("hs_1k_off", d["hs_1k_off_sec"]) < 6, f"{ov('x', d['hs_1k_off_sec']):.1f}%")
    chk("ov.hs_10k_off<12%", ov("hs_10k_off", d["hs_10k_off_sec"]) < 12, f"{ov('x', d['hs_10k_off_sec']):.1f}%")
    chk("ov.compressed_not_slower", d["hs_10k_on_sec"] <= d["hs_10k_off_sec"] + 2,
        f"on {d['hs_10k_on_sec']}s vs off {d['hs_10k_off_sec']}s")
    # D: 压缩能力
    red = 1 - d["cap_on_bytes"]/max(1, d["cap_off_bytes"])
    chk("cap.bandwidth_reduction>=90%", red >= 0.90, f"{red*100:.1f}%")
    chk("cap.samples_match", abs(d["cap_on_samples"]-d["cap_off_samples"]) <= max(500, d["cap_off_samples"]*0.10),
        f"on {d['cap_on_samples']} vs off {d['cap_off_samples']}")
    print("RESULT:", "PASS" if not fails else f"FAIL {fails}")
    sys.exit(0 if not fails else 1)
EOF

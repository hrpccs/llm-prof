#!/usr/bin/env bash
# 开销-采样率曲线：llm-prof vs py-spy，10Hz -> 10000Hz
# 负载：case_hotspot.py（单线程 CPU 密集，~30s 固定工作量）
# 每配置 2 轮取中位，输出 rate_curve.txt
set -u
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
OUT=rate_curve.txt
RATES="10 20 50 100 200 500 1000 2000 5000 10000"
: > "$OUT"

wait_pid() {
  local p="" i=0
  while [ $i -lt 40 ]; do
    sleep 1; i=$((i+1))
    p=$(pgrep -f "$1" | head -1)
    [ -n "$p" ] && break
  done
  echo "$p"
}

# 基线 ×3
for i in 1 2 3; do
  $MINI case_hotspot.py 2>/dev/null | grep -oP 'elapsed=\K[0-9.]+' | sed "s/^/base$i /" >> "$OUT"
done

for rate in $RATES; do
  # py-spy 2 轮
  for i in 1 2; do
    $MINI case_hotspot.py > /tmp/rc_run.txt 2>&1 &
    sleep 3
    T=$(wait_pid "^$MINI case_hotspot")
    if [ -n "$T" ]; then
      sudo env "PATH=$PATH" py-spy record -r $rate --format raw -o /tmp/rc_ps.txt \
        --pid "$T" -d 12 > /dev/null 2>&1
      wait "$T" 2>/dev/null
      grep -oP 'elapsed=\K[0-9.]+' /tmp/rc_run.txt | sed "s/^/pyspy$rate-$i /" >> "$OUT"
    else
      echo "pyspy$rate-$i FAIL" >> "$OUT"
    fi
  done
  # llm-prof 2 轮
  for i in 1 2; do
    $MINI case_hotspot.py > /tmp/rc_run.txt 2>&1 &
    sleep 3
    T=$(wait_pid "^$MINI case_hotspot")
    if [ -n "$T" ]; then
      sudo ./llm-prof/llm-prof -pid "$T" -d 15s -samples-per-second $rate \
        -off-cpu-threshold 0 -o /tmp/rc_lp.svg > /tmp/rc_lp.log 2>&1
      wait "$T" 2>/dev/null
      grep -oP 'elapsed=\K[0-9.]+' /tmp/rc_run.txt | sed "s/^/llmprof$rate-$i /" >> "$OUT"
    else
      echo "llmprof$rate-$i FAIL" >> "$OUT"
    fi
  done
  echo "rate $rate done" >> "$OUT"
done
echo DONE >> "$OUT"
echo ALL_DONE

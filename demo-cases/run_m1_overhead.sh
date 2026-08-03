#!/usr/bin/env bash
# M1 开销曲线实验：CPU 开销（目标进程 elapsed）与内存带宽（ringbuf 字节率）
# 采样率 {100,1000,10000} × 压缩 {off,on}，各 3 轮；基线 3 轮。
# 输出：/tmp/m1ov.txt（每行: <rate> <mode> <elapsed> <bytes>）
set -u
LLM=/home/ubuntu/ai-infra/llm-prof/llm-prof
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
OUT=/tmp/m1ov.txt
rm -f "$OUT"

# 基线（无采样）3 轮
for i in 1 2 3; do
  cd /home/ubuntu/ai-infra
  $MINI case_hotspot.py > /tmp/m1ov_run.txt 2>&1
  E=$(grep -oP 'elapsed=\K[0-9.]+' /tmp/m1ov_run.txt)
  echo "base base $E 0" >> "$OUT"
  echo "base$i: $E"
done

for RATE in 100 1000 10000; do
  for MODE in off on; do
    for i in 1 2 3; do
      cd /home/ubuntu/ai-infra
      $MINI case_hotspot.py > /tmp/m1ov_run.txt 2>&1 &
      sleep 3
      T=$(pgrep -f "^$MINI case_hotspot" | head -1)
      FLAG=""; [ "$MODE" = on ] && FLAG="-stack-compress"
      sudo $LLM -pid $T -d 12s -samples-per-second $RATE -off-cpu-threshold 0 -topn 0 $FLAG \
        -o /tmp/m1ov.svg > /tmp/m1ov.log 2>&1
      wait $T 2>/dev/null
      E=$(grep -oP 'elapsed=\K[0-9.]+' /tmp/m1ov_run.txt)
      B=$(grep -oP 'ringbuf bytes received: \K[0-9]+' /tmp/m1ov.log)
      [ -z "$B" ] && B=0
      echo "$RATE $MODE $E $B" >> "$OUT"
      echo "rate=$RATE mode=$MODE round=$i elapsed=$E bytes=$B"
      kill -9 $T 2>/dev/null
      sleep 1
    done
  done
done
echo ALLDONE

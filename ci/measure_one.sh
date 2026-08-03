#!/usr/bin/env bash
# 单配置开销测量（3 轮，负载自然完成，wall-time 中位）
# 用法: sudo bash ci/measure_one.sh <loader> <script> <rate> <mode> <outfile>
set -u
LOAD=/home/ubuntu/ai-infra
LLM=/home/ubuntu/ai-infra/llm-prof/llm-prof
loader=$1; script=$2; rate=$3; mode=$4; OUT=$5
times=""
for i in 1 2 3; do
  cd "$LOAD"
  "$loader" "$script" > /tmp/ov_run.txt 2>&1 &
  job=$!
  T=""
  for w in $(seq 1 40); do
    T=$(pgrep -f "^$loader $script" | head -1)
    [ -n "$T" ] && break
    sleep 1
  done
  if [ -z "$T" ]; then echo "FAIL:start $script"; exit 1; fi
  flag=""; [ "$mode" = on ] && flag="-stack-compress"
  t0=$SECONDS
  sudo "$LLM" -pid "$T" -d 12s -samples-per-second "$rate" -off-cpu-threshold 0 -topn 0 \
    $flag -o /tmp/ov.svg > /tmp/ov.log 2>&1
  wait "$job" 2>/dev/null   # 等负载自然完成（~30s）
  times="$times $((SECONDS - t0))"
  sleep 1
done
echo "$times" | tr ' ' '\n' | grep -v '^$' | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}' >> "$OUT"
echo "done $script rate=$rate mode=$mode -> $(tail -1 "$OUT")"

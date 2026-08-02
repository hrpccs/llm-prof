#!/usr/bin/env bash
# 栈深实验：分离 中断/过滤 vs unwind 成本
# 每档: 基线×2 | 中断+过滤基线(-pid 空闲进程)×2 | 完整 unwind(-pid 目标)×2
set -u
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
OUT=stack_exp.txt
RATE=10000
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

# 空闲 target（供中断基线）：长 sleep 进程
$MINI -c "import time; time.sleep(3600)" > /dev/null 2>&1 &
IDLE=$!
sleep 1

for d in 5 20 50 100 200; do
  case $d in
    5) IT=150000000;; 20) IT=150000000;; 50) IT=120000000;; 100) IT=130000000;; 200) IT=120000000;;
  esac
  echo "########## depth=$d ##########" >> "$OUT"
  # 基线 ×2
  for i in 1 2; do
    $MINI stack_bench.py $d $IT 2>/dev/null | grep -oP 'elapsed=\K[0-9.]+' | sed "s/^/d${d}-base$i /" >> "$OUT"
  done
  # 中断+过滤基线 ×2（-pid 指向空闲进程）
  for i in 1 2; do
    $MINI stack_bench.py $d $IT > /tmp/se_run.txt 2>&1 &
    sleep 3
    sudo ./llm-prof/llm-prof -pid $IDLE -d 12s -samples-per-second $RATE \
      -off-cpu-threshold 0 -o /tmp/se_int.svg > /dev/null 2>&1
    wait $! 2>/dev/null
    grep -oP 'elapsed=\K[0-9.]+' /tmp/se_run.txt | sed "s/^/d${d}-int$i /" >> "$OUT"
  done
  # 完整 unwind ×2（-pid 指向目标）
  for i in 1 2; do
    $MINI stack_bench.py $d $IT > /tmp/se_run.txt 2>&1 &
    sleep 3
    T=$(wait_pid "^$MINI stack_bench")
    if [ -n "$T" ]; then
      sudo ./llm-prof/llm-prof -pid $T -d 12s -samples-per-second $RATE \
        -off-cpu-threshold 0 -o /tmp/se_d${d}_$i.svg > /dev/null 2>&1
      wait "$T" 2>/dev/null
      grep -oP 'elapsed=\K[0-9.]+' /tmp/se_run.txt | sed "s/^/d${d}-unw$i /" >> "$OUT"
      sudo head -1 /tmp/se_d${d}_$i.txt 2>/dev/null | sed "s/^/d${d}-samples$i /" >> "$OUT"
    fi
  done
done
kill -9 $IDLE 2>/dev/null
echo DONE >> "$OUT"
echo ALL_DONE

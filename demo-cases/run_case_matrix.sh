#!/usr/bin/env bash
# 对比矩阵：4 个 case × (py-spy 100/1000Hz + llm-prof 100/1000Hz × off-cpu 开/关)
# 每轮记录 case 自报 elapsed（固定工作量，等进程自然结束再读），输出留待 analyze_matrix.py
set -u
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
PS=py-spy
OUT=mx_results.txt
: > "$OUT"

CASES="hotspot gil io misleading"
declare -A ARGS=( [hotspot]="case_hotspot.py" [gil]="case_gil.py 4" [io]="case_io.py" [misleading]="case_misleading.py" )

wait_pid() { # $1=pgrep pattern -> echo real pid
  local p="" i=0
  while [ $i -lt 40 ]; do
    sleep 1; i=$((i+1))
    p=$(pgrep -f "$1" | head -1)
    [ -n "$p" ] && break
  done
  echo "$p"
}

for case in $CASES; do
  echo "########## case=$case ##########" >> "$OUT"
  for i in 1 2; do
    $MINI ${ARGS[$case]} 2>/dev/null | grep -oP 'elapsed=\K[0-9.]+' | sed "s/^/$case baseline$i /" >> "$OUT"
  done

  for rate in 100 1000; do
    $MINI ${ARGS[$case]} > /tmp/mx_run.txt 2>&1 &
    sleep 3
    T=$(wait_pid "^$MINI ${ARGS[$case]%% *}")
    echo "[$case py-spy ${rate}Hz] T=$T"
    if [ -n "$T" ]; then
      $PS record -r $rate --format raw -o /tmp/mx_${case}_ps${rate}.txt --pid "$T" -d 12 > /dev/null 2>&1
      wait "$T" 2>/dev/null   # 等 case 自然结束（打印 elapsed）
      grep -oP 'elapsed=\K[0-9.]+' /tmp/mx_run.txt | sed "s/^/$case pyspy$rate /" >> "$OUT"
    else
      echo "$case pyspy$rate FAIL" >> "$OUT"
    fi
    sleep 1
  done

  for rate in 100 1000; do
    for off in 0 1.0; do
      $MINI ${ARGS[$case]} > /tmp/mx_run.txt 2>&1 &
      sleep 3
      T=$(wait_pid "^$MINI ${ARGS[$case]%% *}")
      echo "[$case llm-prof ${rate}Hz off=$off] T=$T"
      if [ -n "$T" ]; then
        sudo ./llm-prof/llm-prof -pid "$T" -d 15s -samples-per-second $rate \
          -off-cpu-threshold $off -topn 0 -o /tmp/mx_${case}_lp${rate}_off${off}.svg \
          > /tmp/mx_${case}_lp${rate}_off${off}.log 2>&1
        wait "$T" 2>/dev/null
        grep -oP 'elapsed=\K[0-9.]+' /tmp/mx_run.txt | sed "s/^/$case lp${rate}off$off /" >> "$OUT"
      else
        echo "$case lp${rate}off$off FAIL" >> "$OUT"
      fi
      sleep 1
    done
  done
done
echo DONE >> "$OUT"
echo ALL_DONE

#!/usr/bin/env bash
# 补跑 py-spy attach（需要 sudo，ptrace_scope=1）
set -u
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
OUT=mx_results.txt

CASES="hotspot gil io misleading"
declare -A ARGS=( [hotspot]="case_hotspot.py" [gil]="case_gil.py 4" [io]="case_io.py" [misleading]="case_misleading.py" )

wait_pid() {
  local p="" i=0
  while [ $i -lt 40 ]; do
    sleep 1; i=$((i+1))
    p=$(pgrep -f "$1" | head -1)
    [ -n "$p" ] && break
  done
  echo "$p"
}

# 清掉旧的 FAIL/pyspy 行，重新记录
grep -vE "pyspy" "$OUT" > /tmp/mx_tmp.txt && mv /tmp/mx_tmp.txt "$OUT"

for case in $CASES; do
  for rate in 100 1000; do
    $MINI ${ARGS[$case]} > /tmp/mx_run.txt 2>&1 &
    sleep 3
    T=$(wait_pid "^$MINI ${ARGS[$case]%% *}")
    echo "[$case py-spy ${rate}Hz] T=$T"
    if [ -n "$T" ]; then
      sudo env "PATH=$PATH" py-spy record -r $rate --format raw -o /tmp/mx_${case}_ps${rate}.txt --pid "$T" -d 12 > /tmp/mx_${case}_ps${rate}.log 2>&1
      wait "$T" 2>/dev/null   # 等 case 自然结束（打印 elapsed）
      grep -oP 'elapsed=\K[0-9.]+' /tmp/mx_run.txt | sed "s/^/$case pyspy$rate /" >> "$OUT"
    else
      echo "$case pyspy$rate FAIL" >> "$OUT"
    fi
    kill -9 $T 2>/dev/null
    sleep 1
  done
done
echo DONE >> "$OUT"
echo ALL_DONE

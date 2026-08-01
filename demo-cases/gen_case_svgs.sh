#!/usr/bin/env bash
# 生成 4 case 的 py-spy 火焰图（attach，12s @100Hz）
set -u
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3

gen() { # $1=case $2=args
  $MINI $2 > /tmp/svg_run.txt 2>&1 &
  sleep 3
  T=$(pgrep -f "^$MINI ${2%% *}" | head -1)
  echo "[$1] T=$T"
  [ -n "$T" ] && sudo env "PATH=$PATH" py-spy record -r 100 -o /tmp/svg_$1_pyspy.svg --pid "$T" -d 12 > /dev/null 2>&1
  kill -9 $T 2>/dev/null
  sleep 1
}

gen hotspot case_hotspot.py
gen gil "case_gil.py 4"
gen io case_io.py
gen misleading case_misleading.py
ls -la /tmp/svg_*_pyspy.svg 2>/dev/null | awk '{print $5, $9}'
echo DONE

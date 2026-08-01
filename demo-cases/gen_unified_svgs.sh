#!/usr/bin/env bash
# 统一 SVG 生成：所有 demo 先输出 pprof，再用 go tool pprof -svg 统一渲染，
# 替换 docs/ 下的对比图（py-spy 与 llm-prof 渲染风格完全一致）。
# 用法: sudo bash demo-cases/gen_unified_svgs.sh
set -u
# sudo bash 环境下 PATH 不含 go/py-spy，显式补齐
export PATH="/home/ubuntu/.vmr/versions/go_versions/go/bin:/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin:$PATH"
# train_like.py 的 checkpoint 写 /tmp/ckpt_*.json，历史运行可能留下异主文件导致崩溃
rm -f /tmp/ckpt_*.json /tmp/torch_ckpt_*.txt
cd /home/ubuntu/ai-infra
MINI=/home/ubuntu/.vmr/versions/miniconda_versions/miniconda/bin/python3
LLM=./llm-prof/llm-prof
P2P=/tmp/pyraw2pprof
DOCS=llm-prof/docs
IGNORE='asm_sysvec|sysvec_|irqentry|entry_SYSCALL|do_syscall_64|x64_sys_call|finish_task_switch|schedule|__schedule'

wait_pid() { # $1=pgrep pattern
  local p="" i=0
  while [ $i -lt 40 ]; do
    sleep 1; i=$((i+1))
    p=$(pgrep -f "$1" | head -1)
    [ -n "$p" ] && break
  done
  echo "$p"
}

run_one() { # $1=name  $2=args... （输出 docs/${1}_{tool}.svg）
  local name=$1; shift
  local T
  echo "===== $name ====="

  # --- llm-prof: pprof ---
  $MINI "$@" > /tmp/uni_run.txt 2>&1 &
  sleep 3
  T=$(wait_pid "^$MINI ${1}")
  if [ -n "$T" ]; then
    sudo "$LLM" -pid "$T" -d 15s -samples-per-second 100 -off-cpu-threshold 1.0 \
      -o /tmp/uni_${name}_lp.pb.gz > /tmp/uni_${name}_lp.log 2>&1
    if grep -q "Wrote" /tmp/uni_${name}_lp.log; then
      go tool pprof -ignore="$IGNORE" -svg /tmp/uni_${name}_lp.pb.gz > "$DOCS/${name}_llmprof.svg" 2>/dev/null
      echo "  llm-prof: ok ($(wc -c < $DOCS/${name}_llmprof.svg) B)"
    else
      echo "  llm-prof: FAIL $(grep ERROR /tmp/uni_${name}_lp.log | head -1)"
    fi
  fi
  kill -9 "$T" 2>/dev/null
  sleep 1

  # --- py-spy: raw -> pprof ---
  $MINI "$@" > /tmp/uni_run2.txt 2>&1 &
  sleep 3
  T=$(wait_pid "^$MINI ${1}")
  if [ -n "$T" ]; then
    sudo env "PATH=$PATH" py-spy record -r 100 --format raw -o /tmp/uni_${name}_ps.txt \
      --pid "$T" -d 12 > /tmp/uni_${name}_ps.log 2>&1
    if [ -s /tmp/uni_${name}_ps.txt ]; then
      "$P2P" /tmp/uni_${name}_ps.txt /tmp/uni_${name}_ps.pb.gz > /dev/null
      go tool pprof -ignore="$IGNORE" -svg /tmp/uni_${name}_ps.pb.gz > "$DOCS/${name}_pyspy.svg" 2>/dev/null
      echo "  py-spy:   ok ($(wc -c < $DOCS/${name}_pyspy.svg) B)"
    else
      echo "  py-spy:   FAIL"
    fi
  fi
  kill -9 "$T" 2>/dev/null
  sleep 1
}

run_one case_hotspot case_hotspot.py
run_one case_gil case_gil.py 4
run_one case_io case_io.py
run_one case_misleading case_misleading.py
run_one flame_demo train_like.py
run_one flame_async infer_asyncio.py 30
run_one flame_io bench.py io 50000
echo ALL_DONE

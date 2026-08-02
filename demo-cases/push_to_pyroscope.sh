#!/usr/bin/env bash
# 把 llm-prof / py-spy 的 pprof 上传到 Pyroscope（Grafana 的 profiling 后端），
# 在 http://localhost:4040 直接查看火焰图（或用 Grafana 的 Pyroscope 数据源）。
#
# 前置：docker run -d --name pyroscope -p 4040:4040 grafana/pyroscope:latest
# 注意：Pyroscope 默认只接受时间戳新鲜的 profile（摄取窗口约 1 小时），
#       长时间前采样的 pprof 会被 400 拒绝——需重新采样或调大摄取窗口。
#
# 用法: push_to_pyroscope.sh <profile.pb.gz> [service_name]
set -u
URL=${PYROSCOPE_URL:-http://localhost:4040}
PROFILE=${1:?usage: push_to_pyroscope.sh <profile.pb.gz> [service_name]}
NAME=${2:-llm-prof}
UUID=$(cat /proc/sys/kernel/random/uuid)

curl -s -o /dev/null -w "push %{http_code} ($NAME)\n" -H 'Content-Type: application/json' \
  -d "{\"series\":[{\"labels\":[{\"name\":\"__name__\",\"value\":\"process_cpu\"},{\"name\":\"service_name\",\"value\":\"$NAME\"}],\"samples\":[{\"ID\":\"$UUID\",\"rawProfile\":\"$(base64 -w0 "$PROFILE")\"}]}]}" \
  "$URL/push.v1.PusherService/Push"

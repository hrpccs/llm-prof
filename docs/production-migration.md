# 生产环境用 llm-prof 替代 py-spy 的迁移指南

> 依据：4 个构造 case 的定位/开销矩阵、开销-采样率曲线（10Hz-10000Hz）、
> 栈深分解实验、专家评审与后续修复（demo-cases/ 与 git 历史可复现）。
> 结论先行：**有条件替代**——Linux + root/CAP_BPF + 单进程 + 分钟级采样场景可以
> 切换（且等待型负载显著优于 py-spy）；非 Linux、无 root、需要实时看栈、多进程
> 批量、短命进程场景必须保留 py-spy。

## 1. 能力边界（实测结论）

### 可以替代（且有优势）

| 场景 | llm-prof | py-spy | 证据 |
|---|---|---|---|
| CPU 热点定位（单进程） | 命中 89-92% | 命中 89-91% | demo-cases 矩阵 |
| 1000Hz 高频采样开销 | <1% | +26%~114%（且注入伪等待） | rate_curve |
| GIL/锁等待可见性（off-cpu 开） | 73-89% | 0%（结构性漏采） | case_gil/io |
| asyncio/IO 等待（off-cpu 开） | 可见（时长加权） | 丢失 76%+ | README asyncio demo |
| 输出格式 | pprof/svg/txt，可接 Pyroscope/Grafana/Parca | raw/自定义 | pprof 统一改造 |

### 不能替代（保留 py-spy）

1. **非 root / 非 Linux / 无 CAP_BPF 的容器**：llm-prof 必须 root + eBPF；
2. **`py-spy dump` 实时看栈**：llm-prof 只有事后聚合，无"当前任意线程栈"能力；
3. **短命进程**（生命周期 <10s）：eBPF 加载 ~2.5s + attach 同步窗口 ~100ms，
   进程太快会采不到；
4. **多进程批量**：llm-prof 一次 attach 一个 `-pid`（`-pid 0` 全系统是另一种语义）；
5. **跨平台**（macOS/Windows 开发机）。

### 已知边界（切到生产前必须知道）

- **Python 3.13+**：TLS 偏移提取失败时降级 `pthread_getspecific`（日志 ERROR 级告警，
  Python 帧可能不完整——miniconda 3.13 实测可用，其他发行版需验证）；
- **128 帧记录上限**：极深栈（>128 帧）会被截断（与 py-spy 同类）；
- **off-cpu kprobe**：Ubuntu 24.04 上 `finish_task_switch.isra.0.cold` 附加失败
  （notrace 限制）——on-CPU 采样不受影响，off-cpu 依赖主符号路径（实测等待可见性
  正常）；其他内核需验证；
- **JIT/匿名代码**：unwind 失败的样本显示为 `[unwind-error]`（默认可见）或裸 0x 帧。

## 2. 生产化改造清单（当前差距 → 补法）

| # | 差距 | 补法 |
|---|---|---|
| 1 | 无版本管理（本地 v0.0.0-dirty 构建） | CI 流水线：`make ebpf && go build -a -tags osusergo,netgo`，版本号注入，产物+sha256 存档；**每次改 eBPF 必须 `go build -a`**（README 构建章节） |
| 2 | 全 root 运行 | systemd unit：`User=root` + `AmbientCapabilities=CAP_BPF CAP_PERFMON CAP_SYS_ADMIN`（按需最小化）；`-o` 输出目录专用（如 `/var/lib/llm-prof/`） |
| 3 | 无自监控 | wrapper 脚本/服务检查：① 日志关键字（`ERROR`、`no samples collected`、TLS fallback）② 输出文件 mtime ③ 采样完成状态码 |
| 4 | attach 时序脆弱 | 用 `demo-cases/` 的 `wait_pid` 模式（`pgrep -f "^<python> <script>"` 锚定真实进程，避免 bash -c 包装误匹配）；`-d` 必须 ≥8s（attach 后留 ≥5s 给 PID 报告轮询） |
| 5 | 无告警 | 对接现有监控：采集 agent 日志关键字 + 输出文件新鲜度，超时/空输出告警 |

## 3. 部署与使用规范

### 采样命令（生产推荐配置）

```bash
# CPU 热点 / 常规
sudo llm-prof -pid "$PID" -d 60s -samples-per-second 100 \
  -off-cpu-threshold 0 -o /var/lib/llm-prof/$(date +%s).pb.gz

# 等待型负载（GIL/IO/asyncio）——开 off-cpu
sudo llm-prof -pid "$PID" -d 60s -samples-per-second 100 \
  -off-cpu-threshold 1.0 -o /var/lib/llm-prof/$(date +%s).pb.gz
```

- **采样率**：默认 20Hz 偏低（单进程有效样本率 ≈ 名义值 × CPU 占用率），建议 100-1000Hz；
  1000Hz 开销实测 <1%，可放心用；
- **`-d`**：从 agent 启动算起（eBPF 加载 ~2.5s），**`-d` ≥ 8s**，否则 PID 报告轮询
  （5s）可能落在窗口外导致 0 样本；
- **off-cpu**：等待型负载必开；纯 CPU 热点可关（省 ringbuf 压力）；
- **输出**：统一 `.pb.gz`（pprof），分析用 `go tool pprof` 或 Pyroscope
  （`demo-cases/push_to_pyroscope.sh`），与 py-spy 对比用 `cmd/pyraw2pprof` 转换。

### 可视化链路（已实测）

```
llm-prof -o out.pb.gz ──┬─> go tool pprof -top / -svg
                        └─> Pyroscope Push API ──> :4040 火焰图 / Grafana 面板
py-spy raw ──> pyraw2pprof ──> 同上（统一 pprof）
```

## 4. 灰度切换流程（建议 2-4 周）

1. **双跑对照（第 1 周）**：同一负载各跑一轮——py-spy 100Hz（raw）+
   llm-prof 100Hz（off-cpu 开），用 `analyze_compare.py` / `pyraw2pprof` 对齐；
   先校准样本量级（llm-prof 有效样本率 ≈ 名义值 × CPU 占用率）；
2. **小范围试点（第 2 周）**：挑 1-2 个**等待型**服务（llm-prof 优势场景）切换，
   每日对比定位结论与 py-spy 基线的一致性；建立 agent 自监控告警；
3. **扩大范围（第 3-4 周）**：CPU 热点服务切换；保留 py-spy 用于：实时 dump、
   多进程批量、短命进程、非 Linux 环境；
4. **回滚条件**：agent 故障率 >0.5%、空输出（no samples）占比 >1%、定位结论与
   py-spy 对照偏差（top5 栈重合率 <60%）。

## 5. 故障排查速查（实测踩过的坑）

| 现象 | 原因 | 处理 |
|---|---|---|
| `no samples collected` | `-d` 窗口 <5s 轮询周期 / 目标进程已退出 / `-pid` 不匹配 | 诊断信息已内置；`-d` ≥8s，`pgrep` 锚定进程 |
| attach 初期少量 `[unwind-error]` | 同步窗口（~100ms，4012） | 已修复：静默丢弃；若仍见其他 error code 属真实 unwind 失败 |
| 行为与源码不符（verifier 拒绝/指标不生效） | go build 缓存嵌入旧 eBPF | `go build -a`；`strings llm-prof \| grep <特征>` 验证 |
| `Failed to attach to finish_task_switch.isra.0.cold` | 内核 notrace 限制（Ubuntu 24.04） | WARN 级，on-CPU 不受影响；换内核需验证 off-cpu |
| Python 3.13+ `TLS offset extraction failed` | 3.13 直接 TLS 变量，汇编特征提取失败 | 降级 pthread_getspecific（miniconda 3.13 实测可用）；告警后对比 Python 帧完整性 |
| torch/静态链接 python 无 Python 帧 | JIT/匿名代码无 unwind info 或栈在 C++ 层 | 属真实分布；配合 `[unwind-error]` 与 native 帧解读 |

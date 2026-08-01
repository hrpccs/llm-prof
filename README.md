# llm-prof

面向 **LLM 训练/推理场景**的轻量级 eBPF profiler：**on-CPU + off-CPU 统一采样**，不暂停目标进程的任何线程。

基于 [opentelemetry-ebpf-profiler](https://github.com/open-telemetry/opentelemetry-ebpf-profiler) 裁剪精简（保留 eBPF native/Python unwinder 与 off-cpu 机制），on/off-CPU 统一采样思路参考 [Blocked Samples（OSDI'24）](https://github.com/s3yonsei/blocked_samples)。

```
sudo ./llm-prof/llm-prof -pid <PID> -d 10s -off-cpu-threshold 1.0 -o out.svg
```

## 特色

| 能力 | 说明 |
|---|---|
| **零暂停采样** | perf event 中断触发，eBPF 内实时解帧 + `process_vm_readv` 读内存——**从不冻结目标进程**（对比 py-spy 的 ptrace 暂停全部线程） |
| **on + off CPU 统一** | perf 采样 on-CPU 执行点 + `sched_switch` 跟踪阻塞时长（off-cpu 样本按时长加权）——IO/等待型负载（网络、数据加载、GIL 等待）不再盲区 |
| **混合栈** | Python（函数名/文件/行号）+ native C/C++（torch 算子层）+ kernel，一条火焰图 |
| **`--pid` 目标过滤** | eBPF 侧按 pid 过滤（on/off 都过滤）——ringbuffer 不被全系统事件淹没，agent CPU 占用低 |
| **时长加权口径** | off-cpu 按真实阻塞时长加权（等价采样间隔数），等待占比更接近真实时间分配 |
| **对比分析工具** | `analyze_compare.py`：与 py-spy 的归一化对比（帧集/路径/方向、total 自检、行号分布） |

## 与 py-spy 的实测对比

测试机：16 核 Ubuntu 24.04，Python 3.12，固定工作量基准（bench.py）。同批基线、各 2 轮取中位数。

### 开销（相对基线，100Hz / 1000Hz）

| 负载 | py-spy @100Hz | llm-prof @100Hz | py-spy @1000Hz | llm-prof @1000Hz |
|---|---|---|---|---|
| 单线程 CPU | +3.2% | **≈0** | +23.5% | **≈0** |
| 4 线程 GIL 争用 | +12.2% | **≈0**（off-cpu 开 ≈0） | **+111.5%** | **+9.8%**（off-cpu 开 +15.1%） |
| IO/sleep 型 | +0.8% | ≈0（off-cpu 开 +1.9%） | +6.4% | +0~2.7% |

### 采集能力（100Hz，off-cpu 开）

| 指标 | py-spy | llm-prof |
|---|---|---|
| 单线程行号分布（cpu_work line16） | 89.7% | **90.9%**（偏差 1.2pp） |
| 多线程 GIL 等待栈 `_wait_for_tstate_lock` | 1.1% | **1.1%（off-cpu 补齐）** |
| IO 等待点 `main:42` | ≈100% | **≈100%** |
| 等待型负载样本量 | 88 | **4238（约 48 倍）** |

### 同一负载的火焰图对比（IO/sleep 型，100Hz）

左侧 py-spy（采样点快照），右侧 llm-prof（on+off 统一，时长加权）——同样的等待点 `main (bench.py:42)`，llm-prof 的样本量约为 48 倍：

| py-spy | llm-prof |
|---|---|
| ![py-spy](docs/flame_io_pyspy.svg) | ![llm-prof](docs/flame_io_llmprof.svg) |

### 1000Hz 的观测者效应（重要发现）

1000Hz 下 **py-spy 的 GIL 等待样本从 1.1% 虚高到 16.7%**——每毫秒暂停冻结全部线程本身就在加剧 GIL 争用，火焰图里多出的"等待"很大部分是采样器自己造成的；llm-prof（无暂停）保持 0.8%，更接近真实。

### 已知差异（实事求是）

1. **行号分布口径**：llm-prof 的 off-cpu 用**时长加权**、py-spy 用**采样点快照**——多线程下行号分布有约 9pp 系统差异（时长加权更接近真实时间占比，但两工具数字不可直接混用）；
2. **启动期杂项**：py-spy 能采到进程启动/退出期的 import 等杂项栈，llm-prof 只采 on/off CPU 执行点（差异 <1%）；
3. **部署门槛**：llm-prof 需要 root + `CAP_BPF`（仅 Linux）；py-spy 普通用户即可、跨平台；
4. **采样率语义**：`-samples-per-second` 为每 CPU 周期，单进程实际样本率视负载而定（实测 100Hz 下单线程约 35%、IO 型约 21%），对比前先校准样本量级。

**结论**：在有 root 权限的 Linux 环境，llm-prof 可平替 py-spy——性能更优（多线程 100Hz -12pp、1000Hz -96pp）、等待覆盖对齐、样本量反超；1000Hz 高频场景 py-spy 因开销与观测者效应基本不可用，llm-prof 是唯一合理选择。

## 用法

```
# attach 运行中的进程，采样 10 秒（on+off CPU），输出火焰图
sudo ./llm-prof/llm-prof -pid <PID> -d 10s -off-cpu-threshold 1.0 -o out.svg

# 参数
#   -pid <PID>            目标进程（0 = 全部）
#   -d <duration>         采样时长（0 = 直到 SIGINT）
#   -samples-per-second N 采样率（每 CPU，默认 20）
#   -off-cpu-threshold P  off-cpu 采样概率 [0..1]，0 禁用（默认），1.0 全采
#   -topn N               文本输出栈数（0 = 全部）
#   -o <path>             火焰图 SVG 输出路径
```

输出：`out.svg`（火焰图）+ `out.txt`（top-N 栈统计）。

## 构建

```bash
# Rust 符号化库（symblib-capi）
cargo build --release -p symblib-capi

# eBPF 程序 + Go agent
make EBPF_FLAGS="CLANG_FORMAT=true" ebpf
go build -tags osusergo,netgo -o llm-prof .
```

依赖：Linux 内核 ≥5.13（BTF）、clang、Go ≥1.25、Rust ≥1.88。

## Credits

- 核心 eBPF 程序与 Python unwinder 裁剪自 [opentelemetry-ebpf-profiler](https://github.com/open-telemetry/opentelemetry-ebpf-profiler)（Apache-2.0），保留了其 perf_event 采样、native unwind、Python 3.6-3.14 支持与 off-cpu 机制；
- on/off-CPU 统一采样思路参考 [Blocked Samples（OSDI'24，Minwoo Ahn et al.）](https://github.com/s3yonsei/blocked_samples)：把阻塞状态记录在切换时刻，让采样同时覆盖 on/off-CPU；
- 本仓库基于 Apache-2.0 许可（见 LICENSE）。

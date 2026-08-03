# eBPF 内核侧流式栈压缩（unwind 后栈 ID 化传输）——研究提案

> 状态：提案 / 状态：v1
> 关联代码：llm-prof（opentelemetry-ebpf-profiler 裁剪），内核参考 v6.8 `kernel/bpf/stackmap.c`
> 本文档的动机数据均来自本仓库实测（`demo-cases/` 可复现）。

## 1. 背景

### 1.1 持续 profiling 的数据规模问题

持续 profiling（Parca / Pyroscope / OTel Profiling）已成为可观测性三支柱之外的新支柱，
但其成本随采样率与进程规模**线性膨胀**：

- **传输带宽**：llm-prof 的 eBPF → 用户态走 `bpf_ringbuf`，ringbuf 大小按
  `rate × CPU 数 × 单样本大小` 分配（tracer.go:628）。实测：
  - 10000Hz × 16 核 × ~1.2KB/样本 ≈ **192MB** ringbuf 预算；
  - 高采样率是刚需（llm-prof @1000Hz 开销 <1%，py-spy 同频 +26%~114%），
    数据量随采样率线性涨，压缩是必要而非可选。
- **存储成本**：Parca/Pyroscope 对每个上传的 pprof 做通用压缩（gzip/zstd），
  但**传输链路**（内核 ringbuf → 用户态 → 网络）在压缩之前——通用压缩发生在
  数据已经"出内核"之后。

### 1.2 实测的数据特征（压缩的有利条件）

llm-prof 采集数据画像（case_hotspot.py，1000Hz，12s）：

| 特征 | 实测值 | 含义 |
|---|---|---|
| 样本/不同栈 | 5163 样本 / 33 distinct 栈 | **155:1 冗余**——相同栈反复出现 |
| 栈分布 | Zipf 型（top 栈占 39%） | 熵低，字典/熵编码收益大 |
| 前缀共享 | `_start → Py_BytesMain → ...` 是 95% 栈公共前缀 | 树/DAG 化指数级省 |
| 时间局部性 | 相邻窗口活跃栈高度重叠（待量化，见 §6.2） | 差分/增量编码收益大 |

### 1.3 问题陈述

> 在 eBPF 内核侧，对 **unwind 完成后的混合栈**（Python+native 帧序列）做**无损字典化
> 与时间差分**，使 ringbuf/网络带宽与采样率解耦（带宽 ∝ distinct 栈数而非采样数），
> 同时保持零/极低采样开销。

## 2. 相关工作

| 工作 | 机制 | 与本提案的差距 |
|---|---|---|
| **`bpf_get_stackid` / stack_map**（内核 `kernel/bpf/stackmap.c:210`） | 抓**原始地址栈** → jhash → 32 位 ID → 用户态按 ID 查回 | ① 原始地址栈（unwind **前**），不含混合栈语义；② **有损**：ID 是 hash 截断，`BPF_F_FAST_STACK_CMP` 只看 hash，冲突时覆盖（`BPF_F_REUSE_STACKID`）或拒绝（`-EEXIST`）；③ 每采样仍传 1 个 ID+计数，无时间差分 |
| **perf callchain 缓存**（内核） | per-cpu 缓存最近 callchain，减少抓栈成本 | 缓存的是"抓取结果"而非"传输编码"；不解决带宽 |
| **pprof protobuf**（google/pprof） | 字符串表 + packed varint（传输/存储层） | 发生在数据出内核之后；单 profile 内去重，无跨 profile/流式 |
| **HPCToolkit**（call-path profiling） | 离线构建调用树，按树节点计数 | 离线批处理，非流式；不解决实时传输 |
| **Parca / Pyroscope 存储** | 对象存储 + block 压缩（zstd） | 存储侧；传输链路（内核→agent→网络）未压缩 |
| **OTel 增量导出**（OTLP diff） | 相邻批次增量序列化 | 用户态协议层，未进内核；只做"值增量"不做"栈字典" |
| **持续 profiling 采样率自适应**（e.g. 内核 `perf_event_max_sample_rate` 节流） | 超限降采样 | 是"少采"不是"压缩"；丢失信息 |

**空白区**：没有工作针对 **unwind 后混合栈** 做内核侧**无损**流式字典压缩 + 时间差分。
`bpf_get_stackid` 证明"栈字典化"可行且已被工业界实践（perf 长期使用），但其有损、
原始栈、非流式的特性正是本提案的改进空间。

## 3. 创新性（claim）

1. **首个面向 unwind 后混合栈（Python+native）的 eBPF 内核侧无损栈字典压缩**：
   现有 `bpf_get_stackid` 只能压缩 unwind 前的原始地址栈，而混合栈（帧类型 + 函数名
   语义）才是可观测性工具实际传输的内容。
2. **无损设计**：128 位指纹（jhash2 双字）+ 冲突检测 + 全栈回退通道，
   对比 `bpf_get_stackid` 的 32 位有损 hash；正确性可验证（压缩/非压缩输出等价）。
3. **带宽与采样率解耦**：压缩后 ringbuf 写入量 ∝ **distinct 栈数变化率**而非采样率——
   对 Zipf 型负载（实测 155:1 冗余）预期带宽降 90%+，高采样率不再有带宽墙。
4. **双层编码**：空间字典（栈→ID）+ 时间差分（per-CPU 窗口计数增量）；
   两层的收益可分别量化（消融实验，§6.4）。
5. **可证明的压缩率下界**：栈序列的熵（§6.2 数据画像）给出压缩率的理论上限，
   与实测压缩率对照，验证"接近最优"。

## 4. 设计

### 4.1 架构总览

```
eBPF 内核侧                              用户态
┌──────────────────────────┐   ringbuf   ┌──────────────────────────┐
│ perf 采样 → unwind 完成   │────────────▶│ 解码：ID → 栈重建          │
│   → frame_data 序列       │  (ID, delta)│ 栈字典（LRU, ID→栈帧表）   │
│   → 指纹 hash（128 位）   │             │ 符号化（现有 native/python）│
│   → 栈字典查找            │◀────────────│ 字典 miss 通知 → 全栈样本   │
│     hit  → 只发 (ID,Δ)    │  (控制通道) │ 窗口 flush → 增量批量      │
│     miss → 发全栈 + 登记   │             └──────────────────────────┘
└──────────────────────────┘
```

### 4.2 栈指纹与字典

- **指纹**：对 unwind 完成的 `frame_data` 序列（每帧 = 类型 + 地址/偏移）做
  `jhash2`（内核同款，`stackmap.c:228`），取 128 位（两次不同 seed 的 jhash2 拼接），
  冲突概率 ~2^-128，可视为无冲突；与 `bpf_get_stackid` 的 32 位截断形成对比。
- **字典**：`BPF_MAP_TYPE_LRU_HASH`，key = 128 位指纹，value = 32 位 ID + 引用计数；
  容量上限（如 64K 条目，≈ 64K distinct 栈，覆盖 Zipf 尾部分布）。
- **淘汰**：LRU 淘汰条目 → 通过控制通道通知用户态删除对应 ID→栈表项；
  用户态持有完整 ID→栈表（LRU 同步）。

### 4.3 消息格式（ringbuf）

```
样本消息（两种）：
  FULL_STACK | trace_id | frame_data[]        # 字典 miss 或回退
  STACK_ID   | trace_id | stack_id | delta    # 字典 hit：32 位 ID + 计数增量
控制消息（控制通道）：
  EVICT | stack_id                            # 用户态删除 ID→栈项
  FLUSH | window_seq                          # 窗口结束，flush 计数
```

- `send_trace`（tracemgmt.h:567）扩展：字典 hit 走 `STACK_ID` 路径；
- 兼容现有路径：`-stack-compress` 开关，关闭时行为与现状完全一致。

### 4.4 时间差分（跨窗口）

- per-CPU 计数 map：`stack_id → u64 count`，窗口（如 5s，对齐 monitor interval）结束时
  flush 增量（delta = 本窗口计数 − 已上报计数）；
- 窗口间未变化的栈 delta=0 → 不上报；新出现/计数变化的栈才传输——
  持续 profiling 场景（长期运行）下传输量趋近"变化率"而非"采样率"。

### 4.5 eBPF 侧复杂度预算

- 当前 unwind_native 980 指令（verifier 6.8 通过）；
- 指纹计算（~10 帧 × 每帧 8B → jhash2 两次）≈ +200~300 指令；
- 单次字典 lookup（LRU_HASH）≈ 1 次 map 操作；
- 预期总指令数 ~1300，仍在 6.6+ verifier 预算内（需按内核版本降级验证）。

## 5. 实现

### 5.1 模块划分

```
support/ebpf/stack_dict.h        # 指纹计算、字典 lookup/insert、ID 分配
support/ebpf/tracemgmt.h         # send_trace 扩展（FULL_STACK / STACK_ID 分支）
support/ebpf/native_stack_trace.ebpf.c  # unwind 完成后调用 stack_dict 路径
tracer/tracer.go                 # ringbuf 消息解码、字典管理接入
processmanager/stackdict.go      # 用户态 ID→栈表（LRU）、符号化对接
internal/stackcompress/          # 指纹/ID 编解码、窗口差分逻辑（可单测）
```

### 5.2 实现步骤（里程碑）

1. **M1 空间字典**：eBPF 侧指纹 + 字典 + `STACK_ID` 消息；用户态重建栈 + 符号化；
   正确性验证（压缩/非压缩输出等价）。
2. **M2 时间差分**：per-CPU 窗口计数 + delta flush + `FLUSH` 消息；
   持续 profiling 模式（`-d 0` + 周期输出）下验证长期运行的带宽曲线。
3. **M3 淘汰与回退**：LRU 淘汰通知、冲突/异常回退全栈路径、指标埋点
   （压缩率、miss 率、evict 数——沿用 metrics 翻译表，注意其正确性已修复）。
4. **M4 工程化**：`-stack-compress` 开关、文档、火焰图/pprof 管线验证。

### 5.3 正确性保证

- 指纹 128 位 → 冲突概率可忽略；仍保留"冲突时全栈重传"回退；
- 压缩路径与非压缩路径**并行采样对照**（A/B 实验，§6.3）；
- 用户态重建栈与直接传输栈逐帧比对（hash 比对）。

## 6. 实验设计

### 6.1 指标

| 指标 | 测量方法 |
|---|---|
| **ringbuf 带宽**（B/s） | bpftool 统计 ringbuf 生产量 / 用户态接收字节数，多采样率（100/1000/10000Hz） |
| **压缩率** | ① distinct/总量（栈冗余度）；② 传输字节比（压缩前/后）；③ 与栈序列熵的比值 |
| **采样开销** | 目标进程 elapsed 增长（复用 rate_curve / stack_exp 方法论，10000Hz 下对比） |
| **用户态处理延迟** | `ProcessedUntil latency`（llm-prof 日志）——ID 传输应显著降低 |
| **正确性** | 压缩 vs 非压缩：栈集合等价（逐帧 hash）、计数一致（±0） |
| **字典行为** | miss 率、evict 率、LRU 命中率（metrics 埋点） |

### 6.2 前置：数据画像（论文实验基础）

对 llm-prof 已有负载量化三个压缩相关数字：

1. **栈熵 / distinct 比例**：`H(栈) = -Σ p log p`，压缩率下界 = 熵 × 采样数 / 原始字节；
2. **前缀共享度**：公共前缀树节点数 / 总帧数（树化收益）；
3. **时间局部性**：相邻窗口（5s）活跃栈集合的 Jaccard 相似度（差分收益上限）。

### 6.3 负载与对照组

| 负载 | 特征 | 预期收益 |
|---|---|---|
| case_hotspot（浅栈 Zipf） | 155:1 冗余 | 最高（~95% 带宽降） |
| stack_bench depth=200（深栈） | 长栈、distinct 多 | 中等（栈长但重复高） |
| case_gil（多线程） | 多线程同栈 | 高 |
| train_torch（混合栈/JIT） | JIT 帧高基数 | 中（JIT 帧地址随机 → 字典 miss 多，暴露边界） |
| 真实 vLLM（可选） | 生产特征 | 待测 |

对照组：① 现状（全栈传输）；② 全栈传输 + 用户态 zstd（存储侧压缩的等价物）；
③ `bpf_get_stackid` 风格（原始栈 + 32 位有损）——量化"无损混合栈"相对两者的增益。

### 6.4 消融

- A：仅空间字典（无差分）；B：仅时间差分（无字典）；C：组合；
- 各自对带宽/开销/正确性的贡献分离，验证双层设计的必要性。

### 6.5 理论对照

- 计算负载的栈熵，给出压缩率下界；实测压缩率与之对比（接近程度 = 编码效率）；
- 与 zstd 对"已传输 pprof"的压缩率对比（传输侧 vs 存储侧的收益定位）。

## 7. 预期结果与风险

### 7.1 预期结果

- Zipf 型负载（case_hotspot）：ringbuf 带宽降 **~90-95%**，开销增量 <1%
  （哈希 ~200ns/样本 vs 单样本传输 ~µs 级）；
- 深栈/高基数负载：带宽降 50-70%（distinct 栈仍多，字典收益有限但差分有效）；
- 用户态处理延迟显著下降（每样本从"传帧+符号化"变为"ID 查表"）。

### 7.2 风险与缓解

| 风险 | 缓解 |
|---|---|
| verifier 复杂度（指纹 + 字典指令） | 按内核版本降级（沿用 `python_frames_per_program` 模式）；`-stack-compress` 可关 |
| LRU 抖动（高基数负载频繁 evict） | 容量自适应（按 miss 率调）；evict 通知通道 |
| 指纹冲突 | 128 位 + 冲突全栈回退 |
| 多进程共享字典的隔离/泄漏 | 字典 key 含 pid 命名空间或按 pid 分段；LRU 容量按进程分 |
| 与现有 trace 批处理（maxEvents=4096）的交互 | M2 阶段验证窗口 flush 与批处理的配合 |

## 8. 交付物

- 代码（M1-M4，可开关 feature）；
- 数据画像报告（§6.2 三个数字）；
- 实验报告（带宽/开销/正确性/消融曲线）；
- 若数据支撑，按系统论文结构整理（背景-设计-评估），投稿方向：ATC/OSDI 类
  （系统 + 测量），或 EuroSys 的短论文/工业 track。

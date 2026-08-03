# 栈压缩提案专家审查报告

> 审查对象：`docs/stack-compression-proposal.md`
> 审查团队（4 位博士级研究员，独立并行审查）：
> - **A · 理论/信息论**：熵下界论证、指纹碰撞、无损性、差分编码信息论
> - **B · 系统落地**：eBPF verifier 预算、ringbuf 集成、LRU 一致性、工程风险
> - **C · 创新点/相关工作**：学术界与工业界文献调研、空白区论证、事实核查
> - **D · 实验设计**：指标口径、可复现性、统计严谨性、消融与对照公平性
> 日期：2026-08-03 ｜ 状态：待提案修订（v2）

---

## 0. 总体结论

**工程方向成立、动机数据真实（rate_curve.txt 等实测），但当前提案处于"可行性论证（工业报告级）"，不足以支撑论文级结论。** 修订后创新点应定位为：

> **面向 unwind 后混合栈的内核侧流式传输编码协议（字典 + 增量 + 回退），使持续 profiling 传输带宽 ∝ 栈变化率**

而非泛泛的"内核侧栈压缩"。空白区论证在加上**五重限定**（内核侧 + unwind 后混合栈 + 无损 + 流式 + 跨窗口差分）后成立，但必须修正 3 处事实性错误、1 处技术论证重构（见 §3）。

---

## 1. 致命问题（不解决则结论无法成立）

### F1. "128 位指纹（jhash2 双字拼接）→ 冲突概率 ~2^-128"是数学错误 [A]

- `jhash2` 返回值是 **u32**（内核 `stackmap.c:228` 可证）。两次不同 seed 拼接 = **2×32 = 64 位**，不是 128 位。要凑 128 位需 4 次 jhash2 或改用 siphash。
- 即便 64 位，碰撞概率应按**生日界对全部历史 distinct 栈配对**计算：P ≈ n²/2^65。n=10⁶ → 2.7×10⁻⁸；n=2³⁰（长期运行累积）→ ~3%。"可视为无冲突"只在有限窗口成立。
- 更本质：**"冲突检测 + 全栈回退"机制未定义**。参照 `stackmap.c:232-262`（`__bpf_get_stackid` 的 `hash_matches` → `bucket->nr`/`memcmp` 慢路径），真正的无损必须 = "指纹命中 + 完整帧序列比对，不等则回退 FULL_STACK"。任何指纹（哪怕 128 位）单独都只是概率保证。

### F2. LRU 淘汰 / ID 复用与 ringbuf 有损通道的一致性漏洞 [A]

ringbuf 是**有损通道**（满则丢、只计数不重传，`tracemgmt.h:588`）。由此：
1. **ID 复用静默错配**：LRU 淘汰后 ID 池复用，EVICT 消息丢失/延迟时，新栈样本被计入旧栈——错误不报错，逐帧 hash 也查不出；
2. **FULL_STACK 丢失**：miss 时全栈消息丢失 → 该 ID 后续样本永久无法解析；
3. **差分状态失步**：delta = 本窗口计数 − 已上报计数依赖双方状态一致。

**修复**：ID 单调递增不复用（32 位空间 + epoch 轮换 + ack）；FLUSH 携带窗口采样总数，用户态核对，不一致触发全量重同步（周期快照）。

### F3. A/B 正确性对照"计数一致（±0）"方法学上不可能 [A][D]

压缩/非压缩并行采样 → 两组样本**采样时刻不同**，逐样本比对无从谈起。正确做法（二选一）：
- **(i) 离线 replay**：非压缩路径的原始样本流回放给压缩解码器，验证重建逐帧一致（无损性的正确定义）；
- **(ii) 内核侧双写对照**：同一采样点同时走 FULL_STACK 与压缩路径（1-2s 短窗），用户态逐样本比对；
- (iii) 在线只做统计一致性（栈集合等价、KS 检验、计数相对误差阈值）。

### F4. 压缩率口径自证：155:1 是符号化口径，指纹是地址级口径 [D][A]

- §1.2 的"5163/33 distinct"来自 `reporter/local.go:134` 的**符号化后字符串栈**去重；而 §4.2 指纹是对**地址级 frame_data**（类型+地址/偏移）哈希。同一函数不同调用点/采样 PC → 不同地址级栈。
- **仓库内反例**：`demo-cases/stack_exp.txt` 中 `stack_bench d5`（浅栈，10000Hz）40315 样本有 **4359 distinct（仅 9:1）**，远非 155:1。155:1 是 case_hotspot 特例（单热点 PC），**不能外推为"浅栈 Zipf 普遍 155:1"**。
- "带宽 ∝ distinct 栈数"隐含**栈长恒定假设**，且未计入 FULL_STACK 注册 / EVICT / FLUSH 控制通道字节。

### F5. 相关工作两处事实错误、一处遗漏 [C]

1. **"OTel 增量导出（OTLP diff）"不存在**：实际是 OTLP profiles v1 的 `ProfilesDictionary`（stack_table + stack_index，协议层字典化）——应替换该行；
2. **"perf 长期使用 bpf_get_stackid"不精确**：perf 自身用自带 callchain 机制（`--call-graph`/`-z` zstd），`bpf_get_stackid` 是 BPF 程序的 helper；perf 的 BPF off-cpu 脚本才用 stack map；
3. **遗漏关键相关工作**：
   - **内核 `stack_depot`**（kernel stack trace 去重存储，kmemleak/warning 用）——未提；
   - **Parca 内核侧（agent 的 eBPF）**有 stack ID + count 聚合（先聚合后导出）——未提，且正是"流式 vs 聚合"张力所在；
   - **JEP 328 / JFR（JDK 11, 2018）**——用户态流式栈 ID 化的先例，必补；
   - Gorilla（VLDB 2015）时间差分先例、bpftime（OSDI'25）、UniProf（EuroSys 2024，标注未全文核验）——建议补。

---

## 2. 重要问题

### I1. 熵下界论证链断裂 [A][D]

- 公式"下界 = 熵×采样数/原始字节"**漏掉字典建立成本**：压缩后 ≈ Σ_distinct(全栈, 首次 FULL_STACK) + N×(ID+头)。浅栈 Zipf 场景摊销可忽略（33×1.2KB ≈ 40KB vs 6.2MB），但 **d200 实测 28,465–35,613 distinct × ~200 帧 ≈ 45–57MB 字典 vs 原始 ~58MB → 字典化零收益甚至负收益**。公式必须声明"字典摊销可忽略"假设。
- **定宽 32 位 ID 与熵存在结构性 gap**：33 符号 Zipf（p_top=0.39）H≈3-4 bit/样本，而定长 ID = 32 bit，冗余 ~8 倍。"实测压缩率 ≈ 熵下界 → 接近最优"不成立——**只有对 ID 流再做熵编码（Huffman/算术）才可能接近下界**，设计没有这一步。
- **Jaccard 不是差分收益的正确判据**：差分的理论下界是**转移熵 H(栈_t | 栈_{t-1})**；Jaccard 只测集合稳定（不含计数变化信息），且对低频长尾敏感。工程判据应为"每窗口变化条目数 × 条目字节 < 每窗口样本数 × 条目字节"。
- 熵估计有非平稳性/小样本问题（d200 场景 distinct≈样本量，MLE 熵严重低估）。

### I2. 前缀共享 claim 与设计脱节，"指数级"夸大 [A]

- 整栈指纹 + 整栈 ID 字典**完全不利用前缀共享**；稳态带宽单元是 32 位 ID，前缀只影响 miss 时的 FULL_STACK。
- 树化最多**线性**省；"指数级"只属于 DAG 化（不同前缀汇合公共后缀）且仅在存储/首次传输层面。
- 二选一：设计升级为**分层 ID**（前缀节点 ID + 新增后缀帧）让 claim 落地；或把前缀重新定位为"FULL_STACK 首次传输与字典存储的优化空间"，删"指数级"。

### I3. ringbuf 预算数字错误（差 10 倍）[A][D]

- `tracer.go:628` 用 `Sizeof_Trace = 0x62d8 ≈ 25.3KB`（`support/types.go:317`，含 frame_data[3072]×8B），不是 ~1.2KB：10000Hz×16 核 = 4.05GB → cap **2GB**（`min(NextPowerOfTwo, 1<<31)`）。
- ringbuf 是**静态预分配**，压缩不降 ringbuf 内存（除非自适应 resize）；压缩降的是**生产速率/丢事件率、用户态处理量、下游传输**。§1.3 表述需精确化。
- 区分两个口径：ringbuf 预算（25.3KB×rate×CPU）vs 每样本实际发送字节（`send_trace` 的 `send_size`，`tracemgmt.h:578`）。

### I4. 消融 A/B/C 隔离不干净 [D]

- "仅时间差分（无字典）"实现上不成立（差分必须标识栈，本质仍是字典）；"仅空间字典"也要定义消息语义。
- **改为离线编码模拟器消融**：同一 trace 流分别计算 帧流/ID 流/ID+delta 流 字节，报告 miss/evict/窗口变化率——干净、可复现、不需要三套 eBPF。模拟器也是 `internal/stackcompress/` 单测的天然载体。

### I5. 时间局部性 Jaccard 当前不可测 [D]

- `LocalReporter` 只输出聚合计数（`local.go:77-117`），svg/txt/pprof 均无时间戳；**仓库没有任何导出带 ktime 样本序列的路径**。需要 M0 新增"样本流导出"（`loadBpfTrace` 后按 ktime 写序列，`events.go:229` 处）。

### I6. 统计严谨性不足 [D]

- 现有方法论（`rate_curve.txt`：基线 3 轮、每配置 2 轮、中位数）下，"开销增量 <1%"与测量噪声同量级（基线极差 1.2%，10000Hz 两轮极差 0.8%）。
- 修订：每配置 ≥5 轮、A/B/A/B 交替轮序消除漂移、median+IQR、配对 bootstrap/Wilcoxon；可检测最小增量 ~0.3-0.5%；开销三段分解（复用 `run_stack_exp.sh` 的中断/过滤 / unwind / unwind+字典）单独量化 hash+lookup 成本。

### I7. 相关代码事实（供修订引用）[D]

- pprof 输出用 `gzip.BestSpeed`（`internal/pprofout/pprofout.go:248`），**不是 zstd**——zstd 对照组需明确压缩对象（原始 ringbuf 字节流 vs 未压缩 pprof protobuf）；
- `ProcessedUntil` latency 是 Debug 级日志（`processmanager/processinfo.go:900`），需改为 P50/P99 分布报告；
- `-pid` 仅单进程（`cli_flags.go:128`）——多进程并发实验需扩展；
- `maxEvents=4096` 批处理（`events.go:38`）与窗口 flush 的交互需验证。

---

## 3. 创新点重定位（修订后建议）

| 原 claim | 修订 |
|---|---|
| "128 位无损指纹" | **64 位指纹 + 命中后全栈比对 + 回退通道**（真正的无损判据链），按累积 distinct 数给配对碰撞概率表 |
| "首个 unwind 后混合栈内核侧压缩" | 五重限定后成立：**内核侧 + unwind 后混合栈 + 无损 + 流式 + 跨窗口差分**；补 JFR/JEP 328 先例后弱化为"首个针对混合栈的流式传输编码协议" |
| "带宽与采样率解耦" | 成立但需证据：补"采样率 vs distinct 饱和曲线"实验（P2-4）；表述精确为"降低 ringbuf 生产速率与丢事件率、解耦下游传输带宽" |
| "可证明压缩率下界 + 接近最优" | 拆两个基准：工程基准（字典+定长 ID+首次传输摊销模型）与理论基准（ID 流熵编码下界），指明对照哪个；差分用转移熵而非 Jaccard |

---

## 4. 修订路线图（M0 优先）

### M0 · 数据画像 + 离线模拟器（不写 eBPF，1-2 周，优先级最高）
1. **样本流导出**：`loadBpfTrace` 后按 ktime 写原始序列（含地址级 frame_data）；
2. **地址级画像**：采样数 / **地址级 distinct** / 总字节（per-CPU），三列对照表——**先回答"155:1 是否地址级成立"**（风险最大点：若地址级 distinct 显著高于符号化口径，§1.2 动机与 §7.1 预期需整体改写）；
3. **离线编码模拟器**（`internal/stackcompress/`）：帧流/ID 流/ID+delta 流字节分解 + 熵/转移熵（Miller-Madow 校正）+ trie 前缀共享 + 加权 Jaccard + 字典容量扫描（1K/4K/16K/64K）miss/evict 曲线；
4. 产出 `demo-cases/analyze_compress.py` 与 §6.2 三个数字的修订版。

### M1 · 实现 + 正确性协议
- 内核侧 per-CPU 字节计数器（压缩/非压缩路径分开，主带宽测量）；
- **双写对照**（F3 ii）或离线 replay（F3 i）作为无损验证协议；
- ID 单调递增 + FLUSH 校验 + 周期快照重同步（F2）。

### M2 · 统计协议与实验矩阵
- ≥5 轮 + A/B 交替 + 配对检验；三段开销分解；
- 负载补 `case_io`（等待主导，差分收益最大场景）与 `case_misleading`（差分压力测试）；vLLM 时间盒（2 周）或明确排除；
- 新增实验：压缩→pprof 大小对照（无损端到端证据）、多进程并发、≥10min LRU 稳定性（`train_like.py` 的 `plugin_reload` 近似栈漂移）、显式 Zipf 敏感性（s=1.0/1.5/2.0）、指纹冲突率实测、ASLR 重启敏感性、字典容量扫描。

---

## 5. 修改优先级汇总

| 优先级 | 事项 | 对应 |
|---|---|---|
| P0 | M0 地址级画像（先验证 155:1 口径） | F4, D-P0-1 |
| P0 | 无损验证协议（replay/双写） | F3, D-P0-3 |
| P0 | 指纹改为 64 位 + 比对 + 回退的完整判据链 | F1, A |
| P0 | 一致性协议（ID 不复用 + FLUSH 校验 + 重同步） | F2 |
| P0 | 相关工作事实修正（OTLP ProfilesDictionary、stack_depot、Parca、JFR） | F5 |
| P1 | 熵下界公式补摊销模型 + 双基准 + 转移熵 | I1, I4 |
| P1 | 消融改离线模拟器 | I4 |
| P1 | 统计协议收紧（≥5 轮、A/B 交替、CI） | I6 |
| P1 | ringbuf 预算数字修正 + 口径区分 | I3 |
| P2 | 前缀共享 claim 落地（分层 ID）或弱化 | I2 |
| P2 | 新增实验清单（§3 缺失实验） | D-§3 |

---

## 6. 附：已核实的代码/数据事实索引

| 事实 | 位置 |
|---|---|
| `Sizeof_Trace = 0x62d8 ≈ 25.3KB`（ringbuf 预算口径） | `support/types.go:317` |
| ringbuf = `SamplesPerSecond × NumCPU × Sizeof_Trace` | `tracer/tracer.go:628` |
| 每样本实际发送 `send_size` | `support/ebpf/tracemgmt.h:578` |
| ringbuf 丢事件只计数 | `support/ebpf/tracemgmt.h:588` |
| 用户态接收 `loadBpfTrace(data.RawSample)` | `tracer/events.go:229` |
| distinct 统计（符号化后字符串） | `reporter/local.go:134` |
| pprof gzip.BestSpeed | `internal/pprofout/pprofout.go:248` |
| `ProcessedUntil` Debug 日志 | `processmanager/processinfo.go:900` |
| `-pid` 单进程 | `cli_flags.go:128` |
| `bpf_get_stackid`：u32 jhash + memcmp 慢路径 | 内核 v6.8 `kernel/bpf/stackmap.c:228-262` |
| 浅栈地址级 distinct 反例（d5: 4359/40315 ≈ 9:1） | `demo-cases/stack_exp.txt` |

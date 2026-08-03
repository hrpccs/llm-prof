# 内核侧符号化/归一化可行性论证（压缩的前置问题）

> 问题：llm-prof 的符号化在用户态做，能否下沉到内核先符号化再压缩？
> 结论：**名字级符号化不能也不该进内核；但"压缩所需的稳定 key"（归一化）llm-prof
> 已经在 eBPF 侧完成了**——unwind 产物直接是 (fileID, offset) 帧序列。
> 真正可下沉的增量是"函数边界映射"（offset → 函数 ID），预计 distinct 栈数降 ~4 倍。

---

## 1. 现状事实：eBPF 帧编码已经是归一化的

源码证据（support/ebpf/）：

```c
// native 帧（native_stack_trace.h:14-25）
u64 *data = push_frame(state, trace, FRAME_MARKER_NATIVE, ra_flag, line, 1);
data[0] = file;                        // fileID（文件身份，unwind 时从 pid_information 取）

// python 帧（python_tracer.ebpf.c:32, 168-176）
u64 *data = push_frame(state, trace, FRAME_MARKER_PYTHON, FRAME_FLAG_PID_SPECIFIC, 0, 2);
// 2 个变量字 = (file_id, lineno)
```

- **native 帧 = (fileID, offset/line)**；**python 帧 = (fileID, lineno)**——ASLR 在 unwind
  时已被消除（fileID 是文件身份，offset 是文件内偏移）；
- 仅 **kernel 帧是绝对地址**（kallsyms 解析，但同内核版本内地址稳定）；
- JIT/匿名帧 = FRAME_MARKER_UNKNOWN（train_torch 实测占帧的 ~8%，无 fileID）。

**含义：帧本身就是压缩字典的稳定 key——不需要等用户态符号化，eBPF 侧可直接对
(fileID, offset) 帧序列做指纹与字典化。** 这修正了"先符号化才能压缩"的隐含前提。

---

## 2. 可行性分级：内核侧能做到哪一级

| 级别 | 内容 | 内核可行性 | 先例 / 成本 | llm-prof 现状 |
|---|---|---|---|---|
| L0 | 原始绝对地址 | ✓ | perf/bpf_get_stack 默认 | — |
| **L1** | **fileID + offset（单次运行内稳定）** | ✓✓ | unwind 产物本身就是 | **已实现**（native/python 帧） |
| L2 | build_id + offset（跨运行稳定） | ✓ 内核已有 | `bpf_get_stackid` 的 `BPF_F_BUILD_ID` 模式（stackmap.c:245-259） | 未实现（fileID 是运行内身份） |
| **L3** | **函数 ID（offset → 函数边界）** | △ 可行，需同步函数表 | 用户态 ELF 解析一次 → funcStart 表同步进 eBPF map → eBPF 内二分 | **未实现（这是可下沉的增量）** |
| L4 | 函数名 / 源码行号 | ✗ 不可行 | 符号表太大 + 内核无 ELF 解析 + 名字只用于展示 | 用户态 |

## 3. 为什么名字级符号化（L4）不能进内核

1. **map 容量**：符号表按"名字→地址"全量存储（libtorch .symtab 可达数十 MB），
   eBPF map（即使 memlock 放宽）按进程 × 文件存多份不可行；
2. **无 ELF 解析 helper**：内核 eBPF 没有读取用户进程 ELF/debug 段的机制
   （`bpf_get_stackid` 的 build_id 路径也只存 build_id 元数据，不解符号名）；
3. **展示需求**：名字只用于火焰图/pyroscope 展示——用户态对 **D 个 distinct 栈**
   符号化一次（O(D)）即可，成本可忽略；逐样本符号化（O(N)）才是浪费。
   → **符号化应后置到去重之后（pipeline-theory P2：重活后置）**，而不是前置下沉。

## 4. 真正可下沉的增量：函数边界映射（L3）——实测验证

**动机（初版预期）**：train_torch 1000Hz/12s，PC 粒度 distinct 栈 1582、符号化口径 366，
推测"PC 粒度是 distinct 的主要来源，函数级归一化可降 ~4 倍"。

**实测（demo-cases/l3_simulate.py，全量 topn 0 采样）——预期被证伪**：

| 口径 | distinct | 冗余比 | 说明 |
|---|---|---|---|
| PC 粒度（stream 帧值） | 1408 | 4.8:1 | 帧字面值（含未符号化帧差异） |
| 符号化行粒度（txt） | 996 | 6.8:1 | 同文件同行合并 |
| **函数级（L3 模拟）** | **961** | 7.1:1 | 帧规约到函数名 |

- **L3（offset→函数边界）单独收益仅 1.04x**（996→961）：train_torch 是紧凑循环，
  PC 已集中在少数源码行——函数级合并空间本来就小；
- 之前"1582→366 降 4.3x"的预期基于**错误对比**：1582 是 stream 帧字面值粒度、
  366 是默认 topn 50 截断值——两个口径均不可比，已废弃；
- **新的关键发现：未符号化帧（UNKNOWN，42% 帧）的地址高度稳定**（1498 帧仅 122 个
  不同地址，重复 10.7:1）——它们**可以正常进字典**，单次运行内不是压缩障碍
  （跨运行才需 build_id/vma 归一化）；
- **结论**：L3 不是 train_torch 场景的优化点；其价值仅在"PC 分散"负载（多调用点、
  深栈）中待验证——**压缩的主收益来源是栈字典+差分本身（7.1:1 起步），而非 L3**。

## 5. 推荐架构（压缩不依赖符号化）

```
eBPF 内核侧                                  用户态
unwind → (fileID, offset) 帧序列              按 distinct 栈符号化（O(D)）
  → [L3 可选] funcStart 二分 → (fileID, funcID)    ELF 解析 → 函数名/行号
  → 指纹（jhash 帧序列）                        ← 仅对字典 miss 的栈做
  → 栈字典（LRU）→ (ID, count)
  → ringbuf 传 ID（+ 首次 FULL_STACK）
```

- **压缩路径完全不经过符号化**——符号化是"展示层"的后处理，挂在字典 miss 的
  FULL_STACK 上；
- JIT/匿名帧（UNKNOWN ~8%）：无 fileID → 用 vma 偏移归一化（单次运行稳定，可入字典）；
  跨运行稳定需 L2（build_id）——JIT 匿名映射无 build_id，属开放问题；
- kernel 帧：绝对地址稳定，直接入字典。

## 6. 开放问题

1. **JIT/未符号化帧**：实测 UNKNOWN 帧地址稳定（1498 帧仅 122 distinct，单次运行内可入字典）；
   跨运行稳定性需 vma 相对偏移（运行内稳定）或 build_id（匿名映射无 build_id，候选
   "编译缓存签名"，超纲）；
2. **跨运行稳定性（L2）**：fileID 是运行内身份，跨运行（重启/升级）字典失效；
   加 build_id 需在 unwind 时解析 vma 的 build_id（内核已支持，成本待测）；
3. **L3 适用场景**：实测在 train_torch（紧凑循环，PC 集中）收益仅 1.04x；
   待验证场景是 PC 分散负载（多调用点/深栈）；需按场景可配；
4. **funcStart 表同步的时机与成本**：进程启动后首次 unwind 的延迟影响（若 L3 在
   其他负载验证有效再实现）。

## 7. 建议落地顺序

1. **M0 画像（已完成）**：PC 级 1408 / 符号化 996 / 函数级 961；L3 收益在 train_torch
   证伪；未符号化帧稳定（122 distinct）；
2. **M1 压缩实现**：直接用现有 (fileID, offset) 帧做指纹+字典（L1 已就绪）——
   压缩主收益来源（7.1:1 起步 + 时间差分）；
3. **M1.5（条件性）L3**：先在其他负载（多调用点/深栈）验证收益，再实现
   funcStart 表同步；
4. **M2 符号化后置**：用户态符号化从"每样本"移到"distinct 栈"（含 pprof 输出路径改造）。

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

## 4. 真正可下沉的增量：函数边界映射（L3）

**动机（实测数据）**：train_torch 1000Hz/12s，帧已是 (fileID, offset) 粒度，
distinct 栈 1582（4.7:1）；符号化口径（函数粒度）366（6.3:1）——**PC 粒度
（同函数不同指令位置 = 不同帧）是 distinct 的主要来源，比函数粒度高 4.3 倍**。

**机制**：
```
用户态（一次/文件）：ELF 解析 → 函数起始地址表（funcStart[]，排序）
  → 同步进 eBPF map（PERCPU_ARRAY 或按 fileID 分段的数组，~8B/函数）
eBPF（每帧）：offset 在 funcStart[] 二分（已有 big_stack_deltas 二分代码可复用）
  → 帧归一化为 (fileID, funcID)
```

**收益**：distinct 栈 1582 → 预期 ~400 量级（符号化口径 366 佐证）→ 字典容量需求、
FULL_STACK 首传成本、用户态符号化量同步下降；**压缩率与字典命中率双升**。

**成本**：
- map 空间：热点文件函数表（libtorch ~几万函数 × 8B ≈ 几百 KB/文件，可只同步采样
  命中的文件）；
- eBPF 指令：一次二分 ≈ +50 指令（复用现有 delta 二分模式）；
- 同步时机：文件首次被 unwind 时（已有 processmanager 的文件加载路径可挂）。

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

1. **JIT 帧归一化**：torch inductor 等 JIT 代码（匿名 mmap）无 build_id；
   候选：vma 相对偏移（运行内稳定）、或按"编译缓存签名"（超纲）；
2. **跨运行稳定性（L2）**：fileID 是运行内身份，跨运行（重启/升级）字典失效；
   加 build_id 需在 unwind 时解析 vma 的 build_id（内核已支持，成本待测）；
3. **函数粒度 vs PC 粒度**：L3 的二分把 distinct 降 4 倍，但丢失"调用点"信息
   （同函数不同调用位置的区分）——对诊断有意义，需按场景可配；
4. **funcStart 表同步的时机与成本**：进程启动后首次 unwind 的延迟影响。

## 7. 建议落地顺序

1. **M0 画像补充**：拆解 1582 个 distinct 栈中 PC 粒度占比（模拟 L3：把 offset 归一到
   函数起始——需用户态 ELF 符号表做离线近似）→ 验证 L3 收益预期；
2. **M1 压缩实现**：直接用现有 (fileID, offset) 帧做指纹+字典（L1 已就绪）；
3. **M1.5 L3**：funcStart 表同步 + eBPF 二分（收益已由 M0 离线模拟预估）；
4. **M2 符号化后置**：用户态符号化从"每样本"移到"distinct 栈"（含 pprof 输出路径改造）。

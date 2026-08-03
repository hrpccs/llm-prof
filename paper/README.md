# 论文：Kernel-Side Streaming Stack Compression for Continuous Profiling

> 状态：**初稿（v1）**，6 页，USENIX ATC 模板（usenix-2020-09.sty）
> 编译：`pdflatex paper.tex && pdflatex paper.tex`

## 定位（4 位博导/博士生评审共识）

以"**高采样率持续 profiling 的带宽墙**"为主线（压缩是使能技术，不是全部）。
核心主张：压缩后 ringbuf 带宽与采样率**解耦**（∝ distinct 栈变化率而非采样率）。

## 贡献（C1–C4，均有实测支撑）

- **C1** 内核侧对 **unwind 后混合栈**（Python+native）的无损流式压缩协议：
  增量 64 位指纹 + LRU 字典 + 40B StackIDEvent（保留 ktime/pid/tid 样本流）
- **C2** 正确性 by construction：**指纹即 key**（LRU 淘汰天然安全，无 ID 同步协议）；
  用户态缓存容量 = 内核字典，聚合路径与未压缩逐字节相同；C/Go 指纹一致性测试锁定
- **C3** 实测：6 负载 1kHz 降 53–75%，10kHz 降 94.6%；带宽曲线 56× → 6× 增长；
  样本/栈集合与未压缩一致；压缩 CPU 成本低于测量噪声（报告检出限）
- **C4** 可预测的压缩边界：字典摊销模型（D×S_full ≈ N×S_id 零收益拐点），
  d200 深栈 -2.5% 是**可预测的文档化边界**而非意外

## 已实现的诚实边界（Reviewer 攻击面应对）

| 事项 | 状态 |
|---|---|
| M2 时间差分 | **设计完成，未实现**——论文明确只 claim 空间字典，差分列为设计+未来工作 |
| 无损验证协议（replay/双写） | 未做——当前用样本数一致 + 事件自洽 + 指纹交叉验证 |
| bpf_get_stackid 定量对照 | 未做——Table 1 为定性对比 |
| 多进程 / ≥10min 长期运行 | 未做——列为未来工作 |
| L3 函数级归一化 | 实测 1.04× 收益 → **已证伪并拒绝**（论文中一句话交代） |

## 投稿路线建议（评审）

1. **主投**：系统论文（ATC/EuroSys 风格），叙事 = 带宽墙 → 洞察（unwind 帧已归一化
   + Zipf）→ 设计 → 评估（解耦主图）
2. **投稿前必补**（按优先级）：M2 差分实测（或主 claim 降级）→ replay 无损验证 →
   统计协议收紧（≥5 轮/A/B 交替/配对检验）→ bpf_get_stackid 对照 → 多进程/长运行
3. 备选拆分：压缩专篇（EuroSys 短论文/工业 track）+ 画像测量篇（ICPE 类）

## 文件

- `paper.tex` / `paper.pdf`：论文正文
- `usenix-2020-09.sty`、`usenix2019_v3.1.tex`：USENIX 官方模板
- `figures/`：bandwidth.png（带宽解耦主图）、cpu_overhead.png、profile.png（Zipf+收敛）、
  hs.txt / tt.txt（画像原始数据）

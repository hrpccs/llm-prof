# 回归测试（CI/CD）

每次改动（eBPF unwinder、符号化、采样路径、压缩）后，验证两件事没有回归：
**LLM 训练/推理场景的性能诊断能力** 与 **性能开销**。

## 快速开始

```bash
# 首次：生成基线（约 20 分钟，需 root）
sudo bash ci/run_regression.sh --baseline

# 每次改动后：跑回归（对比 ci/baseline.json，PASS/FAIL + 退出码）
sudo bash ci/run_regression.sh
```

## 覆盖范围（A–D）

| 阶段 | 内容 | 判定 |
|---|---|---|
| **A 构建** | eBPF 编译（make）+ go build + go test（含 C/Go 指纹一致性测试） | 必须全过 |
| **B 诊断能力** | train_torch（LLM 训练循环，1000Hz）压缩 off/on 各采样一轮：样本数、distinct 栈、train_torch.py 帧可见性 | 样本 ≥ 基线 90%；Python 帧必须可见；压缩 on ≈ off（±10%） |
| **C 性能开销** | case_hotspot 1000/10000Hz + train_torch 1000Hz，压缩 off/on，负载自然完成 wall-time 3 轮中位 | 1000Hz < +6%、10000Hz < +12%；压缩不比未压缩慢（+2s） |
| **D 压缩能力** | case_hotspot 10000Hz：压缩 on/off 的 ringbuf 字节与样本数 | 带宽降 ≥ 90%；样本 on ≈ off（±10%） |

## 已知基线（2026-08-03，`ci/baseline.json`）

- 基线 wall-time（无采样）：30s（case_hotspot 固定工作量）
- 诊断：train_torch 1000Hz ≈ 8575 样本 / 1165 distinct，Python 帧可见
- 开销：1000Hz 0%（<1s 粒度测不出）、10000Hz +3.3%
- 压缩：case_hotspot 10000Hz 带宽降 **93.6%**（47.7MB → 3.0MB/12s），样本 on=47200 vs off=47047（+0.3%）

## CI 平台

GitHub Actions workflow（`.github/workflows/regression.yml`）需要 **self-hosted runner**：
- root + eBPF（`bpf()` syscall）+ clang-17 + go + miniconda python3.13 + `/usr/bin/python3.12`（torch 训练负载）
- runner 用户免密 sudo
- 无 GPU 环境用 `train_torch.py`（CPU torch 训练循环）作为 LLM 训练代表；
  接入真实 vLLM 后可在 `ci/run_regression.sh` 的矩阵中追加

## 注意

- 阈值留了运行波动余量（±10%）；有意变更基线时显式重跑 `--baseline` 并提交新 `ci/baseline.json`
- 测量机需空闲（回归对系统负载敏感）；不要在 CI 机器上跑其他负载

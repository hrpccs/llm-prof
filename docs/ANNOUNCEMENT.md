# llm-prof: an eBPF on/off-CPU profiler that never pauses your process

**tl;dr** — We built a sampling profiler for LLM training/inference workloads that:
never freezes a single thread, samples both on-CPU and off-CPU time in one
flamegraph, and costs ~0% overhead where py-spy costs 12-111%. At 1000 Hz,
py-spy's own sampling pauses inflate its GIL-wait numbers 16x — llm-prof
doesn't have that problem, because it has no pauses.

## The problem with py-spy at scale

py-spy is great for quick lookups, but its mechanism — `PTRACE_INTERRUPT` on
every thread, every sample — has two consequences that get worse as your
workload does:

1. **Overhead scales with sampling rate.** Measured on a 16-core Ubuntu 24.04
   box (Python 3.12, fixed-workload microbenchmarks, same-batch baselines):

   | workload | py-spy @100Hz | llm-prof @100Hz | py-spy @1000Hz | llm-prof @1000Hz |
   |---|---|---|---|---|
   | single-thread CPU | +3.2% | **≈0** | +23.5% | **≈0** |
   | 4-thread GIL contention | +12.2% | **≈0** | **+111.5%** | **+9.8%** |
   | IO/sleep | +0.8% | ≈0 | +6.4% | +0~2.7% |

2. **The sampler distorts what it measures.** At 1000 Hz, py-spy freezes every
   thread every millisecond — that *is* GIL contention. Its flamegraph shows
   `_wait_for_tstate_lock` at **16.7%** of samples (1.1% at 100 Hz); llm-prof
   (no pauses) reports **0.8%**. Sixteen of those 16.7 points are py-spy's own
   artifact.

## What llm-prof does instead

- **perf-event interrupts + `process_vm_readv`** — samples the running thread
  in the interrupt context and reads the interpreter structures without ever
  suspending the process. GIL-heavy, multi-threaded, latency-sensitive services
  don't feel it.
- **on + off-CPU in one flamegraph** — a `sched_switch`-based off-CPU path
  (blocked time as weight, in the same units as on-CPU samples) covers IO
  waits, GIL waits, and data-loading stalls that on-CPU-only profilers miss.
  On our IO benchmark llm-prof collected **5021 samples vs py-spy's 71** — the
  same `main (bench.py:42)` wait site, just 70x more signal.
- **Mixed stacks** — Python frames (name/file/line) *plus* native (torch/CUDA
  layers) and kernel frames in one trace, which is exactly what you need to
  answer "is it my Python glue or my kernels?"
- **`--pid` filtering inside the kernel** — only the target process is
  sampled/collected, so the ringbuffer isn't flooded by the rest of the system.

## Honest caveats

- Off-CPU weights are duration-based; py-spy uses sampling-point snapshots.
  Distribution shapes match (single-thread: 90.9% vs 89.7% on the same line),
  but multi-thread line-level ratios can differ by a few points — ours is
  closer to true time share, but the two tools' numbers aren't drop-in
  interchangeable.
- Startup/exit noise (import-time stacks) that py-spy captures isn't sampled
  here (<1% of samples).
- Linux-only, needs root + `CAP_BPF`. py-spy runs unprivileged everywhere.

## Credits

- The eBPF programs and Python unwinder (3.6–3.14) are trimmed from
  [opentelemetry-ebpf-profiler](https://github.com/open-telemetry/opentelemetry-ebpf-profiler)
  (Apache-2.0).
- The on/off-CPU unified sampling idea follows
  [Blocked Samples](https://github.com/s3yonsei/blocked_samples)
  (Minwoo Ahn et al., OSDI'24): record the blocked state at switch time so one
  sampling stream covers both.

**Try it:** `sudo llm-prof -pid <PID> -d 10s -off-cpu-threshold 1.0 -o out.svg`
— flamegraph SVG + top-N text, zero config.

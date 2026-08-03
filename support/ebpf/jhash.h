// SPDX-License-Identifier: GPL-2.0
// Stack fingerprint helpers for stack compression (M1).
//
// The fingerprint is computed incrementally during unwinding: push_frame /
// push_kernel_frames fold every frame word into Trace.stack_fp via fp_step(),
// so no post-hoc walk over frame_data is needed (a post-hoc variable-length
// loop cannot be proven in-bounds by the verifier). Userspace computes the
// same fold sequence over the FULL trace's FrameData to match StackIDEvent
// messages back to their stacks.
//
// fp_step is an xxhash64-style single-step avalanche; the fold sequence is
// fully commutative-safe (order preserved) and must stay byte-for-byte
// identical to internal/stackcompress/jhash.go.

#ifndef OPTI_JHASH_H
#define OPTI_JHASH_H

// Initial value of Trace.stack_fp (set when a record is initialized).
#define STACK_FP_SEED 0x6c6c70666c6c7066ULL // "llfpllfp"

static inline EBPF_INLINE u64 fp_step(u64 h, u64 v)
{
  h ^= v * 0x9E3779B97F4A7C15ULL;
  h  = (h << 31) | (h >> 33);
  h *= 0xC2B2AE3D27D4EB4FULL;
  h ^= h >> 29;
  return h;
}

#endif // OPTI_JHASH_H

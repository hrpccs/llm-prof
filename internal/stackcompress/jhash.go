// Package stackcompress implements the userspace side of llm-prof stack
// compression (M1): the same incremental fingerprint that the eBPF side folds
// into Trace.stack_fp during unwinding (fp_step in support/ebpf/jhash.h),
// used to match compact StackIDEvent ringbuf messages back to their FULL
// stack samples.
//
// The implementation must stay byte-for-byte identical to
// support/ebpf/jhash.h (seed, step sequence, fold order: kernel frames first,
// then user frames in frame_data order).
package stackcompress

// StackFpSeed must match STACK_FP_SEED in support/ebpf/jhash.h.
const StackFpSeed = 0x6c6c70666c6c7066 // "llfpllfp"

// fpStep must match fp_step in support/ebpf/jhash.h.
func fpStep(h, v uint64) uint64 {
	h ^= v * 0x9E3779B97F4A7C15
	h = (h << 31) | (h >> 33)
	h *= 0xC2B2AE3D27D4EB4F
	h ^= h >> 29
	return h
}

// Fingerprint computes the stack fingerprint of a frame_data sequence
// (u64 frame words as produced by the eBPF unwinder), matching the
// incremental fold done in the kernel while unwinding. skipKernelFrames
// leading entries are raw kernel addresses and are skipped, mirroring the
// kernel side which does not fold them into Trace.stack_fp.
func Fingerprint(frameData []uint64, skipKernelFrames int) uint64 {
	h := uint64(StackFpSeed)
	for _, w := range frameData[skipKernelFrames:] {
		h = fpStep(h, w)
	}
	return h
}

// FingerprintBytes is like Fingerprint but takes the raw little-endian byte
// view of the frame words (used for ringbuf payloads).
func FingerprintBytes(b []byte) uint64 {
	h := uint64(StackFpSeed)
	for i := 0; i+8 <= len(b); i += 8 {
		h = fpStep(h, uint64(b[i])|uint64(b[i+1])<<8|uint64(b[i+2])<<16|
			uint64(b[i+3])<<24|uint64(b[i+4])<<32|uint64(b[i+5])<<40|
			uint64(b[i+6])<<48|uint64(b[i+7])<<56)
	}
	return h
}

// StackIDEvent layout (must match StackIDEvent in support/ebpf/types.h):
// u64 magic | u64 ktime | u64 fingerprint | u32 pid | u32 tid | u32 cpu_id | u32 count
const (
	StackIDEventMagic = 0x5349434b49444556 // "STACKIDEV"
	StackIDEventSize  = 40
)

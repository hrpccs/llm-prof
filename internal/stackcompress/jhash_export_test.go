package stackcompress

import "testing"

func TestFingerprintMatchesC(t *testing.T) {
	fd := []uint64{0x1122334455667788, 0xdeadbeefcafebabe, 0x0, 0x2}
	// Reference value produced by the C implementation
	// (support/ebpf/jhash.h fp_step sequence, compiled with clang).
	const want = uint64(0x7a8ed3d8648c1f15)
	got := Fingerprint(fd, 0)
	if got != want {
		t.Fatalf("fingerprint mismatch: got %016x want %016x", got, want)
	}
	// empty frame_data must not be 0 (seed-only finalization)
	if Fingerprint(nil, 0) == 0 {
		t.Fatal("empty fingerprint must not be 0")
	}
	// skipping kernel frames must match folding only the user frames
	want2 := Fingerprint(fd[2:], 0)
	if got2 := Fingerprint(fd, 2); got2 != want2 {
		t.Fatalf("kernel-skip mismatch: %016x vs %016x", got2, want2)
	}
	t.Logf("fingerprint = %016x", got)
}

package stackcompress

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// replayExpand replays an uncompressed sample stream through the compression
// protocol semantics: the first occurrence of a fingerprint is a FULL sample
// (registered in the cache), subsequent occurrences are STACK_ID events
// re-expanded from the cache. Correctness requires every re-expanded clone to
// be byte-identical to its original FULL sample.
func replayExpand(t *testing.T, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("replay data %s not present", path)
		return
	}
	defer f.Close()

	cache := make(map[uint64][]uint64)
	nFull, nHit, nLines := 0, 0, 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		nw, err1 := strconv.Atoi(parts[3])
		nkf, err2 := strconv.Atoi(parts[4])
		if err1 != nil || err2 != nil || nw < 0 || nkf < 0 || 5+nw > len(parts) {
			t.Fatalf("%s: malformed line: %s", path, line)
		}
		frames := make([]uint64, 0, nw)
		for _, v := range parts[5 : 5+nw] {
			w, err := strconv.ParseUint(v, 16, 64)
			if err != nil {
				t.Fatalf("%s: bad frame %q", path, v)
			}
			frames = append(frames, w)
		}
		fp := Fingerprint(frames, nkf)
		nLines++
		// The fingerprint covers only the user frames (kernel frames are raw
		// addresses excluded from the fold); a re-expanded clone therefore
		// must match the original in its user frames, while kernel frames
		// (sampling-time kernel position) may legitimately differ between
		// samples of the same user stack.
		user := frames[nkf:]
		if orig, ok := cache[fp]; !ok {
			// FULL sample: register (user frames).
			cache[fp] = append([]uint64(nil), user...)
			nFull++
		} else {
			// STACK_ID event: re-expanded user frames must equal the original.
			if len(orig) != len(user) {
				t.Fatalf("%s: replay mismatch (user len %d vs %d) fp=%x", path,
					len(orig), len(user), fp)
			}
			for i := range orig {
				if orig[i] != user[i] {
					t.Fatalf("%s: replay mismatch at user frame %d (%x vs %x) fp=%x",
						path, i, orig[i], user[i], fp)
				}
			}
			nHit++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("%s: scan error: %v", path, err)
	}
	t.Logf("replay %s: %d samples, FULL=%d, STACK_ID re-expanded=%d, "+
		"100%% frame-identical (hit rate %.1f%%)",
		path, nLines, nFull, nHit, float64(nHit)/float64(nLines)*100)
}

func TestReplayCaseHotspot(t *testing.T) {
	replayExpand(t, "../../demo-cases/replay_data/hs.txt")
}

func TestReplayTrainTorch(t *testing.T) {
	replayExpand(t, "../../demo-cases/replay_data/tt.txt")
}

func TestReplayStackBench(t *testing.T) {
	replayExpand(t, "../../demo-cases/replay_data/sb.txt")
}

// TestReplayFingerprintMatchesKernelFold checks that the incremental fold
// (simulated as a sequential pass, exactly what the kernel does at each frame
// write site) equals Fingerprint for a synthetic mixed stack.
func TestReplayFingerprintMatchesKernelFold(t *testing.T) {
	// Simulate kernel frames excluded + native (header, fileID) + python
	// (header, fileID, lineno) frames.
	frames := []uint64{
		0x30200000006707f2, 0x197857727ab34c, // native header + fileID
		0x1020000000000000, 0x3, 0x2a, // python header + fileID + lineno
	}
	fp := Fingerprint(frames, 0)
	h := uint64(StackFpSeed)
	for _, w := range frames {
		h = fpStep(h, w)
	}
	if fp != h {
		t.Fatalf("sequential fold mismatch: %016x vs %016x", fp, h)
	}
}

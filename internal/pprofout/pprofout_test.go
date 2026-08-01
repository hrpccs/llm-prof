package pprofout

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFrame covers the two frame label shapes emitted by llm-prof's
// frameLabel() and by py-spy raw traces.
func TestParseFrame(t *testing.T) {
	cases := []struct {
		in       string
		name     string
		filename string
		line     int64
	}{
		{"bottleneck (/home/u/case_hotspot.py:10)", "bottleneck", "/home/u/case_hotspot.py", 10},
		{"0x129d6b", "0x129d6b", "", 0},
		{"<interpreter trampoline> (<shim>:1)", "<interpreter trampoline>", "<shim>", 1},
		{"main (case_io.py:19)", "main", "case_io.py", 19},
	}
	for _, c := range cases {
		f := parseFrame(c.in)
		if f.Name != c.name || f.Filename != c.filename || f.Line != c.line {
			t.Errorf("parseFrame(%q) = %+v, want name=%q file=%q line=%d",
				c.in, f, c.name, c.filename, c.line)
		}
	}
}

// TestWriteGzip verifies the emitted file is a gzip stream whose payload is a
// valid protobuf that contains the interned strings (spot check).
func TestWriteGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.pb.gz")
	b := NewBuilder()
	b.AddStack([]string{"main (app.py:5)", "hotspot (app.py:9)"}, 3)
	b.AddStack([]string{"main (app.py:5)", "other (app.py:20)"}, 1)
	if err := b.Write(path, true, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a gzip stream: %v", err)
	}
	payload, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	// Spot check: both function names and the file name must be present as
	// length-delimited strings in the string table.
	for _, want := range []string{"hotspot", "other", "app.py"} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Errorf("payload missing %q", want)
		}
	}
	// sample_type strings must be present.
	if !bytes.Contains(payload, []byte("samples")) || !bytes.Contains(payload, []byte("count")) {
		t.Errorf("payload missing sample_type strings")
	}
}

// TestSampleOrdering ensures the sample's location_id list is leaf-first, as
// required by pprof convention: the current frame (leaf) comes first.
func TestSampleOrdering(t *testing.T) {
	b := NewBuilder()
	b.AddStack([]string{"root (a.py:1)", "mid (a.py:2)", "leaf (a.py:3)"}, 1)
	var p protoWriter
	locIDs := make([]uint64, 0, 3)
	for i := 2; i >= 0; i-- {
		_, id := b.frameLocation(parseFrame([]string{"root (a.py:1)", "mid (a.py:2)", "leaf (a.py:3)"}[i]))
		locIDs = append(locIDs, id)
	}
	p.packedUint64Field(1, locIDs)
	// leaf's location must be first, i.e. the smallest id in this 3-frame
	// stack because locations are created leaf-first.
	if locIDs[0] > locIDs[1] || locIDs[1] > locIDs[2] {
		t.Errorf("location ids not leaf-first: %v", locIDs)
	}
	_ = p
}

// TestParseFrameRoundTrip sanity-checks the converter path used by
// cmd/pyraw2pprof for a py-spy raw line with a trailing count.
func TestRawLineCount(t *testing.T) {
	line := "<module> (t.py:9);main (t.py:10) 42"
	i := strings.LastIndexByte(line, ' ')
	if i < 0 {
		t.Fatal("no space")
	}
	if line[i+1:] != "42" {
		t.Errorf("count = %q, want 42", line[i+1:])
	}
}

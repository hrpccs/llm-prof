// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package reporter

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
)

// frame builds a single-frame trace with the given label.
func frameTrace(label string) *libpf.Trace {
	f := libpf.Frame{FunctionName: libpf.Intern(label), Type: libpf.PythonFrame}
	var frames libpf.Frames
	frames.Append(&f)
	return &libpf.Trace{Frames: frames}
}

func TestReportTraceEventOffCPUWeight(t *testing.T) {
	rep := NewLocalReporter("", 0, 20, 1.0, false) // 20Hz, p=1.0

	// on-CPU sample: weight 1
	require.NoError(t, rep.ReportTraceEvent(frameTrace("on"), nil))
	// off-CPU sample: 5ms blocked at 20Hz -> 5e6 ns * 20 / 1e9 = 0.1 -> clamped to 1
	offMeta := &samples.TraceEventMeta{
		ProfileType: &samples.TypeMetadata{SampleType: "off_cpu"},
		Value:       5_000_000, // 5ms in ns
	}
	require.NoError(t, rep.ReportTraceEvent(frameTrace("off"), offMeta))
	// off-CPU sample: 100ms blocked at 20Hz -> 1e8 * 20 / 1e9 = 2
	offMeta2 := &samples.TraceEventMeta{
		ProfileType: &samples.TypeMetadata{SampleType: "off_cpu"},
		Value:       100_000_000, // 100ms in ns
	}
	require.NoError(t, rep.ReportTraceEvent(frameTrace("off"), offMeta2))

	rep.mu.Lock()
	defer rep.mu.Unlock()
	require.Equal(t, int64(1), rep.stacks["on"])
	require.Equal(t, int64(3), rep.stacks["off"]) // 1 (5ms clamped) + 2 (100ms)
}

func TestReportTraceEventOffCPUProbabilityCompensation(t *testing.T) {
	// p=0.5: a 100ms blocked sample at 20Hz is worth 2 / 0.5 = 4 intervals.
	rep := NewLocalReporter("", 0, 20, 0.5, false)
	offMeta := &samples.TraceEventMeta{
		ProfileType: &samples.TypeMetadata{SampleType: "off_cpu"},
		Value:       100_000_000,
	}
	require.NoError(t, rep.ReportTraceEvent(frameTrace("off"), offMeta))
	rep.mu.Lock()
	defer rep.mu.Unlock()
	require.Equal(t, int64(4), rep.stacks["off"])
}

func deepFrameTrace(n int) *libpf.Trace {
	var frames libpf.Frames
	for i := 0; i < n; i++ {
		f := libpf.Frame{FunctionName: libpf.Intern(fmt.Sprintf("frame%d (bench.py:%d)", i, i+1)),
			Type: libpf.PythonFrame}
		frames.Append(&f)
	}
	return &libpf.Trace{Frames: frames}
}

func TestWriteOutputInfernoStyleSVG(t *testing.T) {
	svgPath := t.TempDir() + "/out.svg"
	rep := NewLocalReporter(svgPath, 0, 20, 1.0, true)
	require.NoError(t, rep.ReportTraceEvent(frameTrace("cpu_work (bench.py:16)"), nil))
	// A deep stack (maxDepth-1 frames) exercises the footer overlap fix.
	require.NoError(t, rep.ReportTraceEvent(deepFrameTrace(19), nil))
	require.NoError(t, rep.WriteOutput())

	b, err := os.ReadFile(svgPath)
	require.NoError(t, err)
	out := string(b)
	require.Contains(t, out, `<!DOCTYPE svg PUBLIC`)
	require.Contains(t, out, `linearGradient id="background"`)
	require.Contains(t, out, `#frames > *:hover`)
	require.Contains(t, out, `<g id="frames">`)
	require.Contains(t, out, `<title>cpu_work (bench.py:16)</title>`)
	require.Contains(t, out, `viewBox="0 0 1200`)
	require.Contains(t, out, `</svg>`)

	// The deepest rendered frame (depth 19) must not overlap the footer:
	// the footer hint text must appear after the last frame row.
	footerIdx := strings.Index(out, "hover a frame for the full stack path")
	require.Greater(t, footerIdx, 0, "footer hint text should exist")
	lastFrameBottom := strings.LastIndex(out, `height="15" fill=`)
	require.Greater(t, footerIdx, lastFrameBottom, "footer must come after the deepest frame row")
}

func TestReportTraceEventZeroProbabilityDefaults(t *testing.T) {
	// p<=0 falls back to 1.0 (disabled off-cpu path must not divide by zero).
	rep := NewLocalReporter("", 0, 20, 0, false)
	require.Equal(t, 1.0, rep.offCPUProbability)
}

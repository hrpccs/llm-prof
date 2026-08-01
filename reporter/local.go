// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package reporter provides the local flamegraph reporter used by llm-prof.
// It replaces the OTLP reporter of the upstream project: traces are aggregated
// in-process and rendered to a flamegraph SVG plus a text top-N list.
package reporter

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"

	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
)

// LocalReporter aggregates symbolized stack traces in memory.
// It implements reporter.TraceReporter.
type LocalReporter struct {
	mu               sync.Mutex
	stacks           map[string]int64 // "root;mid;leaf" -> sample count
	output           string           // output SVG path
	topN             int              // text top-N stack count (0 = all)
	samplesPerSecond int              // on-CPU sampling rate, for off-cpu weight normalization
	offCPUProbability float64         // off-cpu sampling probability (1/p weight compensation)
	pythonOnly         bool            // keep only Python frames in output
}

// NewLocalReporter creates a reporter that writes a flamegraph to outputPath.
func NewLocalReporter(outputPath string, topN int, samplesPerSecond int,
	offCPUProbability float64, pythonOnly bool,
) *LocalReporter {
	if offCPUProbability <= 0 {
		offCPUProbability = 1.0
	}
	return &LocalReporter{
		stacks:            map[string]int64{},
		output:            outputPath,
		topN:              topN,
		samplesPerSecond:  samplesPerSecond,
		offCPUProbability: offCPUProbability,
		pythonOnly:        pythonOnly,
	}
}

// frameLabel renders one frame as "name (file:line)" or "0xADDR" for
// non-symbolized native frames.
// isPythonFrameLabel reports whether a frame label looks like a Python frame
// (symbolized name with a (file:line) suffix), as opposed to a native 0xADDR frame.
func isPythonFrameLabel(label string) bool {
	if len(label) < 3 {
		return false
	}
	if label[0] == '0' && label[1] == 'x' {
		return false
	}
	return label[len(label)-1] == ')'
}

func frameLabel(f libpf.Frame) string {
	name := f.FunctionName.String()
	if name == "" {
		name = fmt.Sprintf("0x%x", uint64(f.AddressOrLineno))
	}
	if file := f.SourceFile.String(); file != "" {
		name = fmt.Sprintf("%s (%s:%d)", name, file, int64(f.SourceLine))
	}
	return name
}

// Start is a no-op for the local reporter.
func (l *LocalReporter) Start(context.Context) error { return nil }

// Stop is a no-op for the local reporter.
func (l *LocalReporter) Stop() {}

// ReportTraceEvent aggregates the trace's frames into a stack key.
// Frames are rendered root -> leaf so the flamegraph reads top-down.
func (l *LocalReporter) ReportTraceEvent(trace *libpf.Trace, meta *samples.TraceEventMeta) error {
	frames := trace.Frames
	if len(frames) == 0 {
		return nil
	}
	parts := make([]string, 0, len(frames))
	for i := len(frames) - 1; i >= 0; i-- {
		label := frameLabel(frames[i].Value())
		if l.pythonOnly && !isPythonFrameLabel(label) {
			continue
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return nil
	}
	key := strings.Join(parts, ";")
	// Weight: on-CPU samples count 1; off-CPU samples carry their blocked
	// duration, normalized to "equivalent sampling intervals" so both share
	// the same unit in the flamegraph (duration_ns * rate / 1e9), and
	// compensated by 1/p for probabilistic off-cpu sampling.
	// Computed in float64 to avoid overflow (review fix).
	weight := int64(1)
	if meta != nil && meta.ProfileType != nil && meta.ProfileType.SampleType == "off_cpu" {
		w := float64(meta.Value) * float64(l.samplesPerSecond) / 1e9 / l.offCPUProbability
		if w < 1 {
			w = 1
		}
		if w > float64(math.MaxInt64) {
			w = float64(math.MaxInt64)
		}
		weight = int64(w)
	}
	l.mu.Lock()
	l.stacks[key] += weight
	l.mu.Unlock()
	return nil
}

// writeTopN dumps the N most frequent stacks to path.
func (l *LocalReporter) writeTopN(path string, n int) error {
	type kv struct {
		k string
		v int64
	}
	entries := make([]kv, 0, len(l.stacks))
	var total int64
	for k, v := range l.stacks {
		entries = append(entries, kv{k, v})
		total += v
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].v > entries[b].v })

	var b strings.Builder
	fmt.Fprintf(&b, "total samples: %d, distinct stacks: %d\n\n", total, len(entries))
	for i, e := range entries {
		if i >= n {
			break
		}
		fmt.Fprintf(&b, "%6d  %s\n", e.v, strings.ReplaceAll(e.k, ";", " <- "))
	}
	// security review fix: O_NOFOLLOW to avoid symlink overwrite (agent runs as
	// root) and 0600 to avoid leaking source paths/lines in shared dirs.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

// flameNode is a node in the prefix tree of stacks.
type flameNode struct {
	name     string
	count    int64
	children map[string]*flameNode
}

func newFlameNode(name string) *flameNode {
	return &flameNode{name: name, children: map[string]*flameNode{}}
}

// buildTree folds all stacks into a prefix tree.
func (l *LocalReporter) buildTree() *flameNode {
	root := newFlameNode("root")
	for key, count := range l.stacks {
		parts := strings.Split(key, ";")
		node := root
		for _, p := range parts {
			child, ok := node.children[p]
			if !ok {
				child = newFlameNode(p)
				node.children[p] = child
			}
			node = child
			node.count += count
		}
	}
	return root
}

// flameColor returns a stable color for a frame name.
func flameColor(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	palette := []string{
		"#8dd3c7", "#ffffb3", "#bebada", "#fb8072", "#80b1d3",
		"#fdb462", "#b3de69", "#fccde5", "#d9d9d9", "#bc80bd",
		"#ccebc5", "#ffed6f", "#1f78b4", "#33a02c", "#e31a1c",
	}
	return palette[h.Sum32()%uint32(len(palette))]
}

// writeSVG renders the flamegraph. width is the total sample count.
func writeSVG(path string, root *flameNode, total int64, fixedWidth bool) error {
	const rowH = 16
	const titleH = 24
	const maxDepth = 20 // enough for Python frames + native tail in mixed stacks
	// Scale the layout so narrow profiles still produce a readable flamegraph.
	// The viewBox width is at least 1000 units (fixed at 1200 for fixedWidth
	// output so side-by-side comparisons with py-spy render at the same size).
	const minWidth = 1000
	const fixedWidthPx = 1200
	scale := 1.0
	if fixedWidth {
		scale = float64(fixedWidthPx) / float64(total)
	} else if total < minWidth {
		scale = float64(minWidth) / float64(total)
	}
	viewW := int64(float64(total) * scale)
	// maxDepth rows of frames (depth 1..maxDepth-1 after the virtual root)
	// plus one spare row for the footer hint, so it never overlaps frames.
	viewH := titleH + rowH*maxDepth + rowH

	// DFS via a recursive closure; stack depth is bounded by maxDepth.
	children := func(n *flameNode) []*flameNode {
		cs := make([]*flameNode, 0, len(n.children))
		for _, c := range n.children {
			cs = append(cs, c)
		}
		sort.Slice(cs, func(a, b int) bool { return cs[a].count > cs[b].count })
		return cs
	}

	// Pre-order traversal collecting frames with their full stack path.
	type frame struct {
		name  string
		path  string // root -> this frame, for the hover <title>
		x     int64
		w     int64
		depth int
	}
	var frames []frame
	var dfs func(n *flameNode, x int64, depth int, path []string)
	dfs = func(n *flameNode, x int64, depth int, path []string) {
		if n.name != "root" {
			frames = append(frames, frame{n.name, strings.Join(path, ";"), x, n.count, depth})
		}
		var cx int64
		for _, c := range children(n) {
			dfs(c, x+cx, depth+1, append(path, c.name))
			cx += c.count
		}
	}
	dfs(root, 0, 0, nil)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" standalone="no"?>` + "\n")
	b.WriteString(`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n")
	fmt.Fprintf(&b, `<svg version="1.1" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg">`+"\n",
		viewW, viewH)
	b.WriteString(`<defs><linearGradient id="background" x1="0" y1="0" x2="0" y2="1">` + "\n")
	b.WriteString(`<stop stop-color="#eeeeee" offset="5%"/><stop stop-color="#eeeeb0" offset="95%"/>` + "\n")
	b.WriteString(`</linearGradient></defs>` + "\n")
	b.WriteString(`<style type="text/css">` + "\n")
	b.WriteString(`text { font-family:monospace; font-size:12px }` + "\n")
	b.WriteString(`#title { font-size:17px; font-weight:bold; }` + "\n")
	b.WriteString(`#frames > *:hover { stroke:black; stroke-width:0.5; cursor:pointer; }` + "\n")
	b.WriteString(`</style>` + "\n")
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="url(#background)"/>`+"\n", viewW, viewH)
	fmt.Fprintf(&b, `<text id="title" x="4" y="17">llm-prof flamegraph (%d samples)</text>`+"\n", total)

	b.WriteString(`<g id="frames">` + "\n")
	for _, f := range frames {
		y := titleH + int64(f.depth)*rowH
		if f.depth >= maxDepth {
			continue // depth limit for readability
		}
		x := int64(float64(f.x) * scale)
		w := int64(float64(f.w) * scale)
		if w < 1 {
			w = 1 // review fix: avoid zero-width rects with overflowing labels
		}
		fmt.Fprintf(&b, `<g x="%d" y="%d" width="%d" height="%d">`+"\n", x, y, w, rowH-1)
		fmt.Fprintf(&b, `<title>%s</title>`+"\n", htmlEscape(f.path))
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="%s" stroke="#000" stroke-width="0.5"/>`+"\n",
			x, y, w, rowH-1, flameColor(f.name))
		// Show the full label when it fits, otherwise a truncated prefix;
		// the <title> above always carries the complete stack path.
		label := f.name
		chars := int(w) / 7
		if chars >= 4 {
			if runes := []rune(label); len(runes) > chars {
				label = string(runes[:chars])
			}
			fmt.Fprintf(&b, `<text x="%d" y="%d">%s</text>`+"\n",
				x+2, y+rowH-5, htmlEscape(label))
		}
		b.WriteString(`</g>` + "\n")
	}
	b.WriteString(`</g>` + "\n")
	fmt.Fprintf(&b, `<text x="4" y="%d" font-size="11" fill="#999">llm-prof — hover a frame for the full stack path</text>`+"\n", viewH-6)
	b.WriteString(`</svg>` + "\n")

	// security review fix: O_NOFOLLOW to avoid symlink overwrite (agent runs as
	// root) and 0600 to avoid leaking source paths/lines in shared dirs.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// WriteOutput writes the flamegraph SVG and the text top-N list next to it.
func (l *LocalReporter) WriteOutput() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var total int64
	for _, v := range l.stacks {
		total += v
	}
	if total == 0 {
		return fmt.Errorf("no samples collected")
	}
	root := l.buildTree()
	if err := writeSVG(l.output, root, total, l.pythonOnly); err != nil {
		return err
	}
	topPath := strings.TrimSuffix(l.output, ".svg") + ".txt"
	n := l.topN
	if n <= 0 {
		n = len(l.stacks)
	}
	return l.writeTopN(topPath, n)
}

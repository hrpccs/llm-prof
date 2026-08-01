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
}

// NewLocalReporter creates a reporter that writes a flamegraph to outputPath.
func NewLocalReporter(outputPath string, topN int, samplesPerSecond int,
	offCPUProbability float64,
) *LocalReporter {
	if offCPUProbability <= 0 {
		offCPUProbability = 1.0
	}
	return &LocalReporter{
		stacks:           map[string]int64{},
		output:           outputPath,
		topN:             topN,
		samplesPerSecond: samplesPerSecond,
		offCPUProbability: offCPUProbability,
	}
}

// frameLabel renders one frame as "name (file:line)" or "0xADDR" for
// non-symbolized native frames.
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
		parts = append(parts, frameLabel(frames[i].Value()))
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
func writeSVG(path string, root *flameNode, total int64) error {
	const rowH = 16
	const titleH = 24
	// Scale the layout so narrow profiles still produce a readable flamegraph.
	// The viewBox width is at least 1000 units; x/width scale proportionally.
	const minWidth = 1000
	scale := 1.0
	if total < minWidth {
		scale = float64(minWidth) / float64(total)
	}
	viewW := int64(float64(total) * scale)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`font-family="monospace" font-size="12">`+"\n", viewW, titleH+rowH*8)
	fmt.Fprintf(&b, `<text x="4" y="16" font-size="14" font-weight="bold">llm-prof flamegraph (%d samples)</text>`+"\n", total)

	// Use an explicit DFS via a slice to avoid recursion limits on deep stacks.
	children := func(n *flameNode) []*flameNode {
		cs := make([]*flameNode, 0, len(n.children))
		for _, c := range n.children {
			cs = append(cs, c)
		}
		sort.Slice(cs, func(a, b int) bool { return cs[a].count > cs[b].count })
		return cs
	}

	// We do a manual pre-order traversal; root is the virtual root.
	type frame struct {
		name  string
		x     int64
		w     int64
		depth int
	}
	var frames []frame
	var dfs func(n *flameNode, x int64, depth int)
	dfs = func(n *flameNode, x int64, depth int) {
		if n.name != "root" {
			frames = append(frames, frame{n.name, x, n.count, depth})
		}
		var cx int64
		for _, c := range children(n) {
			dfs(c, x+cx, depth+1)
			cx += c.count
		}
	}
	dfs(root, 0, 0)

	for _, f := range frames {
		y := titleH + int64(f.depth)*rowH
		if f.depth >= 8 {
			continue // depth limit for readability
		}
		x := int64(float64(f.x) * scale)
		w := int64(float64(f.w) * scale)
		if w < 1 {
			w = 1 // review fix: avoid zero-width rects with overflowing labels
		}
		fmt.Fprintf(&b,
			`<rect x="%d" y="%d" width="%d" height="%d" fill="%s" stroke="#000" stroke-width="0.5"/>`+"\n",
			x, y, w, rowH-1, flameColor(f.name))
		// Truncate label to the rect width; skip text entirely for tiny rects.
		label := f.name
		chars := int(w) / 7
		if chars < 4 {
			continue // review fix: too narrow to render a readable label
		}
		// Truncate by runes, not bytes, to keep valid UTF-8 in the SVG.
		if runes := []rune(label); len(runes) > chars {
			label = string(runes[:chars])
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d">%s</text>`+"\n",
			x+2, y+rowH-5, htmlEscape(label))
	}
	b.WriteString("</svg>\n")
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
	if err := writeSVG(l.output, root, total); err != nil {
		return err
	}
	topPath := strings.TrimSuffix(l.output, ".svg") + ".txt"
	n := l.topN
	if n <= 0 {
		n = len(l.stacks)
	}
	return l.writeTopN(topPath, n)
}

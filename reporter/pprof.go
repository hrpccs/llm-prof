// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package reporter

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/ebpf-profiler/internal/pprofout"
)

// writePprof dumps the aggregated stacks in Google pprof protobuf format
// (gzip-compressed), so the profile can be analyzed side by side with py-spy
// data using `go tool pprof` / `pprof` (py-spy raw traces are converted with
// cmd/pyraw2pprof).
func (l *LocalReporter) writePprof(path string) error {
	b := pprofout.NewBuilder()
	for key, count := range l.stacks {
		b.AddStack(strings.Split(key, ";"), count)
	}
	gzipOut := strings.HasSuffix(path, ".gz") || filepath.Ext(path) == ".pb"
	return b.Write(path, gzipOut, 0)
}

// isPprofPath reports whether the output path requests pprof format
// (.pprof / .pb / .pb.gz).
func isPprofPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pprof", ".pb":
		return true
	}
	return strings.HasSuffix(strings.ToLower(path), ".pb.gz")
}

// writeOutput dispatches to the format requested by the output file
// extension: .svg -> flamegraph + txt top-N, pprof extensions -> pprof.
// Returns the auxiliary text path written next to the main output, if any.
func (l *LocalReporter) writeOutput() (string, error) {
	if isPprofPath(l.output) {
		if err := l.writePprof(l.output); err != nil {
			return "", err
		}
		topPath := strings.TrimSuffix(l.output, filepath.Ext(l.output))
		topPath = strings.TrimSuffix(topPath, ".pb")
		topPath += ".txt"
		n := l.topN
		if n <= 0 {
			n = len(l.stacks)
		}
		if err := l.writeTopN(topPath, n); err != nil {
			return "", fmt.Errorf("write top-N list: %w", err)
		}
		return topPath, nil
	}
	root := l.buildTree()
	if err := writeSVG(l.output, root, l.totalSamples(), l.pythonOnly); err != nil {
		return "", err
	}
	topPath := strings.TrimSuffix(l.output, ".svg") + ".txt"
	n := l.topN
	if n <= 0 {
		n = len(l.stacks)
	}
	if err := l.writeTopN(topPath, n); err != nil {
		return "", err
	}
	return topPath, nil
}

// totalSamples sums the weights of all aggregated stacks.
func (l *LocalReporter) totalSamples() int64 {
	var total int64
	for _, v := range l.stacks {
		total += v
	}
	return total
}

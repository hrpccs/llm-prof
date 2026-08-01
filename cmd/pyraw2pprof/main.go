// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// pyraw2pprof converts a py-spy raw trace (one stack per line,
// "frame;frame;...;frame [count]", root first) into the same Google pprof
// protobuf format that llm-prof emits with `-o out.pb.gz`. This lets py-spy
// and llm-prof profiles be compared with identical tooling:
//
//	go tool pprof -top py.pprof llm-prof.pb.gz
//
// Usage:
//
//	pyraw2pprof <py-spy-raw.txt> <out.pprof[.gz]>
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/ebpf-profiler/internal/pprofout"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pyraw2pprof <py-spy-raw.txt> <out.pprof[.gz]>")
		os.Exit(2)
	}
	in, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open input: %v\n", err)
		os.Exit(1)
	}
	defer in.Close()

	b := pprofout.NewBuilder()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// py-spy raw lines may carry a trailing sample count.
		count := int64(1)
		stack := line
		if i := strings.LastIndexByte(line, ' '); i > 0 {
			if c, err := strconv.ParseInt(line[i+1:], 10, 64); err == nil && c > 0 {
				count = c
				stack = line[:i]
			}
		}
		frames := strings.Split(stack, ";")
		trimmed := make([]string, 0, len(frames))
		for _, f := range frames {
			if f = strings.TrimSpace(f); f != "" {
				trimmed = append(trimmed, f)
			}
		}
		b.AddStack(trimmed, count)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}

	gzipOut := strings.HasSuffix(os.Args[2], ".gz")
	if err := b.Write(os.Args[2], gzipOut, 0); err != nil {
		fmt.Fprintf(os.Stderr, "write output: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d stacks)\n", os.Args[2], lineNo)
}

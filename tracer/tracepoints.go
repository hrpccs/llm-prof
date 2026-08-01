// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tracer // import "go.opentelemetry.io/ebpf-profiler/tracer"

import (
	"fmt"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
)

// attachToTracepoint attaches an eBPF program of type tracepoint to a tracepoint in the kernel
// defined by group and name.
// Otherwise it returns an error.
func (t *Tracer) attachToTracepoint(group, name string, prog *ebpf.Program) error {
	hp := hookPoint{
		group: group,
		name:  name,
	}
	hook, err := link.Tracepoint(hp.group, hp.name, prog, nil)
	if err != nil {
		return fmt.Errorf("failed to configure tracepoint on %#v: %v", hp, err)
	}
	t.hooks[hp] = hook
	return nil
}

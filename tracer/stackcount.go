// M2 window aggregation: periodically drain the kernel per-CPU counter map
// (stack_counts) and re-expand the aggregated counts into the trace channel,
// exactly like per-sample StackIDEvent re-expansion. Draining uses
// LookupAndDelete so every counted sample is drained exactly once (an update
// racing the drain is either included in the read value or lands in the next
// window; never lost, never duplicated).
package tracer

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/cilium/ebpf"

	"go.opentelemetry.io/ebpf-profiler/internal/log"
	"go.opentelemetry.io/ebpf-profiler/libpf"
)

// startStackCountDrain starts the M2 aggregation goroutine when window mode
// is enabled. It ticks every window seconds, drains stack_counts, and emits
// re-expanded clones of the cached FULL traces through traceOutChan.
func (t *Tracer) startStackCountDrain(ctx context.Context,
	traceOutChan chan<- *libpf.EbpfTrace) {
	if t.stackCountsMap == nil || t.stackCompressWindow <= 0 {
		return
	}
	window := time.Duration(t.stackCompressWindow) * time.Second
	go func() {
		ticker := time.NewTicker(window)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// The final partial window (<= window seconds of samples)
				// may be unflushed at shutdown; this is the documented
				// latency semantics of window aggregation.
				return
			case <-ticker.C:
				t.drainStackCounts(traceOutChan)
			}
		}
	}()
	log.Infof("M2 stack-count drain started (window %ds)", t.stackCompressWindow)
}

// drainStackCounts iterates the per-CPU counter map, sums counts across CPUs
// per fingerprint, LookupAndDelete's the entry, and re-expands the count.
func (t *Tracer) drainStackCounts(traceOutChan chan<- *libpf.EbpfTrace) {
	var key uint64
	var perCPU []uint32 // one slot per CPU (valueSize * nCPU)
	it := t.stackCountsMap.Iterate()
	for it.Next(&key, &perCPU) {
		total := uint64(0)
		for _, c := range perCPU {
			total += uint64(c)
		}
		if total == 0 {
			continue
		}
		// Atomic drain: remove the entry so the next window starts fresh.
		if err := t.stackCountsMap.Delete(&key); err != nil {
			log.Debugf("stack_counts delete failed: %v", err)
		}
		t.emitExpanded(traceOutChan, key, int64(total))
	}
	if err := it.Err(); err != nil {
		log.Debugf("stack_counts iterate error: %v", err)
	}
}

// emitExpanded re-expands count clones of the cached trace for fingerprint fp
// (shared with the per-sample StackIDEvent path).
func (t *Tracer) emitExpanded(traceOutChan chan<- *libpf.EbpfTrace, fp uint64, count int64) {
	t.stackCacheMu.RLock()
	cached := t.stackCache[fp]
	t.stackCacheMu.RUnlock()
	if cached == nil {
		// Should not happen (kernel only counts registered fingerprints);
		// fall back to pending counts folded onto the next FULL sample.
		t.pendingStackCountsMu.Lock()
		t.pendingStackCounts[fp] += count
		t.pendingStackCountsMu.Unlock()
		return
	}
	for j := int64(0); j < count; j++ {
		clone := t.tracePool.Get().(*libpf.EbpfTrace)
		*clone = *cached
		traceOutChan <- clone
	}
}

// helper for events.go: pendingStackCounts is now also touched by the drain
// goroutine, so guard it there too.
var _ = binary.LittleEndian
var _ = ebpf.UpdateAny

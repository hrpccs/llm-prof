package controller // import "go.opentelemetry.io/ebpf-profiler/internal/controller"

import (
	"flag"
	"fmt"
	"time"

	"go.opentelemetry.io/ebpf-profiler/internal/log"

	"go.opentelemetry.io/collector/consumer/xconsumer"
	"go.opentelemetry.io/ebpf-profiler/collector/config"
	"go.opentelemetry.io/ebpf-profiler/reporter"
)

type Config struct {
	config.Config
	CollAgentAddr string
	Copyright     bool
	DisableTLS    bool
	PprofAddr     string
	Version       bool
	// TargetPID restricts sampling to a single process (0 = all processes).
	TargetPID uint
	// OutputPath is the flamegraph SVG output path.
	OutputPath string
	// Duration, if non-zero, stops sampling after this duration.
	Duration time.Duration
	// TopN is the number of stacks in the text output (0 = all).
	TopN int
	// PythonOnly keeps only Python frames in the flamegraph/text output.
	PythonOnly bool
	// SampleStreamPath, if set, appends every decoded raw sample to this file
	// (one line per sample: KTime PID TID numFrames frame...). See tracer.Config.
	SampleStreamPath string
	// StackCompress enables M1 stack compression (see tracer.Config).
	StackCompress bool
	// StackCompressWindow enables M2 window aggregation (seconds; 0 = per-event).
	StackCompressWindow int

	ExecutableReporter reporter.ExecutableReporter
	OnShutdown         func() error

	// If ReporterFactory is set, it will be used to create a Reporter and set it as the Reporter field.
	// Either ReporterFactory or Reporter must be set. If both are set, ReporterFactory will be used.
	ReporterFactory func(cfg *reporter.Config, nextConsumer xconsumer.Profiles) (reporter.Reporter, error)
	Reporter        reporter.Reporter

	Fs *flag.FlagSet
}

// Dump visits all flag sets, and dumps them all to debug
// Used for verbose mode logging.
func (cfg *Config) Dump() {
	log.Debug("Config:")
	cfg.Fs.VisitAll(func(f *flag.Flag) {
		log.Debug(fmt.Sprintf("%s: %v", f.Name, f.Value))
	})
}

// Validate runs validations on the provided configuration, and returns errors
// if invalid values were provided.
func (cfg *Config) Validate() error {
	return cfg.Config.Validate()
}

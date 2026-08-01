// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package interpreterconfig aggregates per-interpreter configuration.
package interpreterconfig // import "go.opentelemetry.io/ebpf-profiler/interpreter/interpreterconfig"

import (
	"go.opentelemetry.io/ebpf-profiler/interpreter"
	"go.opentelemetry.io/ebpf-profiler/interpreter/python"
)

// Config holds configuration for all interpreters.
// By default all interpreters are enabled.
type Config struct {
	Python python.Config `mapstructure:"python" json:"python,omitempty"`
}

// AllInterpreters returns a Config with all interpreters enabled.
func AllInterpreters() Config { return Config{} }

// NoInterpreters returns a Config with all interpreters disabled.
func NoInterpreters() Config {
	disabled := interpreter.BaseConfig{Disabled: true}
	return Config{
		Python: python.Config{BaseConfig: disabled},
	}
}

// IsMapEnabled returns true if for the given mapName the respective
// configuration is enabled.
func (cfg *Config) IsMapEnabled(mapName string) bool {
	switch mapName {
	case python.BPFMapName:
		return !cfg.Python.IsDisabled()
	default:
		return true // Not an interpreter map, so it should be loaded
	}
}

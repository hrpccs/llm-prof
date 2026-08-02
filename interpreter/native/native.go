// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package native provides ELF symbolization for native (C/C++/Rust/...) frames.
// Without it, native frames such as CPython internals or libc show up as raw
// addresses. It matches any ELF that carries a symbol table and resolves
// addresses to the nearest preceding symbol (function name).
package native // import "go.opentelemetry.io/ebpf-profiler/interpreter/native"

import (
	"debug/elf"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ianlancetaylor/demangle"

	"go.opentelemetry.io/ebpf-profiler/host"

	"go.opentelemetry.io/ebpf-profiler/interpreter"
	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/libpf/pfelf"
	"go.opentelemetry.io/ebpf-profiler/remotememory"
)

// demangleCache memoizes C++ demangling results (mangled -> demangled).
// Only symbols actually hit during symbolization are demangled, so large
// libraries (e.g. libtorch) pay the cost lazily.
var demangleCache sync.Map // map[string]string

// demangleName converts an Itanium-mangled C++ symbol to a readable form;
// non-mangled names and mangled names that fail to parse pass through.
func demangleName(name libpf.SymbolName) libpf.SymbolName {
	s := string(name)
	if !strings.HasPrefix(s, "_Z") {
		return name
	}
	if v, ok := demangleCache.Load(s); ok {
		return libpf.SymbolName(v.(string))
	}
	out := s
	if d, err := demangle.ToString(s); err == nil {
		out = d
	}
	demangleCache.Store(s, out)
	return libpf.SymbolName(out)
}

// symbol is one ELF symbol (virtual address -> name), with size for
// coverage checks.
type symbol struct {
	addr libpf.Address
	size libpf.Address
	name libpf.SymbolName
}

// nativeData implements interpreter.Data: it matches any ELF with symbols and
// holds the loaded symbol table shared by all per-PID instances.
type nativeData struct {
	fileID host.FileID
	syms   []symbol // sorted by addr (virtual addresses, st_value semantics)
	mapper pfelf.AddressMapper
}

// nativeInstance symbolizes native frames against one ELF's symbol table.
type nativeInstance struct {
	interpreter.InstanceStubs
	fileID host.FileID
	syms   []symbol // shared with the Data, read-only
	mapper pfelf.AddressMapper
}

var _ interpreter.Data = &nativeData{}
var _ interpreter.Instance = &nativeInstance{}

func (d *nativeData) String() string { return "native" }

// GetLoader returns the native symbolization loader.
func GetLoader() interpreter.Loader { return loader }

func loader(ebpf interpreter.EbpfHandler, info *interpreter.LoaderInfo) (interpreter.Data, error) {
	syms, err := loadSymbols(info.FileName())
	if err != nil || len(syms) == 0 {
		return nil, nil //nolint:nilerr // not an interpreter we handle
	}
	ef, err := info.GetELF()
	if err != nil {
		return nil, err
	}
	return &nativeData{fileID: info.FileID(), syms: syms, mapper: ef.GetAddressMapper()}, nil
}

// loadSymbols reads .symtab (falling back to .dynsym) via debug/elf and
// returns the sorted list of function symbols. STT_OBJECT/data symbols are
// excluded so addresses resolve to function names (e.g. CPython internals),
// not to nearby data globals like __signgam.
func loadSymbols(path string) ([]symbol, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := f.Symbols()
	if err != nil || len(syms) == 0 {
		syms, err = f.DynamicSymbols()
		if err != nil {
			return nil, err
		}
	}
	out := make([]symbol, 0, len(syms))
	for _, s := range syms {
		if s.Value == 0 {
			continue
		}
		switch elf.ST_TYPE(s.Info) {
		case elf.STT_FUNC, elf.STT_GNU_IFUNC, elf.STT_NOTYPE:
			out = append(out, symbol{addr: libpf.Address(s.Value),
				size: libpf.Address(s.Size), name: libpf.SymbolName(s.Name)})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no function symbols")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].addr < out[j].addr })
	return out, nil
}

// Attach shares the symbol table with a new per-PID instance.
func (d *nativeData) Attach(_ interpreter.EbpfHandler, _ libpf.PID, _ libpf.Address,
	_ remotememory.RemoteMemory,
) (interpreter.Instance, error) {
	return &nativeInstance{fileID: d.fileID, syms: d.syms, mapper: d.mapper}, nil
}

// Unload is a no-op: native symbolization keeps no eBPF state.
func (d *nativeData) Unload(interpreter.EbpfHandler) {}

// Detach is a no-op.
func (p *nativeInstance) Detach(interpreter.EbpfHandler, libpf.PID) error { return nil }

// Symbolize resolves a native frame address to the nearest preceding symbol.
// The frame's absolute address is converted to a file offset using the
// mapping (start + file offset), then binary-searched against the symbol
// table. Non-native frames are rejected so other interpreters get a chance.
func (p *nativeInstance) Symbolize(ef libpf.EbpfFrame, frames *libpf.Frames,
	mapping libpf.FrameMapping,
) error {
	if ef.Type().Interpreter() != libpf.Native {
		return interpreter.ErrMismatchInterpreterType
	}
	if !mapping.Valid() || len(p.syms) == 0 {
		return interpreter.ErrMismatchInterpreterType
	}
	// This instance's symbol table belongs to one specific file: only
	// symbolize frames whose mapping matches, otherwise the address would
	// be resolved against the wrong ELF (cross-file mis-symbolization).
	if md := mapping.Value(); host.FileIDFromLibpf(md.File.Value().FileID) != p.fileID {
		return interpreter.ErrMismatchInterpreterType
	}
	address := libpf.Address(ef.Data())
	md := mapping.Value()
	fileOffset := uint64(address - md.Start + libpf.Address(md.FileOffset))
	// Convert the file offset to a virtual address (st_value semantics) via
	// the ELF segment layout, then find the nearest preceding symbol.
	vaddr, ok := p.mapper.FileOffsetToVirtualAddress(fileOffset)
	if !ok {
		return interpreter.ErrMismatchInterpreterType
	}
	i := sort.Search(len(p.syms), func(i int) bool {
		return p.syms[i].addr > libpf.Address(vaddr)
	})
	if i == 0 {
		return interpreter.ErrMismatchInterpreterType
	}
	sym := p.syms[i-1]
	// Coverage check: when the symbol has a size, the address must fall
	// inside [addr, addr+size). Sparse tables (stripped binaries with only
	// .dynsym) otherwise resolve to the nearest preceding *exported* symbol,
	// producing plausible-looking but wrong function names.
	if sym.size > 0 && libpf.Address(vaddr) >= sym.addr+sym.size {
		return interpreter.ErrMismatchInterpreterType
	}
	frames.Append(&libpf.Frame{
		Type:            libpf.NativeFrame,
		FunctionName:    libpf.Intern(string(demangleName(sym.name))),
		AddressOrLineno: libpf.AddressOrLineno(vaddr),
	})
	return nil
}

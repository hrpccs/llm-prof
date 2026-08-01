// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package pprofout encodes stack samples into the Google pprof protobuf
// format (github.com/google/pprof/blob/main/proto/profile.proto) with zero
// external dependencies. Both llm-prof's native output and the py-spy raw
// converter share this encoder, so the two tools can be compared with the
// standard `go tool pprof` / `pprof` tooling.
package pprofout

import (
	"compress/gzip"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// wire types
const (
	wireVarint = 0
	wireBytes  = 2
)

// protoWriter accumulates a protobuf message body.
type protoWriter struct {
	buf []byte
}

func (w *protoWriter) varint(v uint64) {
	for v >= 0x80 {
		w.buf = append(w.buf, byte(v)|0x80)
		v >>= 7
	}
	w.buf = append(w.buf, byte(v))
}

func (w *protoWriter) tag(field int, wire int) {
	w.varint(uint64(field)<<3 | uint64(wire))
}

func (w *protoWriter) int64Field(field int, v int64) {
	w.tag(field, wireVarint)
	w.varint(uint64(v))
}

func (w *protoWriter) uint64Field(field int, v uint64) {
	w.tag(field, wireVarint)
	w.varint(v)
}

func (w *protoWriter) bytesField(field int, data []byte) {
	w.tag(field, wireBytes)
	w.varint(uint64(len(data)))
	w.buf = append(w.buf, data...)
}

func (w *protoWriter) strField(field int, s string) {
	w.bytesField(field, []byte(s))
}

// packedUint64Field writes a packed repeated uint64 field.
func (w *protoWriter) packedUint64Field(field int, vals []uint64) {
	if len(vals) == 0 {
		return
	}
	var inner protoWriter
	for _, v := range vals {
		inner.varint(v)
	}
	w.bytesField(field, inner.buf)
}

// packedInt64Field writes a packed repeated int64 field.
func (w *protoWriter) packedInt64Field(field int, vals []int64) {
	if len(vals) == 0 {
		return
	}
	var inner protoWriter
	for _, v := range vals {
		inner.varint(uint64(v))
	}
	w.bytesField(field, inner.buf)
}

// messageField wraps a sub-message as a length-delimited field.
func (w *protoWriter) messageField(field int, sub *protoWriter) {
	w.bytesField(field, sub.buf)
}

// Frame is one symbolized stack frame.
type Frame struct {
	Name     string
	Filename string
	Line     int64
}

// parseFrame splits a frame label rendered by frameLabel() (llm-prof) or by
// py-spy's raw format:
//
//	"name (file:line)"  -> Name=name, Filename=file, Line=line
//	"0xADDR"            -> Name=0xADDR
func parseFrame(label string) Frame {
	if strings.HasPrefix(label, "0x") || strings.HasPrefix(label, "0X") {
		return Frame{Name: label}
	}
	if i := strings.LastIndex(label, " ("); i > 0 && strings.HasSuffix(label, ")") {
		inner := label[i+2 : len(label)-1]
		if j := strings.LastIndex(inner, ":"); j > 0 {
			if ln, err := strconv.ParseInt(inner[j+1:], 10, 64); err == nil {
				return Frame{Name: label[:i], Filename: inner[:j], Line: ln}
			}
		}
		return Frame{Name: label[:i], Filename: inner}
	}
	return Frame{Name: label}
}

// ParseFrame exposes frame parsing for converters (e.g. py-spy raw).
func ParseFrame(label string) Frame { return parseFrame(label) }

// Builder accumulates samples and renders a pprof profile.
type Builder struct {
	// string table; index 0 is the empty string
	strtab []string
	strIdx map[string]int64

	funcs    []protoWriter
	funcIdx  map[Frame]uint64
	locIdx   map[Frame]uint64
	locations []protoWriter

	samples []protoWriter
}

// NewBuilder creates an empty pprof profile builder.
func NewBuilder() *Builder {
	b := &Builder{
		strtab: []string{""},
		strIdx: map[string]int64{"": 0},
		funcIdx: map[Frame]uint64{},
		locIdx:  map[Frame]uint64{},
	}
	return b
}

// stringIndex interns a string into the string table.
func (b *Builder) stringIndex(s string) int64 {
	if i, ok := b.strIdx[s]; ok {
		return i
	}
	i := int64(len(b.strtab))
	b.strtab = append(b.strtab, s)
	b.strIdx[s] = i
	return i
}

// frameFunction returns (functionID, locationID) for a frame, creating both
// on first use. Locations are interned per unique frame.
func (b *Builder) frameLocation(f Frame) (uint64, uint64) {
	if id, ok := b.locIdx[f]; ok {
		return b.funcIdx[f], id
	}
	fid, ok := b.funcIdx[f]
	if !ok {
		fid = uint64(len(b.funcs) + 1)
		var fn protoWriter
		fn.uint64Field(1, fid)                  // id
		fn.int64Field(2, b.stringIndex(f.Name)) // name
		fn.int64Field(3, b.stringIndex(f.Name)) // system_name
		if f.Filename != "" {
			fn.int64Field(4, b.stringIndex(f.Filename)) // filename
		}
		if f.Line > 0 {
			fn.int64Field(5, f.Line) // start_line
		}
		b.funcs = append(b.funcs, fn)
		b.funcIdx[f] = fid
	}

	var loc protoWriter
	loc.uint64Field(1, uint64(len(b.locations)+1)) // id
	var line protoWriter
	line.uint64Field(1, fid) // function_id
	if f.Line > 0 {
		line.int64Field(2, f.Line) // line
	}
	loc.messageField(4, &line) // line[]
	b.locations = append(b.locations, loc)
	id := uint64(len(b.locations))
	b.locIdx[f] = id
	return fid, id
}

// AddStack records one sample. frames is ordered root -> leaf (as stored by
// the local reporter and py-spy raw); the pprof sample stores leaf -> root.
func (b *Builder) AddStack(frames []string, count int64) {
	if len(frames) == 0 || count <= 0 {
		return
	}
	locIDs := make([]uint64, 0, len(frames))
	for i := len(frames) - 1; i >= 0; i-- {
		_, id := b.frameLocation(parseFrame(frames[i]))
		locIDs = append(locIDs, id)
	}
	var s protoWriter
	s.packedUint64Field(1, locIDs) // location_id
	s.packedInt64Field(2, []int64{count})
	b.samples = append(b.samples, s)
}

// Write serializes the profile as gzip-compressed protobuf (or plain when
// gzipOut is false) to path.
func (b *Builder) Write(path string, gzipOut bool, duration time.Duration) error {
	var p protoWriter

	// sample_type: "samples" / "count"
	var st protoWriter
	st.int64Field(1, b.stringIndex("samples"))
	st.int64Field(2, b.stringIndex("count"))
	p.messageField(1, &st)

	for i := range b.samples {
		p.messageField(2, &b.samples[i])
	}
	for i := range b.locations {
		p.messageField(4, &b.locations[i])
	}
	for i := range b.funcs {
		p.messageField(5, &b.funcs[i])
	}
	for _, s := range b.strtab {
		p.strField(6, s)
	}
	p.int64Field(9, time.Now().UnixNano()) // time_nanos
	if duration > 0 {
		p.int64Field(10, int64(duration)) // duration_nanos
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open pprof output: %w", err)
	}
	defer f.Close()

	if gzipOut {
		zw, err := gzip.NewWriterLevel(f, gzip.BestSpeed)
		if err != nil {
			return err
		}
		if _, err := zw.Write(p.buf); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
	} else {
		if _, err := f.Write(p.buf); err != nil {
			return err
		}
	}
	return nil
}

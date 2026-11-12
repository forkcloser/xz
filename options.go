// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/forkcloser/xz/v2/internal/filters"
	"github.com/forkcloser/xz/v2/lzma"
)

// An Option configures a Reader or a Writer. Each option names the
// constructor it applies to; a constructor handed the other's option
// returns an error.
type Option struct {
	name string
	set  func(*options)
}

// options is what the constructors read after applying their options.
type options struct {
	set optionSet
	// reader
	maxDictSize  int64
	singleStream bool
	workers      int
	// writer
	dictSize  int
	props     lzma.Properties
	blockSize int64
	check     Check
	filters   []Filter
}

// optionSet records which options were given, one bit each.
type optionSet uint16

const (
	optMaxDictSize optionSet = 1 << iota
	optSingleStream
	optWorkers
	optDictSize
	optProperties
	optBlockSize
	optCheck
	optFilters
)

const (
	readerOptions = optMaxDictSize | optSingleStream | optWorkers
	writerOptions = optDictSize | optProperties | optBlockSize | optCheck | optFilters
)

// What the constructors refuse of their options.
var (
	errOptionScope   = errors.New("xz: option does not apply to this constructor")
	errMaxDictSize   = errors.New("xz: dictionary size limit out of range")
	errWorkers       = errors.New("xz: workers must be positive")
	errBlockSize     = errors.New("xz: block size must be positive")
	errCheck         = errors.New("xz: unknown check")
	errTooManyBefore = errors.New("xz: more than three filters before LZMA2")
	errFilterID      = errors.New("xz: unknown filter")
	errDistance      = errors.New("xz: delta distance out of range")
	errStartOffset   = errors.New("xz: start offset for a filter without one")
)

// defaultDictSize is the writer's dictionary when none is asked for.
const defaultDictSize = 8 << 20

// WithMaxDictSize bounds the dictionary a block header may ask for: a
// block asking for more is refused with ErrUnsupported. The dictionary is
// grown on demand and costs what the block decodes, so the default is no
// bound below the format's 4 GiB-1; the option is for a reader of untrusted
// input that wants a smaller one. NewReader.
func WithMaxDictSize(n int64) Option {
	return Option{"WithMaxDictSize", func(o *options) {
		o.set |= optMaxDictSize
		o.maxDictSize = n
	}}
}

// WithSingleStream makes the reader stop after the first xz stream instead
// of continuing through stream padding into further concatenated streams.
// Any byte after the stream is then reported as corruption, which is what
// the reference tool does with --single-stream. NewReader.
func WithSingleStream() Option {
	return Option{"WithSingleStream", func(o *options) {
		o.set |= optSingleStream
		o.singleStream = true
	}}
}

// WithWorkers sets how many blocks the reader decodes concurrently, 0 for
// the number of CPUs (default 1, no concurrency). Blocks decode in parallel
// when the input is a file or another io.ReaderAt that is also an
// io.Seeker, through the file's indexes; a stream decodes in order on one
// worker. A reader with workers must be closed when abandoned before
// io.EOF. NewReader.
func WithWorkers(n int) Option {
	return Option{"WithWorkers", func(o *options) {
		o.set |= optWorkers
		o.workers = n
	}}
}

// WithDictSize sets the encoder's dictionary size, lzma.MinDictSize to
// lzma.MaxDictSize (default 8 MiB). A larger dictionary finds longer
// matches at the cost of memory on both sides; the size is recorded in
// every block header. NewWriter.
func WithDictSize(n int) Option {
	return Option{"WithDictSize", func(o *options) {
		o.set |= optDictSize
		o.dictSize = n
	}}
}

// WithProperties sets the encoder's literal context bits, literal position
// bits and position bits (default 3, 0 and 2, what xz uses). NewWriter.
func WithProperties(lc, lp, pb int) Option {
	return Option{"WithProperties", func(o *options) {
		o.set |= optProperties
		o.props = lzma.Properties{LC: lc, LP: lp, PB: pb}
	}}
}

// WithBlockSize makes the writer close a block and start a new one every
// n uncompressed bytes (default: one block). Multi-block files are what a
// reader with workers decodes concurrently. NewWriter.
func WithBlockSize(n int64) Option {
	return Option{"WithBlockSize", func(o *options) {
		o.set |= optBlockSize
		o.blockSize = n
	}}
}

// WithCheck selects the integrity check of every block (default CRC64).
// NewWriter.
func WithCheck(c Check) Option {
	return Option{"WithCheck", func(o *options) {
		o.set |= optCheck
		o.check = c
	}}
}

// WithFilters applies filters before LZMA2, in the order given: up to
// three of the BCJ filters and Delta, as the format allows. NewWriter.
func WithFilters(f ...Filter) Option {
	return Option{"WithFilters", func(o *options) {
		o.set |= optFilters
		o.filters = f
	}}
}

// A Check is the integrity check of a block.
type Check uint8

// The checks the format defines. CheckDefault is CRC64.
const (
	CheckDefault Check = iota
	CheckNone
	CheckCRC32
	CheckCRC64
	CheckSHA256
)

// flags is the check's value in the stream flags.
func (c Check) flags() (byte, error) {
	switch c {
	case CheckDefault, CheckCRC64:
		return checkCRC64, nil
	case CheckNone:
		return checkNone, nil
	case CheckCRC32:
		return checkCRC32, nil
	case CheckSHA256:
		return checkSHA256, nil
	}

	return 0, fmt.Errorf("%w: %d", errCheck, c)
}

// A FilterID names one of the filters a block may apply before LZMA2.
type FilterID uint8

// The filters the format defines besides LZMA2.
const (
	X86 FilterID = iota + 1
	PowerPC
	IA64
	ARM
	ARMThumb
	SPARC
	ARM64
	RISCV
	Delta
)

// String returns the filter's name as xz spells it.
func (id FilterID) String() string {
	switch id {
	case X86:
		return "x86"
	case PowerPC:
		return "powerpc"
	case IA64:
		return "ia64"
	case ARM:
		return "arm"
	case ARMThumb:
		return "armthumb"
	case SPARC:
		return "sparc"
	case ARM64:
		return "arm64"
	case RISCV:
		return "riscv"
	case Delta:
		return "delta"
	}

	return fmt.Sprintf("filter(%d)", uint8(id))
}

// wire is the filter's ID in the block header.
func (id FilterID) wire() (uint64, bool) {
	switch id {
	case X86:
		return filters.X86ID, true
	case PowerPC:
		return filters.PowerPCID, true
	case IA64:
		return filters.IA64ID, true
	case ARM:
		return filters.ARMID, true
	case ARMThumb:
		return filters.ARMThumbID, true
	case SPARC:
		return filters.SPARCID, true
	case ARM64:
		return filters.ARM64ID, true
	case RISCV:
		return filters.RISCVID, true
	case Delta:
		return filters.DeltaID, true
	}

	return 0, false
}

// A Filter is one filter of a block's chain before LZMA2.
type Filter struct {
	ID FilterID
	// StartOffset is the address the BCJ filters count from, a multiple of
	// the filter's alignment (x86 1, ARM Thumb and RISC-V 2, IA-64 16, the
	// others 4); zero for Delta.
	StartOffset uint32
	// Distance is the Delta filter's distance, 1 to 256; zero for the BCJ
	// filters.
	Distance int
}

// header returns the filter as the block header carries it.
func (f Filter) header() (filter, error) {
	id, ok := f.ID.wire()
	if !ok {
		return nil, fmt.Errorf("%w: %s", errFilterID, f.ID)
	}

	if f.ID == Delta {
		if f.StartOffset != 0 {
			return nil, fmt.Errorf("%w: %s", errStartOffset, f.ID)
		}

		if f.Distance < filters.MinDistance || f.Distance > filters.MaxDistance {
			return nil, fmt.Errorf("%w: %d", errDistance, f.Distance)
		}

		return &deltaFilter{distance: f.Distance}, nil
	}

	if f.Distance != 0 {
		return nil, fmt.Errorf("%w: distance %d for %s", errDistance, f.Distance, f.ID)
	}

	if _, err := filters.NewBCJ(id, f.StartOffset, true); err != nil {
		return nil, fmt.Errorf("xz: %w", err)
	}

	return &bcjFilter{fid: id, startOffset: f.StartOffset}, nil
}

// apply folds opts into the defaults, refusing any option outside allowed.
func apply(opts []Option, allowed optionSet, constructor string) (options, error) {
	o := options{
		maxDictSize: lzma.MaxDictSize,
		workers:     1,
		dictSize:    defaultDictSize,
		props:       lzma.Properties{LC: 3, LP: 0, PB: 2},
		blockSize:   maxInt64,
	}

	for _, opt := range opts {
		opt.set(&o)
	}

	if err := outOfScope(opts, o.set&^allowed, constructor); err != nil {
		return o, err
	}

	if o.set&optWorkers != 0 && o.workers == 0 {
		o.workers = runtime.GOMAXPROCS(0)
	}

	if err := o.validate(); err != nil {
		return o, err
	}

	return o, nil
}

// outOfScope names the first option whose bit is among bad.
func outOfScope(opts []Option, bad optionSet, constructor string) error {
	if bad == 0 {
		return nil
	}

	for _, opt := range opts {
		var probe options

		opt.set(&probe)

		if probe.set&bad != 0 {
			return fmt.Errorf("%w: %s in %s", errOptionScope, opt.name, constructor)
		}
	}

	return nil
}

// validate checks every value against its range.
func (o *options) validate() error {
	if !(lzma.MinDictSize <= o.maxDictSize && o.maxDictSize <= lzma.MaxDictSize) {
		return fmt.Errorf("%w: %d", errMaxDictSize, o.maxDictSize)
	}

	if o.workers < 1 {
		return fmt.Errorf("%w: %d", errWorkers, o.workers)
	}

	if o.blockSize <= 0 {
		return fmt.Errorf("%w: %d", errBlockSize, o.blockSize)
	}

	if _, err := o.check.flags(); err != nil {
		return err
	}

	if len(o.filters) > maxFilters-1 {
		return fmt.Errorf("%w: %d", errTooManyBefore, len(o.filters))
	}

	return nil
}

// chain is the writer's filter list: the filters asked for, then LZMA2.
func (o *options) chain() ([]filter, error) {
	fs := make([]filter, 0, len(o.filters)+1)

	for _, f := range o.filters {
		h, err := f.header()
		if err != nil {
			return nil, err
		}

		fs = append(fs, h)
	}

	return append(fs, &lzmaFilter{dictSize: int64(o.dictSize)}), nil
}

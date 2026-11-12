// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"fmt"
)

// An Option configures a reader or writer. Each option names the
// constructors it applies to; a constructor handed another's option returns
// an error.
type Option struct {
	name string
	set  func(*options)
}

// options is what the constructors read after applying their options.
type options struct {
	set         optionSet
	maxDictSize int64
	dictSize    int
	props       Properties
	size        int64
	eosMarker   bool
}

// optionSet records which options were given, one bit each.
type optionSet uint8

const (
	optMaxDictSize optionSet = 1 << iota
	optDictSize
	optProperties
	optSize
	optEOSMarker
)

// What the constructors refuse of their options.
var (
	errOptionScope  = errors.New("lzma: option does not apply to this constructor")
	errDictSize     = errors.New("lzma: dictionary size out of range")
	errMaxDictSize  = errors.New("lzma: dictionary size limit out of range")
	errNegativeSize = errors.New("lzma: negative size")
)

// defaultDictSize is the writers' dictionary when none is asked for.
const defaultDictSize = 8 << 20

// WithMaxDictSize bounds the dictionary a classic LZMA header may declare:
// a header asking for more is refused with ErrUnsupported, so a mangled or
// hostile header cannot drive the allocation. The default is 2 GiB-1.
// NewReader.
func WithMaxDictSize(n int64) Option {
	return Option{"WithMaxDictSize", func(o *options) {
		o.set |= optMaxDictSize
		o.maxDictSize = n
	}}
}

// WithDictSize sets the encoder's dictionary size, MinDictSize to
// MaxDictSize (default 8 MiB). A larger dictionary finds longer matches at
// the cost of memory on both sides. NewWriter, NewWriter2.
func WithDictSize(n int) Option {
	return Option{"WithDictSize", func(o *options) {
		o.set |= optDictSize
		o.dictSize = n
	}}
}

// WithProperties sets the encoder's literal context bits, literal position
// bits and position bits (default 3, 0 and 2, what xz uses). NewWriter,
// NewWriter2.
func WithProperties(lc, lp, pb int) Option {
	return Option{"WithProperties", func(o *options) {
		o.set |= optProperties
		o.props = Properties{LC: lc, LP: lp, PB: pb}
	}}
}

// WithSize declares the uncompressed size in the classic LZMA header: Write
// accepts exactly that many bytes and refuses the rest with
// ErrSizeExceeded, and the stream carries no end marker unless
// WithEOSMarker asks for one. Without it the size is unknown and the end
// marker is written. NewWriter.
func WithSize(n int64) Option {
	return Option{"WithSize", func(o *options) {
		o.set |= optSize
		o.size = n
	}}
}

// WithEOSMarker writes the end-of-stream marker even when WithSize declares
// the size. NewWriter.
func WithEOSMarker() Option {
	return Option{"WithEOSMarker", func(o *options) {
		o.set |= optEOSMarker
		o.eosMarker = true
	}}
}

// apply folds opts into the defaults, refusing any option outside allowed.
func apply(opts []Option, allowed optionSet, constructor string) (options, error) {
	o := options{
		maxDictSize: 1<<31 - 1,
		dictSize:    defaultDictSize,
		// What xz uses.
		props: Properties{LC: 3, LP: 0, PB: 2},
		size:  -1,
	}

	for _, opt := range opts {
		opt.set(&o)
	}

	if err := outOfScope(opts, o.set&^allowed, constructor); err != nil {
		return o, err
	}

	// Without a declared size the end marker is the only end there is.
	if o.set&optSize == 0 {
		o.eosMarker = true
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
	if o.set&optMaxDictSize != 0 && !(MinDictSize <= o.maxDictSize && o.maxDictSize <= MaxDictSize) {
		return fmt.Errorf("%w: %d", errMaxDictSize, o.maxDictSize)
	}

	if !(MinDictSize <= o.dictSize && int64(o.dictSize) <= MaxDictSize) {
		return fmt.Errorf("%w: %d", errDictSize, o.dictSize)
	}

	if err := o.props.verify(); err != nil {
		return err
	}

	if o.set&optSize != 0 && o.size < 0 {
		return fmt.Errorf("%w: %d", errNegativeSize, o.size)
	}

	return nil
}

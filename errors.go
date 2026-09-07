// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"fmt"
	"io"

	"github.com/forkcloser/xz/lzma"
)

// Sentinel errors that callers can test for with errors.Is.
//
// The distinction that matters in practice is between a file that is not a
// valid xz stream and a transport that failed underneath us: the first means
// reject the input, the second means the read may be worth retrying. Telling
// them apart used to require matching on message text.
var (
	// ErrCorrupt reports that the data being read is not a valid xz stream:
	// bad magic, a failed checksum, sizes that disagree, a reserved field
	// that is set. Every such error from this package matches it. An I/O
	// error from the underlying reader is passed through untouched, so it
	// does not match.
	ErrCorrupt = errors.New("xz: corrupt input")

	// ErrClosed reports that a reader was used after Close.
	ErrClosed = errors.New("xz: already closed")

	// ErrUnsupported reports a stream this package cannot decode even though
	// it may be well formed, such as a filter other than LZMA2.
	ErrUnsupported = errors.New("xz: unsupported feature")
)

// kindError carries a specific message while matching one of the sentinels
// above. Error returns the message alone: the sentinel is reachable through
// Unwrap, so errors.Is works without "xz: corrupt input: " being prepended to
// every diagnostic.
type kindError struct {
	msg  string
	kind error
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

// corruptf builds an error that describes how the input is malformed and
// matches ErrCorrupt.
func corruptf(format string, args ...any) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrCorrupt}
}

// unsupportedf builds an error that matches ErrUnsupported.
func unsupportedf(format string, args ...any) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrUnsupported}
}

// classifiedError attaches one of this package's sentinels to an error from
// the lzma package without hiding it: errors.Is finds both the sentinel and
// the original chain, and the message stays the decoder's.
type classifiedError struct {
	err  error
	kind error
}

func (e *classifiedError) Error() string   { return e.err.Error() }
func (e *classifiedError) Unwrap() []error { return []error{e.kind, e.err} }

// classify maps the lzma package's sentinels onto this package's, so a
// caller sees one vocabulary whether the fault was found in the xz container
// or inside the LZMA2 payload. Errors that are neither — I/O errors from the
// underlying reader, io.ErrUnexpectedEOF — pass through unchanged.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCorrupt), errors.Is(err, ErrUnsupported):
		return err
	case errors.Is(err, lzma.ErrCorrupt):
		return &classifiedError{err: err, kind: ErrCorrupt}
	case errors.Is(err, lzma.ErrUnsupported):
		return &classifiedError{err: err, kind: ErrUnsupported}
	}
	return err
}

// Interface assertions. WriteTo in particular is a behavioural contract that
// io.Copy picks up silently, so it is worth pinning rather than leaving to be
// noticed when it disappears.
var (
	_ io.ReadCloser  = (*ParallelReader)(nil)
	_ io.WriterTo    = (*ParallelReader)(nil)
	_ io.Reader      = (*Reader)(nil)
	_ io.WriteCloser = (*Writer)(nil)
)

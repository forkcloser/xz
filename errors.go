// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"fmt"
	"io"
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
func corruptf(format string, args ...interface{}) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrCorrupt}
}

// unsupportedf builds an error that matches ErrUnsupported.
func unsupportedf(format string, args ...interface{}) error {
	return &kindError{msg: fmt.Sprintf(format, args...), kind: ErrUnsupported}
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

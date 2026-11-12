// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bufio"
	"errors"
	"io"
)

// MinDictSize and MaxDictSize bound the dictionary size.
const (
	MinDictSize = 1 << 12
	MaxDictSize = 1<<32 - 1
)

// lookaheadSize is the encoder's lookahead buffer, which bounds the longest
// match it looks for.
const lookaheadSize = 4096

// Writer writes an LZMA stream in the classic format.
type Writer struct {
	h   Header
	bw  io.ByteWriter
	buf *bufio.Writer
	e   *encoder
	// err is the first failure of the underlying writer; Write and Close
	// return it from then on rather than continue on an encoder whose
	// dictionary was partly consumed by the failed operation.
	err error
	// closed is set by Close: a second Close must not write a second end.
	closed bool
}

// NewWriter creates a writer for an LZMA stream in the classic format and
// writes the header to lzma. Options: WithDictSize, WithProperties,
// WithSize, WithEOSMarker.
func NewWriter(lzma io.Writer, opts ...Option) (w *Writer, err error) {
	o, err := apply(opts, optDictSize|optProperties|optSize|optEOSMarker, "NewWriter")
	if err != nil {
		return nil, err
	}

	w = &Writer{h: Header{
		Properties: o.props,
		// #nosec G115 -- the dictionary size is within MinDictSize to MaxDictSize
		DictSize: uint32(o.dictSize),
		Size:     o.size,
	}}

	var ok bool

	w.bw, ok = lzma.(io.ByteWriter)
	if !ok {
		w.buf = bufio.NewWriter(lzma)
		w.bw = w.buf
	}

	state := newState(w.h.Properties)

	m, err := newHashTable(int(w.h.DictSize), 4)
	if err != nil {
		return nil, err
	}

	dict, err := newEncoderDict(int(w.h.DictSize), lookaheadSize, m)
	if err != nil {
		return nil, err
	}

	var flags encoderFlags
	if o.eosMarker {
		flags = eosMarker
	}

	w.e = newEncoder(w.bw, state, dict, flags)

	if err = w.writeHeader(); err != nil {
		return nil, err
	}

	return w, nil
}

// Write puts data into the Writer. When the header declares a size, Write
// accepts at most that many bytes in total and returns ErrSizeExceeded for the
// rest.
func (w *Writer) Write(p []byte) (n int, err error) {
	if w.err != nil {
		return 0, w.err
	}

	if w.closed {
		return 0, errClosed
	}

	if w.h.Size >= 0 {
		m := w.h.Size

		m -= w.e.Compressed() + int64(w.e.dict.Buffered())
		if m < 0 {
			m = 0
		}

		if m < int64(len(p)) {
			p = p[:m]
			err = ErrSizeExceeded
		}
	}

	var werr error
	if n, werr = w.e.Write(p); werr != nil {
		w.err = werr
		err = werr
	}

	return n, err
}

// errSizeShort is a Close before the size the header declares was written.
var errSizeShort = errors.New("lzma: fewer bytes written than the header's size")

// Close closes the writer stream. It ensures that all data from the
// buffer will be compressed and the LZMA stream will be finished. After a
// failed Write it returns that error and writes nothing; a second Close
// writes nothing and reports the writer closed.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}

	if w.closed {
		return errClosed
	}

	if w.h.Size >= 0 {
		n := w.e.Compressed() + int64(w.e.dict.Buffered())
		if n != w.h.Size {
			return errSizeShort
		}
	}

	w.closed = true

	err := w.e.Close()
	if w.buf != nil {
		ferr := w.buf.Flush()
		if err == nil {
			err = ferr
		}
	}

	w.err = err

	return err
}

// writeHeader writes the LZMA header into the stream.
func (w *Writer) writeHeader() error {
	data, err := w.h.marshalBinary()
	if err != nil {
		return err
	}

	_, err = w.bw.(io.Writer).Write(data)

	return err
}

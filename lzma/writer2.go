// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"errors"
	"io"
)

// Chunks Writer2 will not write.
var (
	errEmptyUncompressed = errors.New("lzma: can't write empty uncompressed chunk")
	errEmptyCompressed   = errors.New("writeCompressedChunk: empty chunk")
)

// Writer2 supports the creation of an LZMA2 stream. But note that
// written data is buffered, so call Flush or Close to write data to the
// underlying writer. The Close method writes the end-of-stream marker
// to the stream. So you may be able to concatenate the output of two
// writers as long the output of the first writer has only been flushed
// but not closed.
type Writer2 struct {
	w io.Writer

	// err is the first error returned by the underlying writer or the
	// encoder. Once set, Write, Flush and Close return it: the chunk in
	// progress may have been partly written, so continuing would produce a
	// stream the encoder cannot vouch for — and, with the chunk accounting
	// left mid-flight, used to panic on the next Write.
	err error

	start   *state
	encoder *encoder

	cstate chunkState
	ctype  chunkType

	buf bytes.Buffer
	lbw limitedByteWriter
}

// NewWriter2 creates an LZMA2 chunk sequence writer. Options: WithDictSize,
// WithProperties.
func NewWriter2(lzma2 io.Writer, opts ...Option) (w *Writer2, err error) {
	o, err := apply(opts, optDictSize|optProperties, "NewWriter2")
	if err != nil {
		return nil, err
	}

	w = &Writer2{
		w:      lzma2,
		start:  newState(o.props),
		cstate: start,
		ctype:  start.defaultChunkType(),
	}
	w.buf.Grow(maxCompressed)
	w.lbw = limitedByteWriter{BW: &w.buf, N: maxCompressed}

	m, err := newHashTable(o.dictSize, 4)
	if err != nil {
		return nil, err
	}

	d, err := newEncoderDict(o.dictSize, lookaheadSize, m)
	if err != nil {
		return nil, err
	}

	w.encoder = newEncoder(&w.lbw, cloneState(w.start), d, 0)

	return w, nil
}

// errClosed indicates that the writer is closed.
var errClosed = errors.New("lzma: writer closed")

// Write writes data to the LZMA2 stream. Note that written data will be
// buffered.
// Use Flush or Close to ensure that data is written to the underlying
// writer.
//
//nolint:gocognit // one loop filling chunks and flushing each as it fills or hits the limit
func (w *Writer2) Write(p []byte) (n int, err error) {
	if w.err != nil {
		return 0, w.err
	}

	if w.cstate == stop {
		return 0, errClosed
	}
	defer func() { w.setErr(err) }()

	for n < len(p) {
		m := maxUncompressed - w.written()
		if m <= 0 {
			panic("lzma: maxUncompressed reached")
		}

		var q []byte
		if n+m < len(p) {
			q = p[n : n+m]
		} else {
			q = p[n:]
		}

		k, err := w.encoder.Write(q)

		n += k
		if err != nil && !errors.Is(err, errLimit) {
			return n, err
		}

		if errors.Is(err, errLimit) || k == m {
			if err = w.flushChunk(); err != nil {
				return n, err
			}
		}
	}

	return n, nil
}

// Flush writes all buffered data out to the underlying stream. This
// could result in multiple chunks to be created.
func (w *Writer2) Flush() (err error) {
	if w.err != nil {
		return w.err
	}

	if w.cstate == stop {
		return errClosed
	}
	defer func() { w.setErr(err) }()

	for w.written() > 0 {
		if err = w.flushChunk(); err != nil {
			return err
		}
	}

	return nil
}

// Close terminates the LZMA2 stream with an EOS chunk. After a failed Write
// or Flush it returns that error and writes nothing.
func (w *Writer2) Close() (err error) {
	if w.err != nil {
		return w.err
	}

	if w.cstate == stop {
		return errClosed
	}
	defer func() { w.setErr(err) }()

	if err = w.Flush(); err != nil {
		return err
	}
	// write zero byte EOS chunk
	if _, err = w.w.Write([]byte{0}); err != nil {
		return err
	}

	w.cstate = stop

	return nil
}

// setErr records the first failure so later calls report it instead of
// continuing on a writer whose state is unknown. errClosed is a state, not a
// failure, and is not recorded.
func (w *Writer2) setErr(err error) {
	if err != nil && w.err == nil && !errors.Is(err, errClosed) {
		w.err = err
	}
}

// flushChunk terminates the current chunk. The encoder will be reset
// to support the next chunk.
func (w *Writer2) flushChunk() error {
	if w.written() == 0 {
		return nil
	}

	var err error
	if err = w.encoder.Close(); err != nil {
		return err
	}

	if err = w.writeChunk(); err != nil {
		return err
	}

	w.buf.Reset()

	w.lbw.N = maxCompressed
	w.encoder.Reopen(&w.lbw)

	if err = w.cstate.next(w.ctype); err != nil {
		return err
	}

	w.ctype = w.cstate.defaultChunkType()
	// Snapshot into the existing state rather than cloning a new one: the
	// deepcopy methods reuse the probability arrays, so the per-chunk
	// snapshot costs no allocation.
	w.start.deepcopy(w.encoder.state)

	return nil
}

// writes a single chunk to the underlying writer.
func (w *Writer2) writeChunk() error {
	u := int(uncompressedHeaderLen + w.encoder.Compressed())
	c := headerLen(w.ctype) + w.buf.Len()
	// The uncompressed form replays the chunk's input from the encoder
	// dictionary. A dictionary smaller than the chunk has already dropped
	// part of that input, so the compressed form is the only one that can
	// be written, even when it is larger.
	if u < c && int64(w.encoder.dict.Len()) >= w.encoder.Compressed() {
		return w.writeUncompressedChunk()
	}

	return w.writeCompressedChunk()
}

// writeCompressedChunk writes a compressed chunk to the underlying
// writer.
func (w *Writer2) writeCompressedChunk() error {
	if w.ctype == cU || w.ctype == cUD {
		panic("chunk type uncompressed")
	}

	u := w.encoder.Compressed()
	if u <= 0 {
		return errEmptyCompressed
	}

	if u > maxUncompressed {
		panic("overrun of uncompressed data limit")
	}

	c := w.buf.Len()
	if c <= 0 {
		panic("no compressed data")
	}

	if c > maxCompressed {
		panic("overrun of compressed data limit")
	}

	header := chunkHeader{
		ctype:        w.ctype,
		uncompressed: uint32(u - 1),
		compressed:   uint16(c - 1),
		props:        w.encoder.state.Properties,
	}

	hdata, err := header.MarshalBinary()
	if err != nil {
		return err
	}

	if _, err = w.w.Write(hdata); err != nil {
		return err
	}

	_, err = io.Copy(w.w, &w.buf)

	return err
}

// writeUncompressedChunk writes an uncompressed chunk to the LZMA2
// stream.
func (w *Writer2) writeUncompressedChunk() error {
	u := w.encoder.Compressed()
	if u <= 0 {
		return errEmptyUncompressed
	}

	if u > maxUncompressed {
		panic("overrun of uncompressed data limit")
	}

	switch w.ctype {
	case cLRND:
		w.ctype = cUD
	default:
		w.ctype = cU
	}
	// Roll the encoder state back to the chunk start: the decoder never sees
	// the operations encoded into the discarded compressed form. Copy rather
	// than alias, so w.start stays a snapshot of its own.
	w.encoder.state.deepcopy(w.start)

	header := chunkHeader{
		ctype:        w.ctype,
		uncompressed: uint32(u - 1),
	}

	hdata, err := header.MarshalBinary()
	if err != nil {
		return err
	}

	if _, err = w.w.Write(hdata); err != nil {
		return err
	}

	_, err = w.encoder.dict.CopyN(w.w, int(u))

	return err
}

// written returns the number of bytes written to the current chunk.
func (w *Writer2) written() int {
	if w.encoder == nil {
		return 0
	}

	return int(w.encoder.Compressed()) + w.encoder.dict.Buffered()
}

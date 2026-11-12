// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
)

// Filter lists the writer refuses, and a size it cannot count.
var (
	errNoFilters      = errors.New("xz: no filters")
	errTooManyFilters = errors.New("xz: more than four filters")
	// A chain in the wrong order is one the format forbids, so a reader
	// meeting it refuses as the reference does, as unsupported.
	errFilterNotLast   = unsupportedf("xz: LZMA2 filter is not the last of the chain")
	errWrongLastFilter = unsupportedf("xz: last filter of the chain is not LZMA2")
	errCounterOverflow = errors.New("xz: counter overflow")
)

// maxInt64 defines the maximum 64-bit signed integer.
const maxInt64 = 1<<63 - 1

// verifyFilters checks the filter list for the length and the right
// sequence of filters.
func verifyFilters(f []filter) error {
	if len(f) == 0 {
		return errNoFilters
	}

	if len(f) > 4 {
		return errTooManyFilters
	}

	for _, g := range f[:len(f)-1] {
		if g.last() {
			return errFilterNotLast
		}
	}

	if !f[len(f)-1].last() {
		return errWrongLastFilter
	}

	return nil
}

// nopWCloser implements a WriteCloser with a Close method not doing
// anything.
type nopWCloser struct {
	io.Writer
}

// Close returns nil and doesn't do anything else.
func (nopWCloser) Close() error {
	return nil
}

// nopWriteCloser converts the Writer into a WriteCloser with a Close
// function that does nothing beside returning nil.
func nopWriteCloser(w io.Writer) io.WriteCloser {
	return nopWCloser{w}
}

// Writer compresses data written to it. It is an io.WriteCloser.
//
// The first error from the underlying writer is sticky: Write and Close
// return it from then on and write nothing further. The block in progress
// may have been partly written when it happened, so the stream cannot be
// completed; a caller that wants the data has to start over.
type Writer struct {
	opts options

	xz      io.Writer
	bw      *blockWriter
	newHash func() hash.Hash
	h       header
	index   []record
	closed  bool
	err     error
}

// NewWriter creates a writer that compresses what is written to it as an
// xz stream and writes the stream header to xz. Options: WithDictSize,
// WithProperties, WithBlockSize, WithCheck, WithFilters.
func NewWriter(xz io.Writer, opts ...Option) (w *Writer, err error) {
	o, err := apply(opts, writerOptions, "NewWriter")
	if err != nil {
		return nil, err
	}

	// The chain is built once here so a wrong filter is the constructor's
	// error, not the first block's.
	if _, err = o.chain(); err != nil {
		return nil, err
	}

	flags, err := o.check.flags()
	if err != nil {
		return nil, err
	}

	w = &Writer{
		opts:  o,
		xz:    xz,
		h:     header{flags},
		index: make([]record, 0, 4),
	}
	if w.newHash, err = newHashFunc(flags); err != nil {
		return nil, err
	}

	data, err := w.h.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("w.h.MarshalBinary(): error %w", err)
	}

	if _, err = xz.Write(data); err != nil {
		return nil, err
	}

	if err = w.newBlockWriter(); err != nil {
		return nil, err
	}

	return w, nil
}

// newFilterWriteCloser converts a filter list into a WriteCloser that
// can be used by a blockWriter.
func (o *options) newFilterWriteCloser(w io.Writer, f []filter) (fw io.WriteCloser, err error) {
	if err = verifyFilters(f); err != nil {
		return nil, err
	}

	fw = nopWriteCloser(w)
	for _, v := range slices.Backward(f) {
		fw, err = v.writeCloser(fw, o)
		if err != nil {
			return nil, err
		}
	}

	return fw, nil
}

// Write compresses the uncompressed data provided.
func (w *Writer) Write(p []byte) (n int, err error) {
	if w.err != nil {
		return 0, w.err
	}

	if w.closed {
		return 0, errClosed
	}

	for {
		k, err := w.bw.Write(p[n:])

		n += k
		if !errors.Is(err, errNoSpace) {
			return n, w.setErr(err)
		}

		if err = w.closeBlockWriter(); err != nil {
			return n, w.setErr(err)
		}

		if err = w.newBlockWriter(); err != nil {
			return n, w.setErr(err)
		}
	}
}

// Close closes the writer and adds the footer to the Writer. Close
// doesn't close the underlying writer. After a failed Write it returns that
// error and writes nothing; a second Close writes nothing and returns an
// error matching ErrClosed.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}

	if w.closed {
		return errClosed
	}

	w.closed = true

	var err error
	if err = w.closeBlockWriter(); err != nil {
		return w.setErr(err)
	}

	f := footer{flags: w.h.flags}
	if f.indexSize, err = writeIndex(w.xz, w.index); err != nil {
		return w.setErr(err)
	}

	data, err := f.MarshalBinary()
	if err != nil {
		return w.setErr(err)
	}

	if _, err = w.xz.Write(data); err != nil {
		return w.setErr(err)
	}

	return nil
}

// setErr records the first failure. Later calls report it rather than
// continue on a block writer whose state is unknown.
func (w *Writer) setErr(err error) error {
	if err != nil && w.err == nil {
		w.err = err
	}

	return err
}

// closeBlockWriter closes a block writer and records the sizes in the
// index.
func (w *Writer) closeBlockWriter() error {
	var err error
	if err = w.bw.Close(); err != nil {
		return err
	}

	w.index = append(w.index, w.bw.record())

	return nil
}

// newBlockWriter creates a new block writer writes the header out.
func (w *Writer) newBlockWriter() error {
	var err error

	w.bw, err = w.opts.newBlockWriter(w.xz, w.newHash())
	if err != nil {
		return err
	}

	return w.bw.writeHeader(w.xz)
}

// countingWriter is a writer that counts all data written to it.
type countingWriter struct {
	w io.Writer
	n int64
}

// Write writes data to the countingWriter.
func (cw *countingWriter) Write(p []byte) (n int, err error) {
	n, err = cw.w.Write(p)

	cw.n += int64(n)
	if err == nil && cw.n < 0 {
		return n, errCounterOverflow
	}

	return n, err
}

// blockWriter is writes a single block.
type blockWriter struct {
	cxz countingWriter
	// mw combines io.WriteCloser w and the hash.
	mw        io.Writer
	w         io.WriteCloser
	n         int64
	blockSize int64
	closed    bool
	headerLen int

	filters []filter
	hash    hash.Hash
}

// newBlockWriter creates a new block writer.
func (o *options) newBlockWriter(xz io.Writer, check hash.Hash) (bw *blockWriter, err error) {
	chain, err := o.chain()
	if err != nil {
		return nil, err
	}

	bw = &blockWriter{
		cxz:       countingWriter{w: xz},
		blockSize: o.blockSize,
		filters:   chain,
		hash:      check,
	}

	bw.w, err = o.newFilterWriteCloser(&bw.cxz, bw.filters)
	if err != nil {
		return nil, err
	}

	if bw.hash.Size() != 0 {
		bw.mw = io.MultiWriter(bw.w, bw.hash)
	} else {
		bw.mw = bw.w
	}

	return bw, nil
}

// errClosed matches ErrClosed so callers can recognise it with errors.Is.
var errClosed = &kindError{msg: "xz: writer already closed", kind: ErrClosed}

var errNoSpace = errors.New("xz: no space")

// Write writes uncompressed data to the block writer.
func (bw *blockWriter) Write(p []byte) (n int, err error) {
	if bw.closed {
		return 0, errClosed
	}

	t := bw.blockSize - bw.n
	if int64(len(p)) > t {
		err = errNoSpace
		p = p[:t]
	}

	var werr error

	n, werr = bw.mw.Write(p)

	bw.n += int64(n)
	if werr != nil {
		return n, werr
	}

	return n, err
}

// Close closes the writer.
func (bw *blockWriter) Close() error {
	if bw.closed {
		return errClosed
	}

	bw.closed = true
	if err := bw.w.Close(); err != nil {
		return err
	}

	s := bw.hash.Size()
	k := padLen(bw.cxz.n)
	p := make([]byte, k+s)
	bw.hash.Sum(p[k:k])

	if _, err := bw.cxz.w.Write(p); err != nil {
		return err
	}

	return nil
}

// record returns the index record for the current block. Call Close before
// calling this method.
func (bw *blockWriter) record() record {
	return record{bw.unpaddedSize(), bw.uncompressedSize()}
}

// unpaddedSize returns the sum of the header length, the uncompressed
// size of the block and the hash size.
func (bw *blockWriter) unpaddedSize() int64 {
	if bw.headerLen <= 0 {
		panic("xz: block header not written")
	}

	n := int64(bw.headerLen)
	n += bw.compressedSize()
	n += int64(bw.hash.Size())

	return n
}

// uncompressedSize returns the number of data written to the
// blockWriter.
func (bw *blockWriter) uncompressedSize() int64 {
	return bw.n
}

// compressed size returns the amount of data written to the underlying
// stream.
func (bw *blockWriter) compressedSize() int64 {
	return bw.cxz.n
}

// writeHeader writes the header before the block's data, so the sizes are
// not yet known and stay out of it; the index carries them.
func (bw *blockWriter) writeHeader(w io.Writer) error {
	h := blockHeader{
		compressedSize:   -1,
		uncompressedSize: -1,
		filters:          bw.filters,
	}

	data, err := h.MarshalBinary()
	if err != nil {
		return err
	}

	if _, err = w.Write(data); err != nil {
		return err
	}

	bw.headerLen = len(data)

	return nil
}

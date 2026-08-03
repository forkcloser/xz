// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package xz supports the compression and decompression of xz files. It
// supports version 1.0.4 of the specification without the non-LZMA2
// filters. See http://tukaani.org/xz/xz-file-format-1.0.4.txt
package xz

import (
	"bytes"
	"errors"
	"hash"
	"io"
	"slices"

	"github.com/forkcloser/xz/internal/xlog"
	"github.com/forkcloser/xz/lzma"
)

// ReaderConfig defines the parameters for the xz reader. The
// SingleStream parameter requests the reader to assume that the
// underlying stream contains only a single stream.
//
// DictCap is the smallest dictionary the reader will use. A block whose
// header asks for more gets what it asks for, so this raises the floor rather
// than capping memory; the dictionary itself is grown on demand and costs only
// what the stream actually decodes.
type ReaderConfig struct {
	DictCap      int
	SingleStream bool
}

// Verify checks the reader parameters for validity and replaces zero values
// with their defaults, so afterwards DictCap holds the value that will
// actually be used.
func (c *ReaderConfig) Verify() error {
	if c == nil {
		return errors.New("xz: reader parameters are nil")
	}
	lc := lzma.Reader2Config{DictCap: c.DictCap}
	if err := lc.Verify(); err != nil {
		return err
	}
	c.DictCap = lc.DictCap
	return nil
}

// Reader supports the reading of one or multiple xz streams.
type Reader struct {
	ReaderConfig

	xz io.Reader
	sr *streamReader
}

// streamReader decodes a single xz stream
type streamReader struct {
	ReaderConfig

	xz      io.Reader
	br      *blockReader
	newHash func() hash.Hash
	h       header
	index   []record
}

// NewReader creates a new xz reader using the default parameters.
// The function reads and checks the header of the first XZ stream. The
// reader will process multiple streams including padding.
func NewReader(xz io.Reader) (r *Reader, err error) {
	return ReaderConfig{}.NewReader(xz)
}

// NewReader creates an xz stream reader. The created reader will be
// able to process multiple streams and padding unless a SingleStream
// has been set in the reader configuration c.
func (c ReaderConfig) NewReader(xz io.Reader) (r *Reader, err error) {
	if err = c.Verify(); err != nil {
		return nil, err
	}
	r = &Reader{
		ReaderConfig: c,
		xz:           xz,
	}
	if r.sr, err = c.newStreamReader(xz); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return r, nil
}

var errUnexpectedData = corruptf("xz: unexpected data after stream")

// Read reads uncompressed data from the stream.
//
// As the io.Reader contract permits, Read can return decoded data together
// with an error. When the error reports corrupt or truncated input, the
// trailing bytes of that data may stem from decoder state that was fed input
// past the point of corruption; discard data received alongside such an error
// rather than treating it as a correct prefix of the stream.
func (r *Reader) Read(p []byte) (n int, err error) {
	for n < len(p) {
		if r.sr == nil {
			if r.SingleStream {
				data := make([]byte, 1)
				_, err = io.ReadFull(r.xz, data)
				if !errors.Is(err, io.EOF) {
					return n, errUnexpectedData
				}
				return n, io.EOF
			}
			for {
				r.sr, err = r.newStreamReader(r.xz)
				if !errors.Is(err, errPadding) {
					break
				}
			}
			if err != nil {
				return n, err
			}
		}
		k, err := r.sr.Read(p[n:])
		n += k
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.sr = nil
				continue
			}
			return n, err
		}
	}
	return n, nil
}

var errPadding = errors.New("xz: padding (4 zero bytes) encountered")

// newStreamReader creates a new xz stream reader using the given configuration
// parameters. NewReader reads and checks the header of the xz stream.
func (c ReaderConfig) newStreamReader(xz io.Reader) (r *streamReader, err error) {
	if err = c.Verify(); err != nil {
		return nil, err
	}
	data := make([]byte, HeaderLen)
	if _, err := io.ReadFull(xz, data[:4]); err != nil {
		return nil, err
	}
	if bytes.Equal(data[:4], []byte{0, 0, 0, 0}) {
		return nil, errPadding
	}
	if _, err = io.ReadFull(xz, data[4:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	r = &streamReader{
		ReaderConfig: c,
		xz:           xz,
		index:        make([]record, 0, 4),
	}
	if err = r.h.UnmarshalBinary(data); err != nil {
		return nil, err
	}
	xlog.Debugf("xz header %s", r.h)
	if r.newHash, err = newHashFunc(r.h.flags); err != nil {
		return nil, err
	}
	return r, nil
}

// readTail reads the index body and the xz footer.
func (r *streamReader) readTail() error {
	index, n, err := readIndexBody(r.xz, len(r.index))
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return err
	}

	for i, rec := range r.index {
		if rec != index[i] {
			return corruptf("xz: record %d is %v; want %v",
				i, rec, index[i])
		}
	}

	p := make([]byte, footerLen)
	if _, err = io.ReadFull(r.xz, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	var f footer
	if err = f.UnmarshalBinary(p); err != nil {
		return err
	}
	xlog.Debugf("xz footer %s", f)
	if f.flags != r.h.flags {
		return corruptf("xz: footer flags incorrect")
	}
	if f.indexSize != n+1 {
		return corruptf("xz: index size in footer wrong")
	}
	return nil
}

// Read reads actual data from the xz stream.
func (r *streamReader) Read(p []byte) (n int, err error) {
	for n < len(p) {
		if r.br == nil {
			bh, hlen, err := readBlockHeader(r.xz)
			if err != nil {
				if errors.Is(err, errIndexIndicator) {
					if err = r.readTail(); err != nil {
						return n, err
					}
					return n, io.EOF
				}
				if errors.Is(err, io.EOF) {
					// Every xz stream ends with an index and a footer, even
					// one with no blocks. Running out of input where the next
					// block header or the index indicator should be means the
					// stream was cut short, not that it ended. Reporting EOF
					// here made the reader hand back a truncated file as a
					// complete one.
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
			xlog.Debugf("block %v", *bh)
			r.br, err = r.newBlockReader(r.xz, bh,
				hlen, r.newHash())
			if err != nil {
				return n, err
			}
		}
		k, err := r.br.Read(p[n:])
		n += k
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.index = append(r.index, r.br.record())
				r.br = nil
			} else {
				return n, err
			}
		}
	}
	return n, nil
}

// countingReader is a reader that counts the bytes read.
type countingReader struct {
	r io.Reader
	n int64
}

// Read reads data from the wrapped reader and adds it to the n field.
func (lr *countingReader) Read(p []byte) (n int, err error) {
	n, err = lr.r.Read(p)
	lr.n += int64(n)
	return n, err
}

// blockReader supports the reading of a block.
type blockReader struct {
	lxz       countingReader
	header    *blockHeader
	headerLen int
	n         int64
	hash      hash.Hash
	r         io.Reader
}

// newBlockReader creates a new block reader.
func (c *ReaderConfig) newBlockReader(xz io.Reader, h *blockHeader,
	hlen int, hash hash.Hash) (br *blockReader, err error) {

	br = &blockReader{
		lxz:       countingReader{r: xz},
		header:    h,
		headerLen: hlen,
		hash:      hash,
	}

	fr, err := c.newFilterReader(&br.lxz, h.filters)
	if err != nil {
		return nil, err
	}
	if br.hash.Size() != 0 {
		br.r = io.TeeReader(fr, br.hash)
	} else {
		br.r = fr
	}

	return br, nil
}

// uncompressedSize returns the uncompressed size of the block.
func (br *blockReader) uncompressedSize() int64 {
	return br.n
}

// compressedSize returns the compressed size of the block.
func (br *blockReader) compressedSize() int64 {
	return br.lxz.n
}

// unpaddedSize computes the unpadded size for the block.
func (br *blockReader) unpaddedSize() int64 {
	n := int64(br.headerLen)
	n += br.compressedSize()
	n += int64(br.hash.Size())
	return n
}

// record returns the index record for the current block.
func (br *blockReader) record() record {
	return record{br.unpaddedSize(), br.uncompressedSize()}
}

// Read reads data from the block.
func (br *blockReader) Read(p []byte) (n int, err error) {
	n, err = br.r.Read(p)
	br.n += int64(n)

	u := br.header.uncompressedSize
	if u >= 0 && br.uncompressedSize() > u {
		return n, corruptf("xz: wrong uncompressed size for block")
	}
	c := br.header.compressedSize
	if c >= 0 && br.compressedSize() > c {
		return n, corruptf("xz: wrong compressed size for block")
	}
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	if br.uncompressedSize() < u || br.compressedSize() < c {
		return n, io.ErrUnexpectedEOF
	}

	s := br.hash.Size()
	k := padLen(br.lxz.n)
	q := make([]byte, k+s, k+2*s)
	if _, err = io.ReadFull(br.lxz.r, q); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return n, err
	}
	if !allZeros(q[:k]) {
		return n, corruptf("xz: non-zero block padding")
	}
	checkSum := q[k:]
	computedSum := br.hash.Sum(checkSum[s:])
	if !bytes.Equal(checkSum, computedSum) {
		return n, corruptf("xz: checksum error for block")
	}
	return n, io.EOF
}

func (c *ReaderConfig) newFilterReader(r io.Reader, f []filter) (fr io.Reader,
	err error) {

	if err = verifyFilters(f); err != nil {
		return nil, err
	}

	fr = r
	for _, v := range slices.Backward(f) {
		fr, err = v.reader(fr, c)
		if err != nil {
			return nil, err
		}
	}
	return fr, nil
}

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package xz supports the compression and decompression of xz files.
//
// The container format is the one described by the xz file format
// specification (https://xz.tukaani.org/format/xz-file-format.txt): stream
// header and footer, blocks, index, stream padding and multi-stream files,
// with the None, CRC32, CRC64 and SHA-256 checks, and every filter the
// format defines: LZMA2, the eight branch/call/jump filters (x86, PowerPC,
// IA-64, ARM, ARM Thumb, SPARC, ARM64, RISC-V) and delta, in chains of up
// to four. The writer applies the BCJ and delta filters on request
// (WithFilters).
//
// Errors are classified: input that is not valid xz matches ErrCorrupt,
// input this package cannot handle matches ErrUnsupported, a stream cut
// short is io.ErrUnexpectedEOF, and an I/O error from the underlying reader
// is passed through untouched.
package xz

import (
	"bytes"
	"errors"
	"hash"
	"io"
	"slices"
)

// Reader decodes an xz file: one stream or several, concatenated with or
// without padding between them.
//
// With WithWorkers above one, blocks decode concurrently when the input is
// an io.ReaderAt whose start and size the reader can learn (an io.Seeker,
// as a file or a bytes.Reader is), through the file's indexes; a stream
// decodes in order. A reader with workers must be closed when abandoned
// before io.EOF. The decoded stream is presented in order through Read and
// WriteTo.
type Reader struct {
	opts options

	xz io.Reader
	sr *streamReader
	// lz carries the LZMA2 reader across the blocks and streams of the file.
	lz lzma2Cache
	// par decodes in its place when the input allows concurrent decoding.
	par *parallelReader
}

// streamReader decodes a single xz stream.
type streamReader struct {
	opts *options

	xz      io.Reader
	br      *blockReader
	newHash func() hash.Hash
	h       header
	index   []record
	lz      *lzma2Cache
	hdr     [maxBlockHeaderLen]byte
}

// NewReader creates a reader for the xz file read from xz, reading and
// checking the first stream header. Options: WithMaxDictSize,
// WithSingleStream, WithWorkers.
func NewReader(xz io.Reader, opts ...Option) (r *Reader, err error) {
	o, err := apply(opts, readerOptions, "NewReader")
	if err != nil {
		return nil, err
	}

	r = &Reader{opts: o, xz: xz}

	if o.workers > 1 {
		if section, ok := randomAccess(xz); ok {
			r.par, err = newParallelReader(section, section.Size(), o)
			if err != nil {
				return nil, err
			}

			return r, nil
		}
	}

	if r.sr, err = o.newStreamReader(xz, &r.lz); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			err = io.ErrUnexpectedEOF
		case errors.Is(err, errPadding):
			// Padding is between streams or after the last; a file that
			// opens with it holds no stream where one must be.
			err = corruptf("xz: stream padding before the first stream")
		}

		return nil, err
	}

	return r, nil
}

// randomAccess returns r as a section from its current position to its
// end when it can be read by offset: an io.ReaderAt that is also an
// io.Seeker, which tells the position and the size. The position is left
// where it was.
func randomAccess(r io.Reader) (*io.SectionReader, bool) {
	rs, ok := r.(interface {
		io.ReaderAt
		io.Seeker
	})
	if !ok {
		return nil, false
	}

	start, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, false
	}

	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, false
	}

	if _, err = rs.Seek(start, io.SeekStart); err != nil {
		return nil, false
	}

	return io.NewSectionReader(rs, start, end-start), true
}

// Close releases the workers of a reader that decodes concurrently; it is
// required when such a reader is abandoned before io.EOF, and may be
// called after. Close may be called from another goroutine, and then
// cancels a Read waiting on a block. On a reader without workers it does
// nothing.
func (r *Reader) Close() error {
	if r.par != nil {
		return r.par.Close()
	}

	return nil
}

// WriteTo writes the whole remaining uncompressed data to w.
func (r *Reader) WriteTo(w io.Writer) (int64, error) {
	if r.par != nil {
		return r.par.WriteTo(w)
	}

	buf := make([]byte, 32<<10) //nolint:mnd // the copy buffer, io.Copy's size

	var n int64

	for {
		k, err := r.Read(buf)
		if k > 0 {
			written, werr := w.Write(buf[:k])
			n += int64(written)

			if werr != nil {
				return n, werr
			}

			if written < k {
				return n, io.ErrShortWrite
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}

			return n, err
		}
	}
}

var errUnexpectedData = corruptf("xz: unexpected data after stream")

// Read reads uncompressed data from the stream.
//
// As the io.Reader contract permits, Read can return decoded data together
// with an error. When the error reports corrupt or truncated input, the
// trailing bytes of that data may stem from decoder state that was fed input
// past the point of corruption; discard data received alongside such an error
// rather than treating it as a correct prefix of the stream.
//
//nolint:gocognit // the loop over streams: how a single stream ends and the padding between streams, each commented in place
func (r *Reader) Read(p []byte) (n int, err error) {
	if r.par != nil {
		return r.par.Read(p)
	}

	for n < len(p) {
		if r.sr == nil {
			if r.opts.singleStream {
				// One byte past the stream decides: nothing there is the
				// clean end, a byte is trailing data, and a failing read
				// is the transport's problem — not the file's — so the
				// reader's error goes back as it came.
				data := make([]byte, 1)

				var k int

				k, err = r.xz.Read(data)
				switch {
				case k > 0:
					return n, errUnexpectedData
				case err == nil:
					// A Read that returns (0, nil) is allowed and says
					// nothing either way; ask again.
					continue
				case errors.Is(err, io.EOF):
					return n, io.EOF
				default:
					return n, err
				}
			}

			for {
				r.sr, err = r.opts.newStreamReader(r.xz, &r.lz)
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
// parameters. NewReader reads and checks the header of the xz stream. A
// non-nil cache lets the stream's blocks reuse one LZMA2 reader.
func (o *options) newStreamReader(xz io.Reader, cache *lzma2Cache) (r *streamReader, err error) {
	data := make([]byte, HeaderLen)

	k, err := io.ReadFull(xz, data[:4])
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) && allZeros(data[:k]) {
			// Padding comes in whole groups of four zero bytes; fewer at
			// the end is the file's fault, where a non-zero byte starts a
			// stream header and running out inside it is the input ending.
			return nil, corruptf("xz: stream padding is not a multiple of four bytes")
		}

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
		opts:  o,
		xz:    xz,
		index: make([]record, 0, 4),
		lz:    cache,
	}
	if err = r.h.UnmarshalBinary(data); err != nil {
		return nil, err
	}

	if r.newHash, err = newHashFunc(r.h.flags); err != nil {
		return nil, err
	}

	return r, nil
}

// Read reads actual data from the xz stream.
//
//nolint:gocognit // the loop over blocks: the next block, or the index and the tail, each case commented in place
func (r *streamReader) Read(p []byte) (n int, err error) {
	for n < len(p) {
		if r.br == nil {
			bh, hlen, err := readBlockHeader(r.xz, &r.hdr)
			if err != nil {
				if errors.Is(err, errIndexIndicator) {
					if err = r.readTail(); err != nil {
						return n, err
					}

					return n, io.EOF
				}

				if errors.Is(err, io.EOF) {
					// Every xz stream ends with an index and a footer, even
					// one with no blocks: running out of input where the
					// next block header or the index indicator should be is
					// the stream cut short, never its end.
					err = io.ErrUnexpectedEOF
				}

				return n, err
			}

			r.br, err = r.opts.newBlockReader(r.xz, bh,
				hlen, r.newHash(), r.lz)
			if err != nil {
				return n, err
			}
		}

		k, err := r.br.Read(p[n:])
		n += k

		if err != nil {
			if !errors.Is(err, io.EOF) {
				return n, err
			}

			r.index = append(r.index, r.br.record())
			r.br = nil
		}
	}

	return n, nil
}

// readTail reads the index body and the xz footer.
func (r *streamReader) readTail() error {
	index, n, err := readIndexBody(r.xz, len(r.index), -1)
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

	if f.flags != r.h.flags {
		return corruptf("xz: footer flags incorrect")
	}

	if f.indexSize != n+1 {
		return corruptf("xz: index size in footer wrong")
	}

	return nil
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

// newBlockReader creates a new block reader. A non-nil cache lets the block
// reuse the LZMA2 reader of the previous block.
func (o *options) newBlockReader(xz io.Reader, h *blockHeader,
	hlen int, check hash.Hash, cache *lzma2Cache,
) (br *blockReader, err error) {
	br = &blockReader{
		lxz:       countingReader{r: xz},
		header:    h,
		headerLen: hlen,
		hash:      check,
	}

	fr, err := o.newFilterReader(&br.lxz, h.filters, cache)
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

// Read reads data from the block.
func (br *blockReader) Read(p []byte) (n int, err error) {
	n, err = br.r.Read(p)
	br.n += int64(n)
	// The filter chain speaks the lzma package's vocabulary; translate it
	// before anything else looks at the error.
	err = classify(err)

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

	if br.uncompressedSize() < u {
		return n, corruptf("xz: wrong uncompressed size for block")
	}

	if br.compressedSize() < c {
		return n, corruptf("xz: wrong compressed size for block")
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

// record returns the index record for the current block.
func (br *blockReader) record() record {
	return record{br.unpaddedSize(), br.uncompressedSize()}
}

// unpaddedSize computes the unpadded size for the block.
func (br *blockReader) unpaddedSize() int64 {
	n := int64(br.headerLen)
	n += br.compressedSize()
	n += int64(br.hash.Size())

	return n
}

// compressedSize returns the compressed size of the block.
func (br *blockReader) compressedSize() int64 {
	return br.lxz.n
}

// uncompressedSize returns the uncompressed size of the block.
func (br *blockReader) uncompressedSize() int64 {
	return br.n
}

func (o *options) newFilterReader(r io.Reader, f []filter,
	cache *lzma2Cache,
) (fr io.Reader, err error) {
	if err = verifyFilters(f); err != nil {
		return nil, err
	}

	fr = r
	for _, v := range slices.Backward(f) {
		fr, err = v.reader(fr, o, cache)
		if err != nil {
			return nil, err
		}
	}

	return fr, nil
}

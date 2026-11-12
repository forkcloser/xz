// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lzma supports the decoding and encoding of LZMA streams.
// Reader and Writer support the classic LZMA format. Reader2 and
// Writer2 support the decoding and encoding of LZMA2 streams.
//
// The package is written completely in Go and does not rely on any external
// library.
package lzma

import (
	"bufio"
	"errors"
	"io"

	"github.com/forkcloser/xz/v2/internal/bytereader"
)

// Reader provides a reader for LZMA files or streams.
//
// # Security concerns
//
// Note that LZMA format doesn't support a magic marker in the header. So
// [NewReader] cannot determine whether it reads the actual header. For instance
// the LZMA stream might have a zero byte in front of the reader, leading to
// larger dictionary sizes and file sizes. The code will detect later that there
// are problems with the stream, but the dictionary has already been allocated
// and this might consume a lot of memory.
//
// What bounds that:
//   - WithMaxDictSize is a limit on the dictionary size a header may declare;
//     a header asking for more is refused with ErrUnsupported. The default
//     is 2 GiB-1.
//   - The dictionary is allocated as the data needs it, never the declared
//     size up front, and no larger than the stream's own size when the
//     header states one.
//   - Header reports what the header declared, so a caller can choose a
//     limit for the files it meets.
//   - Streams are supported up to a pebibyte (1024^5).
//
// # Input buffering
//
// The decoder pulls compressed bytes one at a time. If the input is an
// [io.ByteReader] — a [bufio.Reader], [bytes.Reader] or [strings.Reader],
// say — it is used as is and the reader consumes exactly the bytes of the
// stream, leaving anything after it unread. Any other [io.Reader] is wrapped
// in a [bufio.Reader] first: without that, a plain [os.File] costs one system
// call per compressed byte and decodes an order of magnitude slower. The
// wrapper may read past the end of the stream, so a caller who needs the
// input positioned exactly at the stream's end should pass an io.ByteReader
// of its own.
type Reader struct {
	lzma   io.Reader
	header Header
	// headerOrig stores the original header read from the stream.
	headerOrig Header
	d          *decoder
}

// We support only files not larger than 1 << 50 bytes (a pebibyte, 1024^5).
const maxStreamSize = 1 << 50

// NewReader creates a reader for an LZMA stream in the classic format,
// reading and checking its header. Options: WithMaxDictSize.
func NewReader(lzma io.Reader, opts ...Option) (r *Reader, err error) {
	o, err := apply(opts, optMaxDictSize, "NewReader")
	if err != nil {
		return nil, err
	}

	// The decoder reads compressed bytes one at a time through an
	// io.ByteReader. Give a plain reader one buffered front-end for the
	// header and the stream alike; an input that already is a ByteReader
	// is left alone so its exact position stays under the caller's control
	// (see the Reader documentation).
	if _, ok := lzma.(io.ByteReader); !ok {
		lzma = bufio.NewReader(lzma)
	}

	data := make([]byte, HeaderLen)
	if _, err = io.ReadFull(lzma, data); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}

		return nil, err
	}

	r = &Reader{lzma: lzma}
	if err = r.header.unmarshalBinary(data); err != nil {
		return nil, err
	}

	r.headerOrig = r.header

	dictSize := int64(r.header.DictSize)
	if o.maxDictSize < dictSize {
		return nil, unsupportedf(
			"lzma: header dictionary size %d exceeds the limit %d",
			dictSize, o.maxDictSize,
		)
	}

	if dictSize < MinDictSize {
		dictSize = MinDictSize
	}
	// The dictionary is what the file states and no larger: the limit is a
	// ceiling, never a size to grow to.
	size := r.header.Size
	if size >= 0 && size < dictSize {
		dictSize = size
	}
	// Protect against modified or malicious headers.
	if size > maxStreamSize {
		return nil, unsupportedf(
			"lzma: stream size %d exceeds a pebibyte (1024^5)",
			size,
		)
	}

	if dictSize < MinDictSize {
		dictSize = MinDictSize
	}

	// #nosec G115 -- the header's uint32, raised at most to MinDictSize
	r.header.DictSize = uint32(dictSize)

	state := newState(r.header.Properties)

	dict, err := newDecoderDict(int(dictSize))
	if err != nil {
		return nil, err
	}

	r.d, err = newDecoder(bytereader.New(lzma), state, dict, r.header.Size)
	if err != nil {
		return nil, err
	}

	return r, nil
}

// Header returns the header as read from the LZMA stream. It is intended to
// allow the user to understand what parameters are typically provided in the
// headers of the LZMA files and choose a WithMaxDictSize accordingly.
func (r *Reader) Header() (h Header, ok bool) {
	return r.headerOrig, r.d != nil
}

// Read returns uncompressed data.
func (r *Reader) Read(p []byte) (n int, err error) {
	return r.d.Read(p)
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/forkcloser/xz/internal/filters"
)

// The non-LZMA2 filters of a block header, as the reader decodes them. The
// writer does not emit them: writeCloser refuses, so a filter list with one
// of them is a reader's.

// bcjFilter is one of the branch/call/jump filters in a block header, with
// the start offset of its properties.
type bcjFilter struct {
	fid         uint64
	startOffset uint32
}

// bcjPropsLen is the length of a BCJ filter's properties when it has any:
// the 32-bit little endian start offset.
const bcjPropsLen = 4

// errWriteFilter is the writer's answer to a filter it does not encode.
var errWriteFilter = errors.New("xz: filter not supported by the writer")

// MarshalBinary writes the filter as the block header carries it: the id,
// the properties size and the start offset when it is not zero.
func (f bcjFilter) MarshalBinary() ([]byte, error) {
	// #nosec G115 -- a BCJ filter id is below 0x0C
	id := byte(f.fid)
	if f.startOffset == 0 {
		return []byte{id, 0}, nil
	}

	data := make([]byte, 2+bcjPropsLen)
	data[0] = id
	data[1] = bcjPropsLen
	binary.LittleEndian.PutUint32(data[2:], f.startOffset)

	return data, nil
}

// UnmarshalBinary reads the filter from the id, the properties size and the
// properties: none, or a start offset the filter's alignment divides.
func (f *bcjFilter) UnmarshalBinary(data []byte) error {
	if len(data) < 2 || !filters.IsBCJ(uint64(data[0])) {
		return corruptf("xz: wrong BCJ filter data")
	}

	f.fid = uint64(data[0])

	switch int(data[1]) {
	case 0:
		if len(data) != 2 {
			return corruptf("xz: BCJ filter properties present with size 0")
		}

		f.startOffset = 0
	case bcjPropsLen:
		if len(data) != 2+bcjPropsLen {
			return corruptf("xz: BCJ filter properties truncated")
		}

		f.startOffset = binary.LittleEndian.Uint32(data[2:])
	default:
		return corruptf("xz: BCJ filter properties size %d, want 0 or %d", data[1], bcjPropsLen)
	}

	if _, err := filters.NewBCJ(f.fid, f.startOffset, false); err != nil {
		// The spec requires an error for a misaligned start offset and
		// the reference reports it as unsupported options.
		return unsupportedf("xz: %v", err)
	}

	return nil
}

func (f bcjFilter) id() uint64 { return f.fid }

func (f bcjFilter) reader(r io.Reader, _ *ReaderConfig, _ *lzma2Cache) (io.Reader, error) {
	b, err := filters.NewBCJ(f.fid, f.startOffset, false)
	if err != nil {
		return nil, unsupportedf("xz: %v", err)
	}

	return &bcjReader{r: r, bcj: b, buf: make([]byte, bcjBufSize)}, nil
}

func (bcjFilter) writeCloser(io.WriteCloser, *WriterConfig) (io.WriteCloser, error) {
	return nil, errWriteFilter
}

func (bcjFilter) last() bool { return false }

// bcjBufSize is the bcjReader's buffer: what one Read of the next filter
// fills before the converter runs over it.
const bcjBufSize = 32 << 10

// bcjReader applies a BCJ converter to what it reads from the next filter.
// The converter leaves an unconverted tail at the end of each buffer, an
// instruction it has not seen the whole of; the tail moves to the front
// and is converted with the next bytes. At the end of the data the tail is
// the data.
type bcjReader struct {
	r   io.Reader
	bcj *filters.BCJ
	buf []byte
	// buf[start:done] is converted and not yet handed out; buf[done:end]
	// is the unconverted tail.
	start, done, end int
	eof              bool
}

func (br *bcjReader) Read(p []byte) (int, error) {
	for br.start == br.done {
		if br.eof {
			return 0, io.EOF
		}

		if err := br.fill(); err != nil {
			return 0, err
		}
	}

	n := copy(p, br.buf[br.start:br.done])
	br.start += n

	return n, nil
}

// fill moves the unconverted tail to the front, reads after it and runs
// the converter. At the end of the input the tail becomes output.
func (br *bcjReader) fill() error {
	br.end = copy(br.buf, br.buf[br.done:br.end])
	br.start, br.done = 0, 0

	n, err := br.r.Read(br.buf[br.end:])
	br.end += n

	switch {
	case n > 0:
		br.done = br.bcj.Code(br.buf[:br.end])
	case errors.Is(err, io.EOF):
		br.eof = true
		br.done = br.end
		err = nil
	}

	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

// deltaFilter is the delta filter in a block header, with its distance.
type deltaFilter struct {
	distance int
}

// MarshalBinary writes the filter as the block header carries it: the id,
// a one-byte properties size and the distance less one.
func (f deltaFilter) MarshalBinary() ([]byte, error) {
	// #nosec G115 -- the distance is 1 to 256
	return []byte{filters.DeltaID, 1, byte(f.distance - 1)}, nil
}

// UnmarshalBinary reads the filter from the id, the properties size and the
// distance byte.
func (f *deltaFilter) UnmarshalBinary(data []byte) error {
	if len(data) != 3 || data[0] != filters.DeltaID || data[1] != 1 {
		return corruptf("xz: wrong delta filter data")
	}

	f.distance = int(data[2]) + 1

	return nil
}

func (deltaFilter) id() uint64 { return filters.DeltaID }

func (f deltaFilter) reader(r io.Reader, _ *ReaderConfig, _ *lzma2Cache) (io.Reader, error) {
	d, err := filters.NewDelta(f.distance)
	if err != nil {
		return nil, corruptf("xz: %v", err)
	}

	return &deltaReader{r: r, delta: d}, nil
}

func (deltaFilter) writeCloser(io.WriteCloser, *WriterConfig) (io.WriteCloser, error) {
	return nil, errWriteFilter
}

func (deltaFilter) last() bool { return false }

// deltaReader applies the delta decoder to what it reads from the next
// filter, byte for byte, so it needs no buffer of its own.
type deltaReader struct {
	r     io.Reader
	delta *filters.Delta
}

func (dr *deltaReader) Read(p []byte) (int, error) {
	n, err := dr.r.Read(p)
	dr.delta.Decode(p[:n])

	return n, err
}

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"io"

	"github.com/forkcloser/xz/v2/internal/dictsize"
	"github.com/forkcloser/xz/v2/lzma"
)

// LZMA filter constants.
const (
	lzmaFilterID  = 0x21
	lzmaFilterLen = 3
)

// lzmaFilter declares the LZMA2 filter information stored in an xz
// block header.
type lzmaFilter struct {
	dictSize int64
}

// MarshalBinary converts the lzmaFilter in its encoded representation.
func (f lzmaFilter) MarshalBinary() (data []byte, err error) {
	c := dictsize.Encode(f.dictSize)
	return []byte{lzmaFilterID, 1, c}, nil
}

// UnmarshalBinary unmarshals the given data representation of the LZMA2
// filter.
func (f *lzmaFilter) UnmarshalBinary(data []byte) error {
	if len(data) != lzmaFilterLen {
		return corruptf("xz: data for LZMA2 filter has wrong length")
	}

	if data[0] != lzmaFilterID {
		return corruptf("xz: wrong LZMA2 filter id")
	}

	if data[1] != 1 {
		return corruptf("xz: wrong LZMA2 filter size")
	}

	dc, err := dictsize.Decode(data[2])
	if err != nil {
		return corruptf("xz: wrong LZMA2 dictionary size property")
	}

	f.dictSize = dc

	return nil
}

// id returns the ID for the LZMA2 filter.
func (lzmaFilter) id() uint64 { return lzmaFilterID }

// lzma2Cache carries an LZMA2 reader across the blocks of an xz file. Every
// block is a fresh chunk sequence, and Reset keeps the decoder, its
// probability models and the dictionary instead of rebuilding them per
// block. The zero value is ready for use.
type lzma2Cache struct {
	r *lzma.Reader2
}

// reader creates a new reader for the LZMA2 filter. When a cache holds a
// reader with the block's dictionary size, that reader is reset and reused
// instead.
func (f lzmaFilter) reader(r io.Reader, o *options, cache *lzma2Cache) (
	fr io.Reader, err error,
) {
	if f.dictSize > o.maxDictSize {
		return nil, unsupportedf("xz: LZMA2 dictionary size %d exceeds the limit %d", f.dictSize, o.maxDictSize)
	}

	dc := int(f.dictSize)
	if dc < 1 {
		// Only reachable where int is 32 bits wide: the block asks for a
		// dictionary the address space cannot hold. The file is valid; this
		// platform cannot decode it.
		return nil, unsupportedf("xz: LZMA2 dictionary size %d exceeds the address space", f.dictSize)
	}

	if cache != nil && cache.r != nil && cache.r.DictSize() == dc {
		cache.r.Reset(r)
		return cache.r, nil
	}

	lr, err := lzma.NewReader2(r, dc)
	if err != nil {
		return nil, err
	}

	if cache != nil {
		cache.r = lr
	}

	return lr, nil
}

// writeCloser creates a io.WriteCloser for the LZMA2 filter.
func (f lzmaFilter) writeCloser(w io.WriteCloser, o *options) (io.WriteCloser, error) {
	dc := int(f.dictSize)
	if dc < 1 {
		return nil, unsupportedf("xz: LZMA2 dictionary size %d exceeds the address space", f.dictSize)
	}

	fw, err := lzma.NewWriter2(w, lzma.WithDictSize(dc), lzma.WithProperties(o.props.LC, o.props.LP, o.props.PB))
	if err != nil {
		return nil, err
	}

	return fw, nil
}

// last returns true, because an LZMA2 filter must be the last filter in
// the filter list.
func (lzmaFilter) last() bool { return true }

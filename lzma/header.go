// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// errHeaderDictSize is a dictionary size the classic LZMA header cannot carry.
var (
	errHeaderDictSize = errors.New("lzma: DictCap out of range")
)

// noHeaderSize defines the value of the length field in the LZMA header.
const noHeaderSize uint64 = 1<<64 - 1

// HeaderLen provides the length of the LZMA file header.
const HeaderLen = 13

// Header represents the Header of an LZMA file.
type Header struct {
	Properties Properties
	DictSize   uint32
	// uncompressed Size; negative value if no Size is given
	Size int64
}

// marshalBinary marshals the header.
func (h *Header) marshalBinary() (data []byte, err error) {
	if err = h.Properties.verify(); err != nil {
		return nil, err
	}

	if !(h.DictSize <= MaxDictCap) {
		return nil, fmt.Errorf("%w: %d", errHeaderDictSize, h.DictSize)
	}

	data = make([]byte, HeaderLen)

	// property byte
	data[0] = h.Properties.Code()

	// dictionary capacity
	binary.LittleEndian.PutUint32(data[1:5], h.DictSize)

	// uncompressed size
	var s uint64
	if h.Size > 0 {
		s = uint64(h.Size)
	} else {
		s = noHeaderSize
	}

	binary.LittleEndian.PutUint64(data[5:], s)

	return data, nil
}

// unmarshalBinary unmarshals the header.
func (h *Header) unmarshalBinary(data []byte) error {
	if len(data) != HeaderLen {
		return corruptf("lzma: header has wrong length")
	}

	// properties
	var err error
	if h.Properties, err = PropertiesForCode(data[0]); err != nil {
		return err
	}

	// dictionary capacity
	h.DictSize = binary.LittleEndian.Uint32(data[1:])
	if int(h.DictSize) < 0 {
		return unsupportedf(
			"lzma: header dictionary size %d exceeds the address space",
			h.DictSize,
		)
	}

	// uncompressed size
	s := binary.LittleEndian.Uint64(data[5:])
	if s == noHeaderSize {
		h.Size = -1
	} else {
		h.Size = int64(s)
		if h.Size < 0 {
			return corruptf("lzma: header uncompressed size out of int64 range")
		}
	}

	return nil
}

// validDictSize checks whether the dictionary capacity is correct. This
// is used to weed out wrong file headers.
func validDictSize(dictcap int) bool {
	if int64(dictcap) == MaxDictCap {
		return true
	}

	for n := uint(10); n < 32; n++ { //nolint:mnd // header dictionary sizes are 2^n and 2^n+2^(n-1) for n from 10 up
		if dictcap == 1<<n {
			return true
		}

		if dictcap == 1<<n+1<<(n-1) {
			return true
		}
	}

	return false
}

// ValidHeader checks for a valid LZMA file header. It allows only
// dictionary sizes of 2^n or 2^n+2^(n-1) with n >= 10 or 2^32-1. If
// there is an explicit size it must not exceed 256 GiB. The length of
// the data argument must be HeaderLen.
//
// This function should be disregarded because there is no guarantee that LZMA
// files follow the constraints.
func ValidHeader(data []byte) bool {
	var h Header
	if err := h.unmarshalBinary(data); err != nil {
		return false
	}

	if !validDictSize(int(h.DictSize)) {
		return false
	}

	return h.Size < 0 || h.Size <= 1<<38
}

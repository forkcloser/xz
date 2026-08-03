// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import "io"

// putUint32LE puts the little-endian representation of x into the first
// four bytes of p.
func putUint32LE(p []byte, x uint32) {
	p[0] = byte(x)
	p[1] = byte(x >> 8)
	p[2] = byte(x >> 16)
	p[3] = byte(x >> 24)
}

// putUint64LE puts the little-endian representation of x into the first
// eight bytes of p.
func putUint64LE(p []byte, x uint64) {
	p[0] = byte(x)
	p[1] = byte(x >> 8)
	p[2] = byte(x >> 16)
	p[3] = byte(x >> 24)
	p[4] = byte(x >> 32)
	p[5] = byte(x >> 40)
	p[6] = byte(x >> 48)
	p[7] = byte(x >> 56)
}

// uint32LE converts a little endian representation to an uint32 value.
func uint32LE(p []byte) uint32 {
	return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 |
		uint32(p[3])<<24
}

// putUvarint puts a uvarint representation of x into the byte slice.
func putUvarint(p []byte, x uint64) int {
	i := 0
	for x >= 0x80 {
		p[i] = byte(x) | 0x80
		x >>= 7
		i++
	}
	p[i] = byte(x)
	return i + 1
}

// errOverflow indicates an overflow of the 64-bit unsigned integer.
var errOverflowU64 = corruptf("xz: uvarint overflows 64-bit unsigned integer")

// errNonCanonicalUvarint indicates a value encoded in more bytes than it
// needs.
var errNonCanonicalUvarint = corruptf("xz: uvarint is not encoded in the fewest bytes")

// maxUvarintLen is the longest variable-length integer the xz format allows.
// Nine bytes carry 63 bits, which covers every value the format can express.
const maxUvarintLen = 9

// readUvarint reads a uvarint from the given byte reader.
//
// The xz format requires the shortest encoding of a value and caps the
// encoding at nine bytes, and liblzma rejects violations of either rule.
// Accepting them would let this package read files the reference
// implementation refuses — the kind of parser disagreement that lets content
// slip past a scanner that checked it with a different decoder.
func readUvarint(r io.ByteReader) (x uint64, n int, err error) {
	var s uint
	i := 0
	for {
		b, err := r.ReadByte()
		if err != nil {
			return x, i, err
		}
		i++
		if b < 0x80 {
			// A final byte of zero means the value fits in fewer bytes.
			if i > 1 && b == 0 {
				return x, i, errNonCanonicalUvarint
			}
			return x | uint64(b)<<s, i, nil
		}
		if i >= maxUvarintLen {
			return x, i, errOverflowU64
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
}

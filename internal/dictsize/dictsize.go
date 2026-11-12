// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package dictsize codes a dictionary size as the one byte the xz block
// header's LZMA2 filter properties carry: a one-bit mantissa and a five-bit
// exponent, 4 KiB to 4 GiB-1.
package dictsize

import "errors"

// MaxCode is the code of the largest dictionary, 4 GiB-1.
const MaxCode = 40

// Max is the largest dictionary size the code can express.
const Max = 1<<32 - 1

// ErrCode reports a code above MaxCode.
var ErrCode = errors.New("dictsize: invalid dictionary size code")

// ofCode is the size a code below MaxCode stands for.
func ofCode(c byte) int64 {
	return (2 | int64(c)&1) << (11 + (c>>1)&0x1f) //nolint:mnd // the format's formula
}

// Decode returns the dictionary size a code stands for.
func Decode(c byte) (int64, error) {
	if c >= MaxCode {
		if c == MaxCode {
			return Max, nil
		}

		return 0, ErrCode
	}

	return ofCode(c), nil
}

// Encode returns the code of the smallest expressible dictionary size that
// is at least n, or MaxCode when n exceeds Max.
func Encode(n int64) byte {
	a, b := byte(0), byte(MaxCode)
	for a < b {
		c := a + (b-a)>>1

		m := ofCode(c)
		if n <= m {
			if n == m {
				return c
			}

			b = c
		} else {
			a = c + 1
		}
	}

	return a
}

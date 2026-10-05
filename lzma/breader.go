// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "io"

// maxEmptyReads bounds ReadByte's retries on a reader that keeps returning
// (0, nil): io.Reader allows it and asks callers to retry, not to fail.
const maxEmptyReads = 100

// breader provides the ReadByte function for a Reader. It doesn't read
// more data from the reader than absolutely necessary.
type breader struct {
	io.Reader

	// helper slice to save allocations
	p []byte
}

// ByteReader converts an io.Reader into an io.ByteReader.
func ByteReader(r io.Reader) io.ByteReader {
	br, ok := r.(io.ByteReader)
	if !ok {
		return &breader{r, make([]byte, 1)}
	}

	return br
}

// ReadByte read byte function.
func (r *breader) ReadByte() (byte, error) {
	for range maxEmptyReads {
		n, err := r.Read(r.p)
		if n > 0 {
			return r.p[0], nil
		}

		if err != nil {
			return 0, err
		}
	}

	return 0, io.ErrNoProgress
}

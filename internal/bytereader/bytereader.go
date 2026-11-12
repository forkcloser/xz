// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package bytereader reads one byte at a time from an io.Reader without
// reading past the byte asked for, so the reader's position stays exact.
package bytereader

import "io"

// maxEmptyReads bounds how many times a Read may return (0, nil) before
// New's reader gives up: io.Reader allows it and asks callers to retry,
// not to fail.
const maxEmptyReads = 100

// reader provides ReadByte for an io.Reader, reading no more than the byte
// asked for.
type reader struct {
	io.Reader

	p []byte
}

// New returns r itself when it is an io.ByteReader, and otherwise a
// ByteReader over it that reads one byte per call.
func New(r io.Reader) io.ByteReader {
	if br, ok := r.(io.ByteReader); ok {
		return br
	}

	return &reader{r, make([]byte, 1)}
}

// ReadByte reads one byte, retrying empty reads up to the bound.
func (r *reader) ReadByte() (byte, error) {
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

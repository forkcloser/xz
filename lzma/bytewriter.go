// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"io"
)

// errLimit indicates that the limit of the limitedByteWriter has been
// reached. It is internal to the encoder: Writer2 turns it into a chunk
// boundary and the classic Writer never sets a limit, so it does not reach a
// caller.
var errLimit = errors.New("lzma: chunk limit reached")

// limitedByteWriter provides a byte writer that can be written until a
// limit is reached. The field N provides the number of remaining
// bytes.
type limitedByteWriter struct {
	BW io.ByteWriter
	N  int64
}

// WriteByte writes a single byte to the limited byte writer. It returns
// errLimit if the limit has been reached. If the byte is successfully
// written the field N will be decremented by one.
func (l *limitedByteWriter) WriteByte(c byte) error {
	if l.N <= 0 {
		return errLimit
	}
	if err := l.BW.WriteByte(c); err != nil {
		return err
	}
	l.N--
	return nil
}

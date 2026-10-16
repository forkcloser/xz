// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// TestWriter2SmallDictIncompressible verifies that a writer with a small
// dictionary can encode incompressible data. Such data fills the 64 KiB
// compressed-chunk limit before the chunk is cut, so the uncompressed form of
// the chunk would be smaller — but a dictionary below that size can no longer
// replay the chunk's input, and choosing the uncompressed form made Write and
// Close fail with ErrNoSpace.
func TestWriter2SmallDictIncompressible(t *testing.T) {
	t.Parallel()

	rnd := rand.NewChaCha8([32]byte{13})

	for _, dictCap := range []int{lzma.MinDictCap, 8192, 16384, 1 << 16} {
		for _, size := range []int{5000, 10000, 100000} {
			data := make([]byte, size)
			_, _ = rnd.Read(data)

			var buf bytes.Buffer

			w, err := lzma.Writer2Config{DictCap: dictCap}.NewWriter2(&buf)
			if err != nil {
				t.Fatalf("NewWriter2 error %s", err)
			}

			if _, err = w.Write(data); err != nil {
				t.Fatalf("dictCap=%d size=%d: Write error %s",
					dictCap, size, err)
			}

			if err = w.Close(); err != nil {
				t.Fatalf("dictCap=%d size=%d: Close error %s",
					dictCap, size, err)
			}

			r, err := lzma.NewReader2(&buf)
			if err != nil {
				t.Fatalf("NewReader2 error %s", err)
			}

			out := new(bytes.Buffer)
			if _, err = io.Copy(out, r); err != nil {
				t.Fatalf("dictCap=%d size=%d: decompress error %s",
					dictCap, size, err)
			}

			if !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("dictCap=%d size=%d: decompressed data differs "+
					"from original", dictCap, size)
			}
		}
	}
}

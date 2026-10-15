// Copyright 2026 Forkcloser. All rights reserved.
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

// BenchmarkReader2Uncompressed decodes a sequence of uncompressed chunks,
// what an encoder emits for incompressible data, through one reader reused
// by Reset, so the dictionary is already grown and the cost is the chunk
// copies alone.
func BenchmarkReader2Uncompressed(b *testing.B) {
	data := make([]byte, 4<<20)
	rng := rand.New(rand.NewPCG(1, 2))

	for i := range data {
		data[i] = byte(rng.Uint32())
	}

	stream := lzma2Stream(b, data)

	r, err := lzma.NewReader2(bytes.NewReader(stream))
	if err != nil {
		b.Fatal(err)
	}

	out := make([]byte, 64<<10)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		r.Reset(bytes.NewReader(stream))

		for {
			if _, err := r.Read(out); err != nil {
				if err != io.EOF { //nolint:errorlint // the decoder's own io.EOF
					b.Fatal(err)
				}

				break
			}
		}
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/forkcloser/xz"
)

// BenchmarkSmallBlocks decodes a file of many small blocks through both
// readers: the per-block cost, header parsing included, is what shows.
func BenchmarkSmallBlocks(b *testing.B) {
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 4096)

	var buf bytes.Buffer

	w, err := xz.WriterConfig{BlockSize: 1 << 10}.NewWriter(&buf)
	if err != nil {
		b.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		b.Fatal(err)
	}

	if err = w.Close(); err != nil {
		b.Fatal(err)
	}

	file := buf.Bytes()

	b.Run("sequential", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		b.ReportAllocs()

		for range b.N {
			r, err := xz.NewReader(bytes.NewReader(file))
			if err != nil {
				b.Fatal(err)
			}

			if _, err = io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		b.ReportAllocs()

		for range b.N {
			r, err := xz.ParallelReaderConfig{Workers: 2}.NewParallelReader(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				b.Fatal(err)
			}

			if _, err = io.Copy(io.Discard, r); err != nil {
				b.Fatal(err)
			}

			if err = r.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
}

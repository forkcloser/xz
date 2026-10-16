// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/forkcloser/xz"
)

// TestParallelReaderSizeThatLies: a size larger than the bytes the
// io.ReaderAt serves is a file cut short, io.ErrUnexpectedEOF, not the
// reader's end of file, whether the lie is one group of four or a megabyte:
// a caller that measured its input wrong must hear truncation, as it would
// from a file that is. (A size that is not a multiple of four is refused as
// corrupt before any read, as the format demands of every file.)
func TestParallelReaderSizeThatLies(t *testing.T) {
	t.Parallel()

	data := cData(5000)
	file := refStream(refOptions{check: xz.CRC32, dictCode: 20}, refBlocks(t, data, 1000))

	for _, extra := range []int64{4, 12, 4096, 1 << 20} {
		pr, err := xz.NewParallelReader(bytes.NewReader(file), int64(len(file))+extra)
		if err == nil {
			_, err = io.ReadAll(pr)
			_ = pr.Close()
		}

		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("size overstated by %d: %v; want io.ErrUnexpectedEOF", extra, err)
		}
	}
}

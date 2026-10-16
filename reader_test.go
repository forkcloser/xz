// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz //nolint:testpackage // white-box: checks the unexported errUnexpectedData

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestCheckNone(t *testing.T) {
	t.Parallel()

	const file = "testdata/fox-check-none.xz"

	xz, err := os.Open(file)
	if err != nil {
		t.Fatalf("os.Open(%q) error %s", file, err)
	}

	r, err := NewReader(xz)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}

	var buf bytes.Buffer
	if _, err = io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy error %s", err)
	}
}

func BenchmarkReader(b *testing.B) {
	const testFile = "testdata/enwik7"

	data, err := os.ReadFile(testFile)
	if err != nil {
		b.Fatalf("os.ReadFile(%q) error %s", testFile, err)
	}

	buf := new(bytes.Buffer)
	uncompressedLen := int64(len(data))
	b.SetBytes(uncompressedLen)
	b.ReportAllocs()
	buf.Reset()

	w, err := NewWriter(buf)
	if err != nil {
		b.Fatalf("NewWriter(buf) error %s", err)
	}

	if _, err = w.Write(data); err != nil {
		b.Fatalf("w.Write(data) error %s", err)
	}

	if err = w.Close(); err != nil {
		b.Fatal("w.Write(data)")
	}

	data = make([]byte, buf.Len())
	copy(data, buf.Bytes())

	for b.Loop() {
		buf.Reset()

		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			b.Fatalf("NewReader(data) error %s", err)
		}

		n, err := io.Copy(buf, r)
		if err != nil {
			b.Fatalf("io.Copy(buf, r) error %s", err)
		}

		if n != uncompressedLen {
			b.Fatalf("io.Copy got %d; want %d", n, uncompressedLen)
		}
	}
}

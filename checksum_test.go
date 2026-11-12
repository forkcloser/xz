// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz //nolint:testpackage // white-box: builds block headers and footers from unexported types

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWithCheck: WithCheck(CheckNone) writes an unchecked stream, and the
// default is CRC64; None is the stream flags' zero, which an API with a zero
// default could not have selected.
func TestWithCheck(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		opts []Option
		want byte
	}{
		{nil, checkCRC64},
		{[]Option{WithCheck(CheckNone)}, checkNone},
		{[]Option{WithCheck(CheckCRC32)}, checkCRC32},
		{[]Option{WithCheck(CheckSHA256)}, checkSHA256},
	} {
		var buf bytes.Buffer

		w, err := NewWriter(&buf, tc.opts...)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}

		if err = w.Close(); err != nil {
			t.Fatal(err)
		}

		if got := buf.Bytes()[7]; got != tc.want {
			t.Fatalf("check %#x; want %#x", got, tc.want)
		}
	}
}

// TestReadReferenceFiles decodes files the reference xz produced from
// parallelTestData(1 << 18): both presets' extremes, each check type, and
// the multi-block layout only xz's threaded mode emits, through both
// readers.
func TestReadReferenceFiles(t *testing.T) {
	t.Parallel()

	data := parallelTestData(1 << 18)

	for _, name := range []string{
		"preset0", "preset9", "crc32", "sha256", "check-none", "multiblock",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			file, err := os.ReadFile(filepath.Join("testdata", "reference", name+".xz"))
			if err != nil {
				t.Fatal(err)
			}

			r, err := NewReader(bytes.NewReader(file))
			if err != nil {
				t.Fatalf("NewReader: %s", err)
			}

			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("sequential read: %s", err)
			}

			if !bytes.Equal(got, data) {
				t.Error("sequential decode differs from the original")
			}

			pr, err := NewReader(bytes.NewReader(file), WithWorkers(4))
			if err != nil {
				t.Fatalf("NewReader: %s", err)
			}
			defer func() { _ = pr.Close() }()

			got, err = io.ReadAll(pr)
			if err != nil {
				t.Fatalf("parallel read: %s", err)
			}

			if !bytes.Equal(got, data) {
				t.Error("parallel decode differs from the original")
			}
		})
	}
}

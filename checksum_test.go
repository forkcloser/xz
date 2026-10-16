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

// TestNoneCheckSumFieldIsNotSelectable pins a trap in the writer API. None is
// 0x0 and WriterConfig.fill treats a zero CheckSum as "not set", so
// WriterConfig{CheckSum: None} silently produces a CRC-64 stream instead of an
// unchecked one, with no error. Selecting None requires the separate
// NoCheckSum field.
func TestNoneCheckSumFieldIsNotSelectable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := WriterConfig{CheckSum: None}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	if got := buf.Bytes()[7]; got != CRC64 {
		t.Fatalf("CheckSum: None produced check %#x; documenting it as %#x",
			got, CRC64)
	}

	buf.Reset()

	w, err = WriterConfig{NoCheckSum: true}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	if got := buf.Bytes()[7]; got != None {
		t.Fatalf("NoCheckSum produced check %#x; want %#x", got, None)
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

			pr, err := NewParallelReader(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				t.Fatalf("NewParallelReader: %s", err)
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

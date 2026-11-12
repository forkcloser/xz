// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// XZ Utils' decoder conformance files, testdata/xzutils (its tests/files
// directory and README). A good file decodes; a bad file is refused as
// corrupt, or as cut short where the damage is a size that runs the decoder
// off the end; a file upstream classes as unsupported is refused as
// unsupported or corrupt, never decoded. The .lz files are lzip's, a format
// this package does not read, and are not run.

// vectors lists the files of one kind by extension.
func vectors(t *testing.T, ext string) []string {
	t.Helper()

	files, err := filepath.Glob("testdata/xzutils/*" + ext)
	if err != nil {
		t.Fatal(err)
	}

	if len(files) == 0 {
		t.Fatalf("no %s files under testdata/xzutils", ext)
	}

	return files
}

// decodeXZ decodes a file through the Reader and the ParallelReader and
// requires them to agree on the data; it returns the data and each reader's
// error, for the caller to class both.
func decodeXZ(t *testing.T, data []byte) (out []byte, err, perr error) {
	t.Helper()

	r, err := xz.NewReader(bytes.NewReader(data))
	if err == nil {
		out, err = io.ReadAll(r)
	}

	pr, perr := xz.NewReader(bytes.NewReader(data), xz.WithWorkers(4))

	var pout []byte
	if perr == nil {
		pout, perr = io.ReadAll(pr)
		if cerr := pr.Close(); perr == nil {
			perr = cerr
		}
	}

	if (err == nil) != (perr == nil) {
		t.Fatalf("Reader: %v; ParallelReader: %v", err, perr)
	}

	if err == nil && !bytes.Equal(out, pout) {
		t.Fatalf("Reader decodes %d bytes, ParallelReader %d", len(out), len(pout))
	}

	return out, err, perr
}

// requireClass holds one reader's result to the file's kind.
func requireClass(t *testing.T, reader, name string, err error) {
	t.Helper()

	switch {
	case strings.HasPrefix(name, "good-"):
		if err != nil {
			t.Fatalf("%s: good file refused: %v", reader, err)
		}
	case strings.HasPrefix(name, "bad-"):
		if !errors.Is(err, xz.ErrCorrupt) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%s: bad file: %v, want ErrCorrupt or ErrUnexpectedEOF", reader, err)
		}
	case strings.HasPrefix(name, "unsupported-"):
		if !errors.Is(err, xz.ErrUnsupported) && !errors.Is(err, xz.ErrCorrupt) {
			t.Fatalf("%s: unsupported file: %v, want ErrUnsupported or ErrCorrupt", reader, err)
		}
	default:
		t.Fatal("unknown kind of file")
	}
}

func TestXZUtilsVectorsXZ(t *testing.T) {
	t.Parallel()

	for _, path := range vectors(t, ".xz") {
		name := filepath.Base(path)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			_, err, perr := decodeXZ(t, data)
			requireClass(t, "Reader", name, err)
			requireClass(t, "ParallelReader", name, perr)
		})
	}
}

func TestXZUtilsVectorsLZMA(t *testing.T) {
	t.Parallel()

	for _, path := range vectors(t, ".lzma") {
		name := filepath.Base(path)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			r, err := lzma.NewReader(bytes.NewReader(data))
			if err == nil {
				_, err = io.ReadAll(r)
			}

			switch {
			case strings.HasPrefix(name, "good-"):
				if err != nil {
					t.Fatalf("good file refused: %v", err)
				}
			case strings.HasPrefix(name, "bad-"):
				if !errors.Is(err, lzma.ErrCorrupt) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("bad file: %v, want ErrCorrupt or ErrUnexpectedEOF", err)
				}
			default:
				t.Fatal("unknown kind of file")
			}
		})
	}
}

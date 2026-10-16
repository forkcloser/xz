// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// lzma2Stream compresses data into an LZMA2 chunk sequence.
func lzma2Stream(tb testing.TB, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		tb.Fatal(err)
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// countingFailingWriter fails every write after the first allow calls with
// err, counting calls so a test can see whether anything was attempted after
// the failure. (failingWriter in writer2_close_test.go counts bytes instead.)
type countingFailingWriter struct {
	allow int
	calls int
	err   error
}

func (f *countingFailingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.allow {
		return 0, f.err
	}

	return len(p), nil
}

// TestWriter2ErrorIsSticky covers writing on after a chunk flush failed. The
// chunk accounting was left mid-flight and the next Write panicked with
// "maxUncompressed reached"; now the first error is what every later call
// reports, and nothing more is written.
func TestWriter2ErrorIsSticky(t *testing.T) {
	t.Parallel()

	ioErr := errors.New("transient")
	fw := &countingFailingWriter{allow: 0, err: ioErr}

	w, err := lzma.NewWriter2(fw)
	if err != nil {
		t.Fatal(err)
	}

	rnd := make([]byte, 3<<20)
	for i := range rnd {
		rnd[i] = byte(i*7919 ^ i>>3)
	}

	if _, err = w.Write(rnd); !errors.Is(err, ioErr) {
		t.Fatalf("Write gave %v; want the writer's error", err)
	}

	calls := fw.calls
	fw.allow = 1 << 30

	if _, err = w.Write(rnd[:1024]); !errors.Is(err, ioErr) {
		t.Errorf("Write after a failure gave %v; want the original error", err)
	}

	if err = w.Flush(); !errors.Is(err, ioErr) {
		t.Errorf("Flush after a failure gave %v; want the original error", err)
	}

	if err = w.Close(); !errors.Is(err, ioErr) {
		t.Errorf("Close after a failure gave %v; want the original error", err)
	}

	if fw.calls != calls {
		t.Errorf("%d writes were attempted after the failure", fw.calls-calls)
	}
}

// TestWriterErrorIsSticky is the classic-format counterpart.
func TestWriterErrorIsSticky(t *testing.T) {
	t.Parallel()

	ioErr := errors.New("transient")
	fw := &countingFailingWriter{allow: 1, err: ioErr} // the header is the first write
	// The smallest dictionary, so the encoder compresses during Write
	// rather than buffering the whole input until Close.
	w, err := lzma.WriterConfig{DictCap: lzma.MinDictCap}.NewWriter(fw)
	if err != nil {
		t.Fatal(err)
	}
	// Incompressible, so the encoder's output outgrows the 4 KiB buffer in
	// front of the writer during Write.
	rnd := make([]byte, 1<<20)
	_, _ = rand.NewChaCha8([32]byte{1}).Read(rnd)

	if _, err = w.Write(rnd); !errors.Is(err, ioErr) {
		t.Fatalf("Write gave %v; want the writer's error", err)
	}

	fw.allow = 1 << 30

	if _, err = w.Write(rnd[:16]); !errors.Is(err, ioErr) {
		t.Errorf("Write after a failure gave %v; want the original error", err)
	}

	if err = w.Close(); !errors.Is(err, ioErr) {
		t.Errorf("Close after a failure gave %v; want the original error", err)
	}
}

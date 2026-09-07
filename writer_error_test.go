// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"errors"
	"testing"
)

// failingWriter accepts the first allow writes and fails every later one
// with err, counting calls so a test can see whether anything was written
// after the failure.
type failingWriter struct {
	allow int
	calls int
	err   error
	buf   bytes.Buffer
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.allow {
		return 0, f.err
	}
	return f.buf.Write(p)
}

// TestWriterErrorIsSticky covers a failure of the underlying writer while a
// block is being closed. The block writer had marked itself closed on the
// way out, so the next Write reported ErrClosed — a closed writer the caller
// never closed — and Close went on to write an index over a block that was
// never finished. The first error is now the only one the writer reports.
func TestWriterErrorIsSticky(t *testing.T) {
	ioErr := errors.New("disk on fire")
	// Stream header and first block header are the first two writes; the
	// first block's data is the third.
	fw := &failingWriter{allow: 2, err: ioErr}
	w, err := WriterConfig{BlockSize: 1024}.NewWriter(fw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(parallelTestData(4096)); !errors.Is(err, ioErr) {
		t.Fatalf("first Write gave %v; want the writer's error", err)
	}
	calls := fw.calls
	if _, err = w.Write(parallelTestData(4096)); !errors.Is(err, ioErr) {
		t.Errorf("Write after a failure gave %v; want the original error", err)
	}
	if err = w.Close(); !errors.Is(err, ioErr) {
		t.Errorf("Close after a failure gave %v; want the original error", err)
	}
	if fw.calls != calls {
		t.Errorf("%d writes were attempted after the failure", fw.calls-calls)
	}
}

// TestWriterTransientErrorDoesNotPanic covers a failure inside a block, when
// an LZMA2 chunk is flushed, followed by more writes. The chunk accounting was
// left mid-flight, and the next Write panicked with "maxUncompressed reached"
// inside the lzma package — a library aborting the process because the disk
// hiccupped once.
func TestWriterTransientErrorDoesNotPanic(t *testing.T) {
	ioErr := errors.New("transient")
	// Header, block header, then the first chunk flush fails; everything
	// after would succeed if attempted.
	fw := &failingWriter{allow: 2, err: ioErr}
	w, err := NewWriter(fw)
	if err != nil {
		t.Fatal(err)
	}
	// Incompressible data, so a compressed chunk fills and is flushed
	// inside Write.
	rnd := make([]byte, 3<<20)
	for i := range rnd {
		rnd[i] = byte(i*7919 ^ i>>3)
	}
	fw.err = ioErr
	if _, err = w.Write(rnd); !errors.Is(err, ioErr) {
		t.Fatalf("Write gave %v; want the writer's error", err)
	}
	fw.allow = 1 << 30 // the transport recovers
	if _, err = w.Write(rnd[:1024]); !errors.Is(err, ioErr) {
		t.Errorf("Write after a transient failure gave %v; want the original error", err)
	}
	if err = w.Close(); !errors.Is(err, ioErr) {
		t.Errorf("Close after a transient failure gave %v; want the original error", err)
	}
}

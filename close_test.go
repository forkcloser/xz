// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forkcloser/xz"
)

// gatedSource is a ReaderAt whose reads, once armed, wait until the gate
// opens: a worker inside one of them is a worker mid-block. parked is
// signalled once, by the first read that waits.
type gatedSource struct {
	r      *bytes.Reader
	gate   chan struct{}
	parked chan struct{}
	once   sync.Once
	armed  atomic.Bool
	closed atomic.Bool
	late   atomic.Int32
}

func (s *gatedSource) ReadAt(p []byte, off int64) (int, error) {
	if s.closed.Load() {
		s.late.Add(1)
	}

	if s.armed.Load() {
		s.once.Do(func() { close(s.parked) })
		<-s.gate
	}

	return s.r.ReadAt(p, off)
}

// TestParallelReaderCloseWaitsForWorkers: Close returns only once every
// worker has finished the block it has in hand, so the source is not read
// after Close returns, whatever the caller does with it then.
func TestParallelReaderCloseWaitsForWorkers(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := xz.WriterConfig{BlockSize: 4096}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 20000)); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	file := buf.Bytes()
	src := &gatedSource{r: bytes.NewReader(file), gate: make(chan struct{}), parked: make(chan struct{})}

	r, err := xz.ParallelReaderConfig{Workers: 4}.NewParallelReader(src, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}

	// The index was read at construction; from here every read of the
	// file is behind the gate, and the first Read sends the workers there.
	src.armed.Store(true)

	go func() { _, _ = r.Read(make([]byte, 10)) }()

	<-src.parked // a worker is at the gate

	closed := make(chan struct{})

	go func() {
		_ = r.Close()

		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned while a worker was still reading the source")
	case <-time.After(200 * time.Millisecond):
	}

	close(src.gate)

	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return once the workers were released")
	}

	src.closed.Store(true)

	if n := src.late.Load(); n != 0 {
		t.Fatalf("%d reads of the source after Close returned", n)
	}
}

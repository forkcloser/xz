// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"errors"
	"testing"
)

// failingWriter starts failing after it has accepted a given number of bytes,
// which puts the failure in the final flush rather than in an earlier Write.
type failingWriter struct {
	allow int
	err   error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.allow <= 0 {
		return 0, w.err
	}
	if len(p) > w.allow {
		n := w.allow
		w.allow = 0
		return n, w.err
	}
	w.allow -= len(p)
	return len(p), nil
}

// TestWriter2CloseReportsFlushFailure covers a write error that only surfaces
// when Close flushes the last chunk. Close discarded it and returned nil, so a
// caller that checked every error still ended up with a truncated stream and
// no indication anything had gone wrong — the writer-side twin of accepting a
// truncated file on read.
func TestWriter2CloseReportsFlushFailure(t *testing.T) {
	sentinel := errors.New("device full")

	for _, allow := range []int{0, 1, 8, 32, 64} {
		fw := &failingWriter{allow: allow, err: sentinel}
		w, err := NewWriter2(fw)
		if err != nil {
			t.Fatalf("NewWriter2: %s", err)
		}
		// Enough data that closing has to flush a chunk out.
		payload := make([]byte, 1<<15)
		for i := range payload {
			payload[i] = byte(i)
		}
		if _, err = w.Write(payload); err != nil {
			// A write that already failed is fine; the point is Close.
			if !errors.Is(err, sentinel) {
				t.Fatalf("allow=%d: Write gave %v; want the sentinel", allow, err)
			}
		}
		err = w.Close()
		if err == nil {
			t.Errorf("allow=%d: Close reported success although the "+
				"underlying writer failed", allow)
			continue
		}
		if !errors.Is(err, sentinel) && !errors.Is(err, ErrLimit) {
			t.Errorf("allow=%d: Close gave %v; want the write error", allow, err)
		}
	}
}

// TestWriter2CloseSucceedsOnGoodWriter guards the fix from turning into a
// Close that always fails.
func TestWriter2CloseSucceedsOnGoodWriter(t *testing.T) {
	fw := &failingWriter{allow: 1 << 30}
	w, err := NewWriter2(fw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("the quick brown fox")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("Close on a healthy writer: %s", err)
	}
}

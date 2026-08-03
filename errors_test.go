// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// errFailingReaderAt fails every read with a distinctive I/O error.
type errFailingReaderAt struct{ err error }

func (e errFailingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, e.err
}

// TestErrCorruptDistinguishesFromIO is the point of the sentinels: a caller
// deciding whether to reject input or retry a transfer must be able to tell a
// malformed file from a failed read without matching message text.
func TestErrCorruptDistinguishesFromIO(t *testing.T) {
	// A malformed file.
	bad := hostileStream([]byte{0, 0, 0, 0},
		[]hostileRecord{{unpaddedSize: 1, uncompressedSize: 1 << 60}}, -1)
	_, err := NewParallelReader(bytes.NewReader(bad), int64(len(bad)))
	if err == nil {
		t.Fatal("hostile file accepted")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt input gave %v, which does not match ErrCorrupt", err)
	}

	// A transport failure must not be mistaken for corruption.
	ioErr := errors.New("disk on fire")
	_, err = NewParallelReader(errFailingReaderAt{err: ioErr}, 1024)
	if !errors.Is(err, ioErr) {
		t.Errorf("I/O failure gave %v; want it to wrap the reader's error", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("I/O failure %v matched ErrCorrupt", err)
	}
}

// TestErrClosedMatchesSentinel covers the reader and the writer, whose closed
// errors used to be unexported values with nothing in common.
func TestErrClosedMatchesSentinel(t *testing.T) {
	data := parallelTestData(1 << 14)
	xz := compressMultiBlock(t, data, 4<<10)

	r, err := NewParallelReader(bytes.NewReader(xz), int64(len(xz)))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Read(make([]byte, 4)); !errors.Is(err, ErrClosed) {
		t.Errorf("Read after Close gave %v; want a match for ErrClosed", err)
	}

	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Close gave %v; want a match for ErrClosed", err)
	}
}

// TestWriteToOnExhaustedReader covers the io.WriterTo contract. io.Copy never
// reports io.EOF for an ordinary reader, so a drained reader must report
// success rather than making callers special-case EOF.
func TestWriteToOnExhaustedReader(t *testing.T) {
	data := parallelTestData(1 << 15)
	xz := compressMultiBlock(t, data, 4<<10)
	r, err := NewParallelReader(bytes.NewReader(xz), int64(len(xz)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	var first bytes.Buffer
	n, err := r.WriteTo(&first)
	if err != nil {
		t.Fatalf("first WriteTo error %s", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("first WriteTo wrote %d bytes; want %d", n, len(data))
	}

	var second bytes.Buffer
	n, err = r.WriteTo(&second)
	if err != nil {
		t.Fatalf("WriteTo on an exhausted reader returned %v; want nil", err)
	}
	if n != 0 {
		t.Fatalf("WriteTo on an exhausted reader wrote %d bytes", n)
	}
	// io.Copy is the way most callers reach WriteTo.
	if _, err = io.Copy(io.Discard, r); err != nil {
		t.Fatalf("io.Copy on an exhausted reader returned %v; want nil", err)
	}
}

// stuckWriter accepts nothing and reports no error, which io.Writer forbids.
// The loop must give up rather than spin.
type stuckWriter struct{ calls int }

func (w *stuckWriter) Write(p []byte) (int, error) {
	w.calls++
	return 0, nil
}

func TestWriteToGivesUpOnStuckWriter(t *testing.T) {
	data := parallelTestData(1 << 15)
	xz := compressMultiBlock(t, data, 4<<10)
	r, err := NewParallelReader(bytes.NewReader(xz), int64(len(xz)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	w := &stuckWriter{}
	if _, err = r.WriteTo(w); err != io.ErrShortWrite {
		t.Fatalf("WriteTo to a stuck writer returned %v; want io.ErrShortWrite", err)
	}
	if w.calls > 2 {
		t.Errorf("WriteTo called the stuck writer %d times", w.calls)
	}
}

// TestNonCanonicalUvarintRejected pins the encoding rules the xz format sets
// and liblzma enforces: shortest form, at most nine bytes. Accepting more
// would make this package disagree with the reference decoder about what a
// file contains.
func TestNonCanonicalUvarintRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		enc  []byte
	}{
		{"zero in two bytes", []byte{0x80, 0x00}},
		{"zero in nine bytes", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}},
		{"one in three bytes", []byte{0x81, 0x80, 0x00}},
	} {
		_, _, err := readUvarint(bytes.NewReader(tc.enc))
		if !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: got %v; want a corruption error", tc.name, err)
		}
	}
	// Ten bytes exceeds the format's limit even with a valid final byte.
	tooLong := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}
	if _, _, err := readUvarint(bytes.NewReader(tooLong)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("ten-byte uvarint: got %v; want a corruption error", err)
	}
	// The canonical forms still work.
	for _, u := range []uint64{0, 1, 0x7f, 0x80, 1<<63 - 1} {
		p := make([]byte, 10)
		n := putUvarint(p, u)
		x, m, err := readUvarint(bytes.NewReader(p[:n]))
		if err != nil || x != u || m != n {
			t.Errorf("round trip of %d: got (%d, %d, %v)", u, x, m, err)
		}
	}
}

// TestZeroWorkersStillDecodes covers the promoted Workers field being set to
// something unusable between construction and the first read. It used to leave
// Read waiting on blocks that nothing was decoding.
func TestZeroWorkersStillDecodes(t *testing.T) {
	data := parallelTestData(1 << 16)
	xz := compressMultiBlock(t, data, 8<<10)
	r, err := NewParallelReader(bytes.NewReader(xz), int64(len(xz)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	r.Workers = 0

	done := make(chan error, 1)
	go func() {
		got, err := io.ReadAll(r)
		if err == nil && !bytes.Equal(got, data) {
			err = errors.New("decoded data differs from original")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Read never returned with Workers set to zero")
	}
}

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// TestEdgeShortCompressedChunk: a compressed LZMA2 chunk whose size field
// leaves fewer than the range coder's five bytes is corrupt, not the end of
// the sequence.
func TestEdgeShortCompressedChunk(t *testing.T) {
	t.Parallel()

	for n := 1; n <= 5; n++ {
		// cLRND, one uncompressed byte, compressed size n, properties 0x5D.
		in := append([]byte{0xE0, 0x00, 0x00, 0x00, byte(n - 1), 0x5D}, make([]byte, n)...)

		r, err := lzma.NewReader2(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("n=%d: NewReader2: %v", n, err)
		}

		b, err := io.ReadAll(r)
		if len(b) != 0 || !errors.Is(err, lzma.ErrCorrupt) {
			t.Errorf("n=%d: %d bytes, %v; want 0 bytes and ErrCorrupt", n, len(b), err)
		}
	}
}

// TestEdgeHeaderOnlyInput: a classic stream cut inside the range coder's
// first five bytes is a stream cut short.
func TestEdgeHeaderOnlyInput(t *testing.T) {
	t.Parallel()

	stream := classicStream(t, []byte("hello"))

	for n := lzma.HeaderLen; n < lzma.HeaderLen+5; n++ {
		_, err := lzma.NewReader(bytes.NewReader(stream[:n]))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%d bytes: %v; want io.ErrUnexpectedEOF", n, err)
		}
	}

	if _, err := lzma.NewReader(bytes.NewReader(stream[:lzma.HeaderLen+5])); err != nil {
		t.Errorf("header and five bytes: %v; want a reader", err)
	}
}

// TestEdgeReaderErrorIsFinal: the data decoded before a failure comes with
// the error, and every later Read returns the error again with no data.
func TestEdgeReaderErrorIsFinal(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("abc"), 100)
	stream := classicStream(t, data)

	r, err := lzma.NewReader(bytes.NewReader(stream[:len(stream)-3]))
	if err != nil {
		t.Fatal(err)
	}

	p := make([]byte, 1<<20)

	n, err := r.Read(p)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("first Read: %d bytes, %v; want io.ErrUnexpectedEOF", n, err)
	}

	if !bytes.Equal(p[:n], data[:n]) {
		t.Fatal("the bytes delivered with the error are not the stream's prefix")
	}

	for i := range 2 {
		if n, err := r.Read(p); n != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("Read %d after the error: %d bytes, %v; want 0 and the same error", i+2, n, err)
		}
	}
}

// TestEdgeReader2ResetDropsUnread: Reset starts the new sequence with the
// new sequence's bytes, not what the previous one decoded and nobody read.
func TestEdgeReader2ResetDropsUnread(t *testing.T) {
	t.Parallel()

	first := lzma2Stream(t, bytes.Repeat([]byte("A"), 1000))
	second := lzma2Stream(t, bytes.Repeat([]byte("B"), 10))

	r, err := lzma.NewReader2(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = io.ReadFull(r, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}

	r.Reset(bytes.NewReader(second))

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, bytes.Repeat([]byte("B"), 10)) {
		t.Fatalf("after Reset: %d bytes starting %q; want the ten B", len(got), got[:min(20, len(got))])
	}
}

// TestEdgeSizeZero: an explicit uncompressed size of zero is a size, written
// as such and read back as an empty stream; so is the hand-written stream
// liblzma and xz accept.
func TestEdgeSizeZero(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.WriterConfig{SizeInHeader: true, Size: 0}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if size := buf.Bytes()[5:13]; !bytes.Equal(size, make([]byte, 8)) {
		t.Fatalf("header size field % x; want zero", size)
	}

	// properties 0x5D, dictionary 4096, size 0, the range coder's flush.
	crafted := append([]byte{0x5D, 0x00, 0x10, 0x00, 0x00}, make([]byte, 13)...)

	for name, stream := range map[string][]byte{"written": buf.Bytes(), "crafted": crafted} {
		r, err := lzma.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatalf("%s: NewReader: %v", name, err)
		}

		if b, err := io.ReadAll(r); err != nil || len(b) != 0 {
			t.Errorf("%s: %d bytes, %v; want none and no error", name, len(b), err)
		}
	}
}

// TestEdgeWriterClosed: a second Close writes nothing and a Write after
// Close is refused.
func TestEdgeWriterClosed(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	size := buf.Len()

	if err := w.Close(); err == nil {
		t.Error("second Close returned nil")
	}

	if buf.Len() != size {
		t.Errorf("second Close wrote %d bytes", buf.Len()-size)
	}

	if _, err := w.Write([]byte("y")); err == nil {
		t.Error("Write after Close returned nil")
	}
}

// TestEdgeShortSizeIsNotCorruption: closing before the declared size is the
// caller's error, not a corrupt stream.
func TestEdgeShortSizeIsNotCorruption(t *testing.T) {
	t.Parallel()

	w, err := lzma.WriterConfig{Size: 10}.NewWriter(io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err == nil || errors.Is(err, lzma.ErrCorrupt) {
		t.Errorf("Close short of the size: %v; want an error that is not ErrCorrupt", err)
	}
}

// classicStream is data in the classic format with the default writer.
func classicStream(tb testing.TB, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := w.Write(data); err != nil {
		tb.Fatal(err)
	}

	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

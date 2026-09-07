// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"
)

// lzma2Stream compresses data into an LZMA2 chunk sequence.
func lzma2Stream(tb testing.TB, data []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := NewWriter2(&buf)
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

// TestReader2CorruptionMatchesErrCorrupt flips every byte of an LZMA2 stream
// and requires every failure to be classified: the decoder used to return
// plain errors ("unsupported chunk header byte", "writeMatch: distance out of
// range") that a caller could only match by text.
func TestReader2CorruptionMatchesErrCorrupt(t *testing.T) {
	var src bytes.Buffer
	for src.Len() < 4096 {
		src.WriteString("the quick brown fox jumps over the lazy dog 0123456789\n")
	}
	want := src.Bytes()
	stream := lzma2Stream(t, want)

	for _, mask := range []byte{0x40, 0xff} {
		for i := range stream {
			bad := append([]byte{}, stream...)
			bad[i] ^= mask
			r, err := NewReader2(bytes.NewReader(bad))
			if err != nil {
				t.Fatalf("NewReader2 failed on byte %d: %v (errors are deferred to Read)", i, err)
			}
			got, err := io.ReadAll(r)
			switch {
			case err == nil:
				if !bytes.Equal(got, want) {
					t.Errorf("byte %d ^ %#x: different data with no error", i, mask)
				}
			case errors.Is(err, ErrCorrupt), errors.Is(err, io.ErrUnexpectedEOF):
			default:
				t.Errorf("byte %d ^ %#x: %q matches neither ErrCorrupt nor io.ErrUnexpectedEOF", i, mask, err)
			}
		}
	}
}

// TestReader2ShortChunkIsCorruptNotTruncated covers a compressed chunk whose
// header promises more than its bytes decode to. The chunk is read into
// memory in full, so the decoder running out of bytes is an inconsistency
// inside the file, not the file ending early — reporting it as unexpected EOF
// sent callers looking for a truncated download.
func TestReader2ShortChunkIsCorruptNotTruncated(t *testing.T) {
	stream := lzma2Stream(t, bytes.Repeat([]byte("abcdefgh"), 512))
	// Chunk header: byte 0 control (its low bits are the high bits of the
	// uncompressed size), bytes 1-2 the low 16 bits of uncompressed size-1
	// (big endian), bytes 3-4 compressed size-1. The 4096-byte input has
	// size-1 = 0x0fff; setting bit 12 asks for 8192 bytes the chunk does
	// not contain.
	bad := append([]byte{}, stream...)
	if bad[1] != 0x0f || bad[2] != 0xff {
		t.Fatalf("first chunk declares uncompressed size-1 %#x; the fixture assumes 0x0fff", int(bad[1])<<8|int(bad[2]))
	}
	bad[1] ^= 0x10
	r, err := NewReader2(bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	if err == nil {
		t.Fatal("a chunk promising more data than it holds was accepted")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("got %v; want a match for ErrCorrupt", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %v; a short chunk in a complete stream is not truncation", err)
	}

	// Truncating the stream itself, on the other hand, is.
	r, err = NewReader2(bytes.NewReader(stream[:len(stream)/2]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated stream gave %v; want io.ErrUnexpectedEOF", err)
	}
}

// TestReaderClassicHeaderErrorsAreClassified covers the classic format's
// header, whose errors were plain strings too.
func TestReaderClassicHeaderErrorsAreClassified(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("the quick brown fox")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	stream := buf.Bytes()

	bad := append([]byte{}, stream...)
	bad[0] = 0xff // properties code out of range
	if _, err = NewReader(bytes.NewReader(bad)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("invalid properties code gave %v; want a match for ErrCorrupt", err)
	}
	if _, err = NewReader(bytes.NewReader(stream[:5])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short header gave %v; want io.ErrUnexpectedEOF", err)
	}
	bad = append([]byte{}, stream...)
	// Uncompressed size of 2^60: valid encoding, larger than this package
	// decodes.
	copy(bad[5:13], []byte{0, 0, 0, 0, 0, 0, 0, 0x10})
	if _, err = NewReader(bytes.NewReader(bad)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("pebibyte stream gave %v; want a match for ErrUnsupported", err)
	}
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
	ioErr := errors.New("transient")
	fw := &countingFailingWriter{allow: 0, err: ioErr}
	w, err := NewWriter2(fw)
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
	ioErr := errors.New("transient")
	fw := &countingFailingWriter{allow: 1, err: ioErr} // the header is the first write
	// The smallest dictionary, so the encoder compresses during Write
	// rather than buffering the whole input until Close.
	w, err := WriterConfig{DictCap: MinDictCap}.NewWriter(fw)
	if err != nil {
		t.Fatal(err)
	}
	// Incompressible, so the encoder's output outgrows the 4 KiB buffer in
	// front of the writer during Write.
	rnd := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(rnd)
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

// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// The README promises that every error meaning "this input is not valid xz"
// matches ErrCorrupt, that a stream cut short is io.ErrUnexpectedEOF, and
// that an I/O error from the underlying reader comes back as it went in.
// These tests hold the package to that across the whole file, payload
// included: the LZMA2 decoder used to report its own plain errors, so for
// the most common damage — a flipped byte inside the compressed data — a
// caller could not tell corruption from anything else.

// singleBlockFile is a small one-block file whose bytes are almost all LZMA2
// payload, so a corruption sweep over it exercises the decoder rather than
// the container.
func singleBlockFile(tb testing.TB) ([]byte, []byte) {
	tb.Helper()
	want := parallelTestData(4096)
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err = w.Write(want); err != nil {
		tb.Fatal(err)
	}
	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes(), want
}

// checkClassified fails unless err is nil or one of the documented kinds.
func checkClassified(t *testing.T, reader string, i int, mask byte, err error) {
	t.Helper()
	if err == nil || errors.Is(err, ErrCorrupt) || errors.Is(err, io.ErrUnexpectedEOF) {
		return
	}
	t.Errorf("%s: byte %d ^ %#x gave %q, which matches neither ErrCorrupt nor io.ErrUnexpectedEOF",
		reader, i, mask, err)
}

// TestCorruptionIsClassified flips every byte of a payload-heavy file and of
// a multi-block file, with two different masks, and requires each reader to
// either return the right data or an error a caller can act on.
func TestCorruptionIsClassified(t *testing.T) {
	single, singleWant := singleBlockFile(t)
	multi := wellFormed(t)
	multiWant := parallelTestData(4096)

	for _, tc := range []struct {
		name string
		file []byte
		want []byte
	}{
		{"single block", single, singleWant},
		{"multi block", multi, multiWant},
	} {
		for _, mask := range []byte{0x40, 0xff} {
			for i := range tc.file {
				bad := append([]byte{}, tc.file...)
				bad[i] ^= mask

				r, err := NewReader(bytes.NewReader(bad))
				var got []byte
				if err == nil {
					got, err = io.ReadAll(r)
				}
				checkClassified(t, tc.name+" sequential", i, mask, err)
				if err == nil && !bytes.Equal(got, tc.want) {
					t.Errorf("%s sequential: byte %d ^ %#x returned different data with no error",
						tc.name, i, mask)
				}

				pr, err := NewParallelReader(bytes.NewReader(bad), int64(len(bad)))
				if err == nil {
					got, err = io.ReadAll(pr)
					_ = pr.Close()
				}
				checkClassified(t, tc.name+" parallel", i, mask, err)
				if err == nil && !bytes.Equal(got, tc.want) {
					t.Errorf("%s parallel: byte %d ^ %#x returned different data with no error",
						tc.name, i, mask)
				}
			}
		}
	}
}

// TestPayloadErrorsKeepTheirChain checks that classification adds the xz
// sentinel without hiding the lzma one or the decoder's message, so a caller
// who wants the detail still gets it.
func TestPayloadErrorsKeepTheirChain(t *testing.T) {
	file, _ := singleBlockFile(t)
	// The first LZMA2 chunk header byte follows the 12-byte stream header
	// and the 12-byte block header the writer emits for LZMA2 without
	// sizes; 0x03 is a chunk header byte no encoder produces.
	bad := append([]byte{}, file...)
	bad[24] = 0x03
	r, err := NewReader(bytes.NewReader(bad))
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		t.Fatal("an invalid chunk header byte was accepted")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("got %v; want a match for xz.ErrCorrupt", err)
	}
	if !errors.Is(err, lzma.ErrCorrupt) {
		t.Errorf("got %v; want the lzma.ErrCorrupt chain to survive classification", err)
	}
	if err.Error() != "lzma: invalid chunk header byte" {
		t.Errorf("message %q; want the decoder's own", err)
	}
}

// errAfterReader serves r and then fails with err instead of io.EOF, the
// shape of a transport that dies after delivering the file.
type errAfterReader struct {
	r   io.Reader
	err error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, e.err
	}
	return n, err
}

// TestSingleStreamReportsIOErrorAsIs covers the one-byte probe SingleStream
// makes after the stream. Any failure of that read used to be reported as
// trailing data — a corruption error — so a caller sorting "reject the
// file" from "retry the transport" would have rejected a good file on a bad
// connection.
func TestSingleStreamReportsIOErrorAsIs(t *testing.T) {
	file, want := singleBlockFile(t)
	ioErr := errors.New("disk on fire")

	r, err := ReaderConfig{SingleStream: true}.NewReader(
		&errAfterReader{r: bytes.NewReader(file), err: ioErr})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if !errors.Is(err, ioErr) {
		t.Errorf("got %v; want the reader's own error", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Errorf("an I/O error after the stream matched ErrCorrupt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the data before the failure was not delivered")
	}

	// The two legitimate outcomes are unchanged: a clean end, and a byte
	// after the stream.
	r, err = ReaderConfig{SingleStream: true}.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); err != nil {
		t.Errorf("clean single stream: %v", err)
	}
	r, err = ReaderConfig{SingleStream: true}.NewReader(
		bytes.NewReader(append(append([]byte{}, file...), 0)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
		t.Errorf("trailing byte gave %v; want a match for ErrCorrupt", err)
	}
}

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

// TestStrictTrailingChunkBytes: a compressed LZMA2 chunk whose size field
// counts a byte past the end of its data is corrupt, as liblzma has it.
func TestStrictTrailingChunkBytes(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("the quick brown fox "), 50)
	stream := lzma2Stream(t, data)

	// The first chunk: control, unpacked size (2), packed size (2),
	// properties; its data follows for packed+1 bytes.
	packed := int(stream[3])<<8 | int(stream[4]) + 1
	bad := bytes.Clone(stream[:6+packed])
	bad = append(bad, 0x00)
	bad = append(bad, stream[6+packed:]...)
	bad[3], bad[4] = byte(packed>>8), byte(packed) // packed+1, stored minus one

	r, err := lzma.NewReader2(bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(r)
	if !errors.Is(err, lzma.ErrCorrupt) {
		t.Fatalf("%d bytes, %v; want ErrCorrupt", len(got), err)
	}

	r, err = lzma.NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}

	got, err = io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the unmodified stream: %d bytes, %v", len(got), err)
	}
}

// TestStrictEOSMarkerInChunk: an LZMA2 chunk carrying the end-of-stream
// marker after its data is corrupt, as liblzma has it: a chunk is delimited
// by its sizes alone.
func TestStrictEOSMarkerInChunk(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte("the quick brown fox "), 50)

	// A classic stream with the marker and no size: its payload past the
	// 13-byte header is LZMA data ending in the marker.
	var buf bytes.Buffer

	w, err := lzma.WriterConfig{EOSMarker: true}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	payload := buf.Bytes()[lzma.HeaderLen:]
	unpacked, packed := len(data)-1, len(payload)-1
	chunk := []byte{
		0xE0 | byte(unpacked>>16), byte(unpacked >> 8), byte(unpacked),
		byte(packed >> 8), byte(packed), buf.Bytes()[0],
	}
	chunk = append(chunk, payload...)
	chunk = append(chunk, 0x00) // end of the sequence

	r, err := lzma.NewReader2(bytes.NewReader(chunk))
	if err != nil {
		t.Fatal(err)
	}

	if got, err := io.ReadAll(r); !errors.Is(err, lzma.ErrCorrupt) {
		t.Fatalf("%d bytes, %v; want ErrCorrupt", len(got), err)
	}
}

// TestStrictLCLPUnsupported: a properties byte with lc+lp above four is a
// stream this package refuses, not a corrupt one; the LZMA SDK decodes it.
func TestStrictLCLPUnsupported(t *testing.T) {
	t.Parallel()

	// lc=8, lp=0, pb=2: (2*5+0)*9+8.
	header := append([]byte{98, 0x00, 0x00, 0x10, 0x00}, make([]byte, 8)...)

	_, err := lzma.NewReader(bytes.NewReader(append(header, make([]byte, 5)...)))
	if !errors.Is(err, lzma.ErrUnsupported) || errors.Is(err, lzma.ErrCorrupt) {
		t.Fatalf("%v; want ErrUnsupported and not ErrCorrupt", err)
	}
}

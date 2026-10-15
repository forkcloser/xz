// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"testing"

	"github.com/forkcloser/xz"
)

// bothReaders decodes data with the sequential and the parallel reader and
// requires each to end in want: a complete file that is not a valid .xz
// file is corrupt, never cut short, and both readers say the same.
func bothReaders(t *testing.T, name string, data []byte, want error) {
	t.Helper()

	var (
		got []byte
		err error
	)

	if r, nerr := xz.NewReader(bytes.NewReader(data)); nerr != nil {
		err = nerr
	} else {
		got, err = io.ReadAll(r)
	}

	if !errors.Is(err, want) {
		t.Errorf("%s: sequential reader: %d bytes, %v; want %v", name, len(got), err, want)
	}

	pr, err := xz.NewParallelReader(bytes.NewReader(data), int64(len(data)))
	if err == nil {
		got, err = io.ReadAll(pr)

		if cerr := pr.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}

	if !errors.Is(err, want) {
		t.Errorf("%s: parallel reader: %d bytes, %v; want %v", name, len(got), err, want)
	}
}

// TestClassesStreamPadding: padding is between streams or after the last;
// before the first stream, or fewer than four bytes of it, is corrupt.
func TestClassesStreamPadding(t *testing.T) {
	t.Parallel()

	fox, err := os.ReadFile("testdata/fox.xz")
	if err != nil {
		t.Fatal(err)
	}

	bothReaders(t, "padding before the stream", append([]byte{0, 0, 0, 0}, fox...), xz.ErrCorrupt)
	bothReaders(t, "only padding", make([]byte, 4096), xz.ErrCorrupt)

	for _, k := range []int{1, 2, 3, 5} {
		bothReaders(t, "trailing zero bytes", append(bytes.Clone(fox), make([]byte, k)...), xz.ErrCorrupt)
	}

	bothReaders(t, "four trailing zero bytes", append(bytes.Clone(fox), make([]byte, 4)...), nil)

	// A second stream cut inside its header is the input ending, however
	// many of its bytes survived; the parallel reader, which knows the
	// file by its size alone, is not held to that.
	for _, cut := range []int{1, 4, 8, 11, 12, 20} {
		data := append(bytes.Clone(fox), fox[:cut]...)

		r, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}

		if _, err = io.ReadAll(r); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("second stream cut at %d bytes: %v; want io.ErrUnexpectedEOF", cut, err)
		}
	}
}

// blockHeader is a 12-byte block header for the writer's one-block file:
// the size byte, flags, the size fields given, the LZMA2 filter with the
// dictionary code dc, zero padding and the CRC-32.
func blockHeader(flags byte, sizes []byte, dc byte) []byte {
	h := append([]byte{0x02, flags}, sizes...)
	h = append(h, 0x21, 0x01, dc)

	for len(h) < 8 {
		h = append(h, 0)
	}

	var crc [4]byte

	binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(h))

	return append(h, crc[:]...)
}

// TestClassesBlockHeader: a block header the CRC-32 vouches for and the
// block does not bear out is corrupt, in both readers: sizes it declares
// that the block does not reach, and fields that run past its end.
func TestClassesBlockHeader(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write([]byte("The quick brown fox jumps over the lazy dog.")); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	file := buf.Bytes()
	dc := file[12+4]

	patched := func(h []byte) []byte {
		d := bytes.Clone(file)
		copy(d[12:24], h)

		return d
	}

	// The writer declares no sizes; 44 bytes uncompressed and 48 compressed
	// are what the block holds.
	bothReaders(t, "the real uncompressed size", patched(blockHeader(0x80, []byte{44}, dc)), nil)
	bothReaders(t, "uncompressed size one more", patched(blockHeader(0x80, []byte{45}, dc)), xz.ErrCorrupt)
	bothReaders(t, "uncompressed size one less", patched(blockHeader(0x80, []byte{43}, dc)), xz.ErrCorrupt)
	bothReaders(t, "compressed size one more", patched(blockHeader(0x40, []byte{49}, dc)), xz.ErrCorrupt)

	// An 8-byte header whose fields need more than its 4 bytes before the
	// CRC: a compressed size whose varint never ends, a filter ID likewise,
	// and filter flags cut after the properties size.
	short := func(body []byte) []byte {
		var crc [4]byte

		binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(body))

		d := bytes.Clone(file)
		d = append(d[:12], append(body, crc[:]...)...)

		return append(d, make([]byte, 4)...) // the index still fits the parallel reader's walk
	}

	bothReaders(t, "size varint past the end", short([]byte{0x01, 0x40, 0x80, 0x80}), xz.ErrCorrupt)
	bothReaders(t, "filter id past the end", short([]byte{0x01, 0x00, 0x80, 0x80}), xz.ErrCorrupt)
	bothReaders(t, "filter flags cut", short([]byte{0x01, 0x00, 0x21, 0x01}), xz.ErrCorrupt)
}

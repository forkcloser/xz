// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The bounded tests walk the implementation's boundaries: every input
// length around the dictionary, the chunk and the buffer sizes, through
// both writers and both readers and the reference decoder; every read size
// around the decoders' buffers; the decoders reused across streams.

package lzma_test

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/internal/lzmaspec"
	"github.com/forkcloser/xz/lzma"
)

// around is n-1, n and n+1, for n at least 1.
func around(n int) []int {
	return []int{n - 1, n, n + 1}
}

// lengths are the input lengths walked: the empty and the one-byte input,
// the dictionary's minimum (the window wraps there under the smallest
// configuration), the lookahead buffer's size, the match's maximum, and
// the LZMA2 chunk's uncompressed limit, each with its neighbours.
func lengths(withChunk bool) []int {
	ls := []int{0, 1, 2, 3}
	ls = append(ls, around(273)...)
	ls = append(ls, around(4096)...)
	ls = append(ls, around(2*4096)...)
	ls = append(ls, around(65536)...)

	if withChunk {
		ls = append(ls, around(1<<21)...)
	}

	return ls
}

// pattern is n bytes of text with a repeat the dictionary's size apart
// and a run of random bytes in the middle, so matches of every distance
// class and literals of every context occur.
func pattern(n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(n), 3))
	text := []byte("Jackdaws love my big sphinx of quartz; ")
	out := make([]byte, n)

	for i := range out {
		switch {
		case i >= n/2 && i < n/2+n/8:
			out[i] = byte(rng.Uint32())
		default:
			out[i] = text[i%len(text)]
		}
	}

	return out
}

// TestBoundedClassic: at every length, under the default and the smallest
// configuration, with and without the size in the header, the classic
// writer's stream decodes to the input through the reference and the
// reader, and the reader hands the bytes out the same through reads of
// one byte, a prime, and a whole buffer.
func TestBoundedClassic(t *testing.T) {
	t.Parallel()

	for _, n := range lengths(false) {
		data := pattern(n)

		configs := map[string]lzma.WriterConfig{
			"default":    {},
			"smallest":   {DictCap: lzma.MinDictCap, BufSize: 273},
			"size":       {SizeInHeader: true, Size: int64(n)},
			"small size": {DictCap: lzma.MinDictCap, BufSize: 273, SizeInHeader: true, Size: int64(n)},
		}

		for cname, c := range configs {
			stream := classic(t, c, data)

			out, err := lzmaspec.Decode(stream)
			if err != nil || !bytes.Equal(out.Data, data) {
				t.Fatalf("len %d, %s: the reference: %d bytes, %v", n, cname, len(out.Data), err)
			}

			for _, size := range []int{1, 7, 4096, 1 << 20} {
				r, err := lzma.NewReader(bytes.NewReader(stream))
				if err != nil {
					t.Fatalf("len %d, %s: %v", n, cname, err)
				}

				got, err := readBy(r, size)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("len %d, %s, reads of %d: %d bytes, %v", n, cname, size, len(got), err)
				}
			}
		}
	}
}

// readBy reads r to the end in pieces of size bytes.
func readBy(r io.Reader, size int) ([]byte, error) {
	var out []byte

	buf := make([]byte, size)

	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)

		if err != nil {
			if err == io.EOF {
				return out, nil
			}

			return out, err
		}
	}
}

// TestBoundedLZMA2: at every length, the chunk limit included, under the
// default and the smallest configuration, the chunk sequence decodes to
// the input through the reference and the reader; the sequence has a
// chunk boundary at every Flush, and the reader reads it by one byte and
// whole.
func TestBoundedLZMA2(t *testing.T) {
	t.Parallel()

	for _, n := range lengths(true) {
		data := pattern(n)

		configs := map[string]lzma.Writer2Config{
			"default":  {},
			"smallest": {DictCap: lzma.MinDictCap, BufSize: 273},
		}

		for cname, c := range configs {
			for _, cut := range cuts(n) {
				stream := lzma2(t, c, data, cut...)

				dictCap := c.DictCap
				if dictCap == 0 {
					dictCap = 8 << 20
				}

				got, err := lzmaspec.DecodeLZMA2(stream, uint32(dictCap))
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("len %d, %s, cuts %v: the reference: %d bytes, %v", n, cname, cut, len(got), err)
				}

				for _, size := range []int{1, 1 << 20} {
					if n > 1<<16 && size == 1 {
						continue // a byte at a time over a megabyte says nothing more
					}

					r, err := lzma.Reader2Config{DictCap: c.DictCap}.NewReader2(bytes.NewReader(stream))
					if err != nil {
						t.Fatalf("len %d, %s: %v", n, cname, err)
					}

					got, err := readBy(r, size)
					if err != nil || !bytes.Equal(got, data) {
						t.Fatalf("len %d, %s, cuts %v, reads of %d: %d bytes, %v", n, cname, cut, size, len(got), err)
					}
				}
			}
		}
	}
}

// TestBoundedLZMA2Compressible: the most compressible input, zeros, at the
// chunk's uncompressed limit and around it: one chunk reaches the limit
// before the compressed one, which the mixed pattern never does.
func TestBoundedLZMA2Compressible(t *testing.T) {
	t.Parallel()

	for _, n := range around(1 << 21) {
		data := make([]byte, n)
		stream := lzma2(t, lzma.Writer2Config{}, data)

		got, err := lzmaspec.DecodeLZMA2(stream, 8<<20)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("len %d: the reference: %d bytes, %v", n, len(got), err)
		}

		r, err := lzma.NewReader2(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}

		if got, err = io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("len %d: %d bytes, %v", n, len(got), err)
		}
	}
}

// TestBoundedReader2Reuse: one reader reset over every length's sequence
// in turn decodes each to its input, with the dictionary grown by the
// largest and the smallest after.
func TestBoundedReader2Reuse(t *testing.T) {
	t.Parallel()

	ls := lengths(false)
	order := append(append([]int{}, ls...), 1<<17, 0, 1, 4096)

	r, err := lzma.NewReader2(bytes.NewReader(lzma2(t, lzma.Writer2Config{}, []byte{})))
	if err != nil {
		t.Fatal(err)
	}

	for _, n := range order {
		data := pattern(n)
		stream := lzma2(t, lzma.Writer2Config{}, data, cuts(n)[len(cuts(n))-1]...)

		r.Reset(bytes.NewReader(stream))

		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("len %d after reuse: %d bytes, %v", n, len(got), err)
		}

		if !r.EOS() {
			t.Fatalf("len %d: no end of stream", n)
		}
	}
}

// TestBoundedWriterSize: the classic writer with a declared size takes
// exactly that many bytes, in writes around the size, refuses more with
// ErrNoSpace, and refuses to close short.
func TestBoundedWriterSize(t *testing.T) {
	t.Parallel()

	data := pattern(5001)

	for _, size := range []int{1, 2, 4095, 4096, 4097, 5000} {
		var buf bytes.Buffer

		w, err := lzma.WriterConfig{SizeInHeader: true, Size: int64(size)}.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}

		if size > 1 {
			if _, err = w.Write(data[:size-1]); err != nil {
				t.Fatalf("size %d: %v", size, err)
			}

			if err = w.Close(); err == nil {
				t.Fatalf("size %d: Close one byte short returned nil", size)
			}
		}

		if _, err = w.Write(data[size-1 : size]); err != nil {
			t.Fatalf("size %d: the last byte: %v", size, err)
		}

		if n, werr := w.Write(data[size : size+1]); n != 0 || werr == nil {
			t.Fatalf("size %d: a byte past the size: %d, %v", size, n, werr)
		}

		if err = w.Close(); err != nil {
			t.Fatalf("size %d: Close: %v", size, err)
		}

		out, err := lzmaspec.Decode(buf.Bytes())
		if err != nil || !bytes.Equal(out.Data, data[:size]) || out.Marker {
			t.Fatalf("size %d: the reference: %d bytes, marker %v, %v", size, len(out.Data), out.Marker, err)
		}
	}
}

// TestBoundedStreaming: the readers decode through a source that hands
// out one byte per Read, so no read of theirs is ever whole.
func TestBoundedStreaming(t *testing.T) {
	t.Parallel()

	data := pattern(10000)

	r, err := lzma.NewReader(&oneByteReader{bytes.NewReader(classic(t, lzma.WriterConfig{}, data))})
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("classic: %d bytes, %v", len(got), err)
	}

	r2, err := lzma.NewReader2(&oneByteReader{bytes.NewReader(lzma2(t, lzma.Writer2Config{}, data, 5000))})
	if err != nil {
		t.Fatal(err)
	}

	got, err = io.ReadAll(r2)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("lzma2: %d bytes, %v", len(got), err)
	}
}

// oneByteReader hands out one byte per Read.
type oneByteReader struct {
	r io.Reader
}

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	return o.r.Read(p[:1])
}

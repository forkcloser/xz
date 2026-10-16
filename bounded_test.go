// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The bounded tests walk the implementation's boundaries: the writer's
// block cuts at and around its block size across several writes, the
// parallel reader's block buffer at and around its initial size and its
// worker count against the block count, read sizes around the readers'
// buffers, and a file's streams with different check types.

package xz_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/forkcloser/xz"
)

// writeAll writes data through w in the given pieces and closes it.
func writeAll(tb testing.TB, c xz.WriterConfig, pieces ...[]byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := c.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	for _, p := range pieces {
		if _, err = w.Write(p); err != nil {
			tb.Fatal(err)
		}
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// TestBoundedWriterBlocks: the writer cuts blocks at its block size however
// the writes fall: a write of the size, one past it, two of the size, a
// zero-length write at a boundary, an empty input, and writes that cross
// the boundary; the reference parses each and counts the blocks.
func TestBoundedWriterBlocks(t *testing.T) {
	t.Parallel()

	const size = 1024

	data := cData(3 * size)
	c := xz.WriterConfig{BlockSize: size}

	cases := []struct {
		name   string
		pieces [][]byte
		blocks int
	}{
		{"empty", [][]byte{{}}, 1},
		{"one byte", [][]byte{data[:1]}, 1},
		{"the size", [][]byte{data[:size]}, 1},
		{"one past", [][]byte{data[:size+1]}, 2},
		{"twice the size", [][]byte{data[:size], data[size : 2*size]}, 2},
		{"zero-length at the boundary", [][]byte{data[:size], {}}, 1},
		{"crossing", [][]byte{data[:1000], data[1000:1048]}, 2},
		{"three and a bit", [][]byte{data[:3*size], data[:5]}, 4},
	}

	for _, tc := range cases {
		var want []byte
		for _, p := range tc.pieces {
			want = append(want, p...)
		}

		file := writeAll(t, c, tc.pieces...)

		got, err := refParse(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: the reference: %d bytes, %v", tc.name, len(got), err)
		}

		if blocks := refCountBlocks(file); blocks != tc.blocks {
			t.Fatalf("%s: %d blocks, want %d", tc.name, blocks, tc.blocks)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) || parallel.err != nil ||
			!bytes.Equal(parallel.data, want) {
			t.Fatalf(
				"%s: sequential %d bytes %v; parallel %d bytes %v",
				tc.name,
				len(sequential.data),
				sequential.err,
				len(parallel.data),
				parallel.err,
			)
		}
	}
}

// TestBoundedParallelBlocks: the parallel reader decodes blocks of every
// size around its initial block buffer (1 MiB), with every worker count
// against every block count, and by reads of one byte, a prime and a whole
// buffer, to the same bytes as the reference.
func TestBoundedParallelBlocks(t *testing.T) {
	t.Parallel()

	const initial = 1 << 20

	for _, blockSize := range []int64{initial - 1, initial, initial + 1, 2*initial + 1} {
		data := cData(int(2*blockSize) + 3)
		file := writeAll(t, xz.WriterConfig{BlockSize: blockSize}, data)

		for _, workers := range []int{1, 2, 5} {
			r, err := xz.ParallelReaderConfig{
				Workers: workers,
			}.NewParallelReader(
				bytes.NewReader(file),
				int64(len(file)),
			)
			if err != nil {
				t.Fatal(err)
			}

			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("block %d, %d workers: %d bytes, %v", blockSize, workers, len(got), err)
			}

			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}

	data := cData(100000)
	file := writeAll(t, xz.WriterConfig{BlockSize: 4096}, data)

	for _, size := range []int{1, 7, 4096, 1 << 20} {
		r, err := xz.ParallelReaderConfig{Workers: 3}.NewParallelReader(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}

		got, err := readBy(r, size)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("reads of %d: %d bytes, %v", size, len(got), err)
		}

		sr, err := xz.NewReader(bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}

		got, err = readBy(sr, size)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("sequential reads of %d: %d bytes, %v", size, len(got), err)
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

// TestBoundedStreams: a file of several streams, each with its own check
// type and block layout, with and without padding, decodes whole through
// both readers, and SingleStream stops after the first.
func TestBoundedStreams(t *testing.T) {
	t.Parallel()

	parts := [][]byte{cData(5000), cData(1), {}, cData(70000)}
	streams := make([][]byte, 0, len(parts))

	for i, part := range parts {
		check := []byte{xz.None, xz.CRC32, xz.CRC64, xz.SHA256}[i]
		streams = append(
			streams,
			refStream(refOptions{check: check, sizes: i%2 == 0, dictCode: 30}, refBlocks(t, part, 3000)),
		)
	}

	for _, padding := range []int{0, 4, 12} {
		file := refFile(padding, streams...)

		want, err := refParse(file)
		if err != nil {
			t.Fatal(err)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) {
			t.Fatalf("padding %d: sequential: %d bytes, %v", padding, len(sequential.data), sequential.err)
		}

		if parallel.err != nil || !bytes.Equal(parallel.data, want) {
			t.Fatalf("padding %d: parallel: %d bytes, %v", padding, len(parallel.data), parallel.err)
		}

		r, err := xz.ReaderConfig{SingleStream: true}.NewReader(bytes.NewReader(file))
		if err != nil {
			t.Fatal(err)
		}

		got, err := io.ReadAll(r)
		if !bytes.Equal(got, parts[0]) || (err == nil) != (padding == 0 && len(streams) == 1) {
			t.Fatalf(
				"padding %d, single stream: %d bytes, %v; want the first stream then a refusal of what follows",
				padding,
				len(got),
				err,
			)
		}
	}
}

// TestBoundedWriterSizes: the writer's output for inputs around the
// encoder's buffers, in one write, parses under the reference to the
// input.
func TestBoundedWriterSizes(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 4095, 4096, 4097, 65535, 65536, 65537, 1<<21 - 1, 1 << 21, 1<<21 + 1} {
		data := cData(n)

		got, err := refParse(writeAll(t, xz.WriterConfig{}, data))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("len %d: the reference: %d bytes, %v", n, len(got), err)
		}
	}
}

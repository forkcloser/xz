// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The contract tests hold the package to the .xz file format specification
// through the reference container (reference_test.go), written from the
// specification and decoding its payload with the reference LZMA decoder,
// so nothing here is checked against the package itself:
//
//   - What the writer produces, the reference parses to the input, with
//     the check type, the block count and the sizes the configuration asked
//     for.
//   - What the reference writes, both readers decode to the input: every
//     check type, sizes present and absent, one block, several, empty
//     blocks, several streams with and without padding.
//   - What the reference refuses, both readers refuse, as ErrCorrupt,
//     ErrUnsupported or io.ErrUnexpectedEOF; what it parses, both decode to
//     the same bytes: every byte flipped, every byte zeroed and every
//     truncation of a corpus of files, and an index whose records lie
//     about a block under a recomputed CRC.
//   - The parallel reader's WriteTo delivers the same bytes and counts
//     them; a writer's failure, or a writer that accepts nothing, ends it.

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/forkcloser/xz"
	"github.com/forkcloser/xz/lzma"
)

// cData is n bytes of text with a random run in the middle.
func cData(n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(n), 5))
	text := []byte("Sphinx of black quartz, judge my vow. ")
	out := make([]byte, n)

	for i := range out {
		if i >= n/2 && i < n/2+n/8 {
			out[i] = byte(rng.Uint32())
		} else {
			out[i] = text[i%len(text)]
		}
	}

	return out
}

// lzma2Of is data as an LZMA2 chunk sequence from the lzma package, whose
// own contract holds that output to the reference decoder.
func lzma2Of(tb testing.TB, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf)
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

// refBlocks cuts data into blocks of at most size bytes.
func refBlocks(tb testing.TB, data []byte, size int) []refBlock {
	tb.Helper()

	var blocks []refBlock

	for pos := 0; pos < len(data) || len(blocks) == 0; pos += size {
		end := min(pos+size, len(data))
		blocks = append(blocks, refBlock{data: data[pos:end], lzma2: lzma2Of(tb, data[pos:end])})

		if size <= 0 {
			break
		}
	}

	return blocks
}

// checks are the check types by name.
func checks() map[string]byte {
	return map[string]byte{"none": xz.None, "crc32": xz.CRC32, "crc64": xz.CRC64, "sha256": xz.SHA256}
}

// both decodes data through the sequential and the parallel reader and
// returns each verdict.
func both(data []byte) (sequential, parallel verdict) {
	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		sequential = verdict{err: err}
	} else {
		out, rerr := io.ReadAll(r)
		sequential = verdict{data: out, err: rerr}
	}

	pr, err := xz.NewParallelReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return sequential, verdict{err: err}
	}

	out, err := io.ReadAll(pr)
	parallel = verdict{data: out, err: err}

	_ = pr.Close()

	return sequential, parallel
}

// verdict is what a decoder made of a file: the bytes, or the failure.
type verdict struct {
	data []byte
	err  error
}

// TestContractWriter: the writer's files parse under the reference, with
// the check type asked for, the sizes in every block header, and as many
// blocks as the block size cuts.
func TestContractWriter(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 5000, 70000} {
		data := cData(n)

		for cname, check := range checks() {
			for _, blockSize := range []int64{0, 1024, 65536} {
				var buf bytes.Buffer

				c := xz.WriterConfig{CheckSum: check, NoCheckSum: check == xz.None, BlockSize: blockSize}

				w, err := c.NewWriter(&buf)
				if err != nil {
					t.Fatal(err)
				}

				if _, err = w.Write(data); err != nil {
					t.Fatal(err)
				}

				if err = w.Close(); err != nil {
					t.Fatal(err)
				}

				file := buf.Bytes()

				got, err := refParse(file)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("len %d, %s, block %d: the reference: %d bytes, %v", n, cname, blockSize, len(got), err)
				}

				if file[7] != check {
					t.Fatalf("len %d, %s: the stream flags say check %#x", n, cname, file[7])
				}

				want := 1
				if blockSize > 0 && n > 0 {
					want = (n + int(blockSize) - 1) / int(blockSize)
				}

				if blocks := refCountBlocks(file); blocks != want {
					t.Fatalf("len %d, %s, block %d: %d blocks, want %d", n, cname, blockSize, blocks, want)
				}
			}
		}
	}
}

// refCountBlocks counts a one-stream file's blocks by its index.
func refCountBlocks(file []byte) int {
	// The footer's backward size locates the index; its count follows the
	// indicator byte.
	backward := int(
		uint32(
			file[len(file)-8],
		) | uint32(
			file[len(file)-7],
		)<<8 | uint32(
			file[len(file)-6],
		)<<16 | uint32(
			file[len(file)-5],
		)<<24,
	)
	indexStart := len(file) - 12 - (backward+1)*4
	p := &refParser{data: file, pos: indexStart + 1}

	count, err := p.vli()
	if err != nil {
		return -1
	}

	return int(count)
}

// TestContractReaders: both readers decode every reference file to its
// data: every check type, sizes present and absent, one block, several
// and an empty one, several streams with and without padding.
func TestContractReaders(t *testing.T) {
	t.Parallel()

	data := cData(30000)

	for cname, check := range checks() {
		for _, sizes := range []bool{false, true} {
			o := refOptions{check: check, sizes: sizes, dictCode: 35}

			files := map[string][]byte{
				"one block": refStream(o, refBlocks(t, data, 0)),
				"blocks":    refStream(o, refBlocks(t, data, 7000)),
				"empty":     refStream(o, refBlocks(t, []byte{}, 0)),
				"no blocks": refStream(o, nil),
				"streams": refFile(
					0,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, refBlocks(t, data[10000:], 3000)),
				),
				"padded": refFile(
					8,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, refBlocks(t, data[10000:], 0)),
				),
				"empty between": refFile(
					4,
					refStream(o, refBlocks(t, data[:10000], 0)),
					refStream(o, nil),
					refStream(o, refBlocks(t, data[10000:], 0)),
				),
			}

			for fname, file := range files {
				want, err := refParse(file)
				if err != nil {
					t.Fatalf("%s, sizes %v, %s: the reference refuses its own file: %v", cname, sizes, fname, err)
				}

				sequential, parallel := both(file)
				if sequential.err != nil || !bytes.Equal(sequential.data, want) {
					t.Fatalf(
						"%s, sizes %v, %s: sequential: %d bytes, %v",
						cname,
						sizes,
						fname,
						len(sequential.data),
						sequential.err,
					)
				}

				if parallel.err != nil || !bytes.Equal(parallel.data, want) {
					t.Fatalf(
						"%s, sizes %v, %s: parallel: %d bytes, %v",
						cname,
						sizes,
						fname,
						len(parallel.data),
						parallel.err,
					)
				}
			}
		}
	}
}

// classify requires a reader's verdict to follow the reference's: where
// the reference parses, the same bytes; where it refuses, a refusal that
// matches one of the package's sentinels.
func classify(t *testing.T, what string, ref, got verdict) {
	t.Helper()

	if ref.err == nil {
		if got.err != nil || !bytes.Equal(got.data, ref.data) {
			t.Fatalf(
				"%s: the reference parses %d bytes; the reader: %d bytes, %v",
				what,
				len(ref.data),
				len(got.data),
				got.err,
			)
		}

		return
	}

	switch {
	case got.err == nil:
		t.Fatalf("%s: the reference refuses (%v); the reader decodes %d bytes", what, ref.err, len(got.data))
	case errors.Is(got.err, xz.ErrCorrupt),
		errors.Is(got.err, xz.ErrUnsupported),
		errors.Is(got.err, io.ErrUnexpectedEOF):
	default:
		t.Fatalf("%s: the reader's error matches no sentinel: %v", what, got.err)
	}
}

// TestContractMalformed: on every byte flip, every byte zeroed and every
// truncation of a small corpus of reference files, both readers follow the
// reference's verdict. A zeroed byte is a size field claiming nothing where
// a flip only claims more.
func TestContractMalformed(t *testing.T) {
	t.Parallel()

	data := cData(600)

	files := map[string][]byte{
		"crc32 sizes":  refStream(refOptions{check: xz.CRC32, sizes: true, dictCode: 20}, refBlocks(t, data, 250)),
		"crc64":        refStream(refOptions{check: xz.CRC64, dictCode: 20}, refBlocks(t, data, 0)),
		"sha256 empty": refStream(refOptions{check: xz.SHA256, sizes: true, dictCode: 20}, refBlocks(t, []byte{}, 0)),
		"no blocks":    refStream(refOptions{check: xz.CRC32, sizes: true, dictCode: 20}, nil),
		"none streams": refFile(
			4,
			refStream(refOptions{check: xz.None, dictCode: 20}, refBlocks(t, data[:300], 0)),
			refStream(refOptions{check: xz.None, sizes: true, dictCode: 20}, refBlocks(t, data[300:], 0)),
		),
	}

	for fname, file := range files {
		bad := make([]byte, len(file))

		for i := range file {
			for _, mask := range []byte{0x01, 0x80, 0xff, file[i]} {
				copy(bad, file)
				bad[i] ^= mask

				ref, err := refParse(bad)
				sequential, parallel := both(bad)
				what := fname + ": byte " + strconv.Itoa(i) + " ^ " + strconv.Itoa(int(mask))
				classify(t, what+", sequential", verdict{ref, err}, sequential)
				classify(t, what+", parallel", verdict{ref, err}, parallel)
			}
		}

		for n := range file {
			ref, err := refParse(file[:n])
			sequential, parallel := both(file[:n])
			what := fname + ": cut at " + strconv.Itoa(n)
			classify(t, what+", sequential", verdict{ref, err}, sequential)
			classify(t, what+", parallel", verdict{ref, err}, parallel)
		}
	}
}

// rawBlock is data as one uncompressed LZMA2 chunk, so the block's size is
// the data's plus a fixed four: every residue of the unpadded size modulo
// four is one data length away.
func rawBlock(data []byte) refBlock {
	lzma2 := []byte{0x01, byte((len(data) - 1) >> 8), byte(len(data) - 1)}
	lzma2 = append(lzma2, data...)

	return refBlock{data: data, lzma2: append(lzma2, 0x00)}
}

// TestContractAlignment: blocks whose unpadded size takes every residue
// modulo four, alone and as the last of several, with and without a check,
// decode under both readers; the one that ends exactly where the index
// starts is as valid as the one that needs three bytes of padding.
func TestContractAlignment(t *testing.T) {
	t.Parallel()

	data := cData(16)

	for cname, check := range checks() {
		for n := 1; n <= 8; n++ {
			o := refOptions{check: check, dictCode: 20}
			files := map[string][]byte{
				"alone": refStream(o, []refBlock{rawBlock(data[:n])}),
				"last":  refStream(o, []refBlock{rawBlock(data[:5]), rawBlock(data[5 : 5+n])}),
			}

			for fname, file := range files {
				want, err := refParse(file)
				if err != nil {
					t.Fatalf("%s, %d bytes, %s: the reference refuses its own file: %v", cname, n, fname, err)
				}

				sequential, parallel := both(file)
				if sequential.err != nil || !bytes.Equal(sequential.data, want) {
					t.Fatalf("%s, %d bytes, %s: sequential: %v", cname, n, fname, sequential.err)
				}

				if parallel.err != nil || !bytes.Equal(parallel.data, want) {
					t.Fatalf("%s, %d bytes, %s: parallel: %v", cname, n, fname, parallel.err)
				}
			}
		}
	}
}

// TestContractLongPadding: stream padding longer than any read buffer,
// between streams and after the last, is still padding.
func TestContractLongPadding(t *testing.T) {
	t.Parallel()

	data := cData(2000)
	o := refOptions{check: xz.CRC32, dictCode: 20}

	for _, padding := range []int{4, 40 << 10, 70 << 10} {
		file := refFile(padding, refStream(o, refBlocks(t, data[:1000], 0)), refStream(o, refBlocks(t, data[1000:], 0)))
		file = append(file, make([]byte, padding)...)

		want, err := refParse(file)
		if err != nil {
			t.Fatalf("padding %d: the reference refuses its own file: %v", padding, err)
		}

		sequential, parallel := both(file)
		if sequential.err != nil || !bytes.Equal(sequential.data, want) {
			t.Fatalf("padding %d: sequential: %v", padding, sequential.err)
		}

		if parallel.err != nil || !bytes.Equal(parallel.data, want) {
			t.Fatalf("padding %d: parallel: %v", padding, parallel.err)
		}
	}
}

// countingWriter accepts everything up to its limit, then behaves as its
// mode says: accepts nothing without an error, or fails.
type countingWriter struct {
	buf   bytes.Buffer
	limit int
	fail  error
	stuck int // calls that took nothing
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		w.stuck++

		return 0, w.fail
	}

	k := min(len(p), w.limit-w.buf.Len())
	w.buf.Write(p[:k])

	if k < len(p) {
		return k, w.fail
	}

	return k, nil
}

// TestContractWriteTo: the parallel reader's WriteTo delivers the
// reference's bytes and counts them; after it, a Read is at the end and a
// second WriteTo writes nothing and succeeds, as io.Copy expects of a drained
// reader. A writer that fails ends it with that error and the count of
// bytes the writer took; one that takes nothing without an error ends it
// with io.ErrShortWrite on the second call, not after a spin.
func TestContractWriteTo(t *testing.T) {
	t.Parallel()

	data := cData(40000)
	file := refStream(refOptions{check: xz.CRC64, dictCode: 20}, refBlocks(t, data, 9000))

	want, err := refParse(file)
	if err != nil {
		t.Fatal(err)
	}

	errSink := errors.New("sink")

	for _, tc := range []struct {
		name  string
		limit int
		fail  error
		want  error
	}{
		{"whole", len(want), nil, nil},
		{"failing", 12345, errSink, errSink},
		{"stuck", 12345, nil, io.ErrShortWrite},
	} {
		pr, err := xz.NewParallelReader(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}

		w := &countingWriter{limit: tc.limit, fail: tc.fail}

		n, err := pr.WriteTo(w)
		if !errors.Is(err, tc.want) || n != int64(w.buf.Len()) || !bytes.HasPrefix(want, w.buf.Bytes()) {
			t.Fatalf("%s: WriteTo: %d, %v; the writer took %d bytes", tc.name, n, err, w.buf.Len())
		}

		if tc.want == nil {
			if k, err := pr.Read(make([]byte, 1)); k != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("%s: after WriteTo, Read: %d, %v", tc.name, k, err)
			}

			if n, err := pr.WriteTo(w); n != 0 || err != nil {
				t.Fatalf("%s: a second WriteTo: %d, %v", tc.name, n, err)
			}
		}

		if errors.Is(tc.want, io.ErrShortWrite) && w.stuck > 2 {
			t.Fatalf("%s: the stuck writer was asked %d times after taking nothing", tc.name, w.stuck)
		}

		_ = pr.Close()
	}
}

// lyingIndex returns file with one record of its one-stream index altered
// by one on the last byte of the field (0 the unpadded size, 1 the
// uncompressed size) and the index CRC-32 recomputed, so only the records
// themselves disagree with the blocks. The footer's CRC covers the
// backward size and the flags, not the index, and stands.
func lyingIndex(file []byte, record, field int) []byte {
	out := bytes.Clone(file)
	backward := int(binary.LittleEndian.Uint32(out[len(out)-8:]))
	start := len(out) - 12 - (backward+1)*4
	index := out[start : len(out)-12]

	p := &refParser{data: index, pos: 1}

	if _, err := p.vli(); err != nil {
		panic(err)
	}

	for i := 0; i <= record; i++ {
		for f := range 2 {
			if _, err := p.vli(); err != nil {
				panic(err)
			}

			if i == record && f == field {
				index[p.pos-1]++
			}
		}
	}

	binary.LittleEndian.PutUint32(index[len(index)-4:], crc32.ChecksumIEEE(index[:len(index)-4]))

	return out
}

// TestContractLyingIndex: an index whose record disagrees with the block it
// describes, under a valid index CRC, is the file a hostile writer makes;
// the reference refuses it and so do both readers, whether the lie is the
// unpadded size, by which the parallel reader locates the blocks, or the
// uncompressed size, in the first record or the last.
func TestContractLyingIndex(t *testing.T) {
	t.Parallel()

	data := cData(600)

	for cname, check := range checks() {
		for _, sizes := range []bool{false, true} {
			file := refStream(refOptions{check: check, sizes: sizes, dictCode: 20}, refBlocks(t, data, 250))

			if _, err := refParse(file); err != nil {
				t.Fatal(err)
			}

			for record := range 3 {
				for field, fname := range []string{"unpadded", "uncompressed"} {
					bad := lyingIndex(file, record, field)
					what := cname + ", sizes " + strconv.FormatBool(sizes) +
						": record " + strconv.Itoa(record) + " " + fname

					if _, err := refParse(bad); err == nil {
						t.Fatalf("%s: the reference accepts the lie", what)
					}

					ref, err := refParse(bad)
					sequential, parallel := both(bad)
					classify(t, what+", sequential", verdict{ref, err}, sequential)
					classify(t, what+", parallel", verdict{ref, err}, parallel)
				}
			}
		}
	}
}

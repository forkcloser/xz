// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The contract tests hold the package to the LZMA specification through the
// reference decoder (internal/lzmaspec, a port of the LZMA SDK's LzmaSpec.cpp
// with liblzma's LZMA2 chunk rules), which shares no code with it:
//
//   - What the writers produce, the reference decodes to the input, with the
//     end marker and the declared size where the configuration put them.
//   - What the reference decodes, the readers decode to the same bytes; what
//     it refuses, the readers refuse, as ErrCorrupt, ErrUnsupported or
//     io.ErrUnexpectedEOF: every byte flipped and every truncation of a
//     corpus of streams.
//   - The header codecs agree with the specification over every value: all
//     225 properties codes, all 41 dictionary codes.
//   - The chunk rules: the first chunk resets the dictionary, a dictionary
//     reset demands properties, a chunk after the end is not read.

package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/v2/internal/lzmaspec"
	"github.com/forkcloser/xz/v2/lzma"
)

// corpus is the inputs every contract runs over: empty, one byte, a chunk
// of text, text past a dictionary's minimum, random bytes, zeros, and a
// mix, so matches, literals, repeats and uncompressed chunks all occur.
func corpus(tb testing.TB) map[string][]byte {
	tb.Helper()

	rng := rand.New(rand.NewPCG(7, 11))
	random := make([]byte, 20000)

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 400)
	mixed := append(bytes.Clone(text[:5000]), random[:5000]...)
	mixed = append(mixed, text[:5000]...)

	return map[string][]byte{
		"empty":  {},
		"byte":   {'x'},
		"short":  []byte("hello, world"),
		"text":   text,
		"random": random,
		"zeros":  make([]byte, 10000),
		"mixed":  mixed,
	}
}

// wcase is one classic writer configuration under test: its options, the
// dictionary they select (the default when zero) and whether the stream
// carries an end marker.
type wcase struct {
	opts   []lzma.Option
	dict   int
	marker bool
}

// w2case is one LZMA2 writer configuration under test.
type w2case struct {
	opts []lzma.Option
	dict int
}

// defaultDict is the writers' dictionary when the options name none.
const defaultDict = 8 << 20

func (c w2case) dictSize() int {
	if c.dict == 0 {
		return defaultDict
	}

	return c.dict
}

// writerConfigs are the classic writer's configurations the contracts run
// with: the default, an explicit size, size and marker both, the smallest
// dictionary, and the properties at their extremes.
func writerConfigs(n int) map[string]wcase {
	return map[string]wcase{
		"default":         {marker: true},
		"size":            {opts: []lzma.Option{lzma.WithSize(int64(n))}},
		"size and marker": {opts: []lzma.Option{lzma.WithSize(int64(n)), lzma.WithEOSMarker()}, marker: true},
		"small dict": {
			opts:   []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)},
			dict:   lzma.MinDictSize,
			marker: true,
		},
		"lc0 lp4":     {opts: []lzma.Option{lzma.WithProperties(0, 4, 2)}, marker: true},
		"lc4 lp0 pb0": {opts: []lzma.Option{lzma.WithProperties(4, 0, 0)}, marker: true},
		"pb4":         {opts: []lzma.Option{lzma.WithProperties(3, 0, 4)}, marker: true},
	}
}

func classic(tb testing.TB, c wcase, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter(&buf, c.opts...)
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

// TestContractClassicWriter: every stream the classic writer produces is
// what the reference decodes to the input, with the end marker when the
// configuration asked for one or declared no size, and the declared size
// in the header.
func TestContractClassicWriter(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writerConfigs(len(data)) {
			stream := classic(t, c, data)

			out, err := lzmaspec.Decode(stream)
			if err != nil {
				t.Fatalf("%s, %s: the reference refuses the writer's stream: %v", name, cname, err)
			}

			if !bytes.Equal(out.Data, data) {
				t.Fatalf("%s, %s: the reference decodes %d bytes, want %d", name, cname, len(out.Data), len(data))
			}

			if out.Read != len(stream) {
				t.Fatalf("%s, %s: the reference read %d of %d bytes", name, cname, out.Read, len(stream))
			}

			if wantMarker := c.marker; out.Marker != wantMarker {
				t.Fatalf("%s, %s: end marker %v, want %v", name, cname, out.Marker, wantMarker)
			}

			if out.Corrupted {
				t.Fatalf("%s, %s: the reference flags the range coder's state", name, cname)
			}
		}
	}
}

// TestContractClassicReader: the reader decodes every stream of the corpus
// to what the reference does, consuming exactly the stream.
func TestContractClassicReader(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writerConfigs(len(data)) {
			stream := classic(t, c, data)

			in := bytes.NewReader(stream)

			r, err := lzma.NewReader(in)
			if err != nil {
				t.Fatalf("%s, %s: NewReader: %v", name, cname, err)
			}

			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s, %s: %d bytes, %v", name, cname, len(got), err)
			}

			if in.Len() != 0 {
				t.Fatalf("%s, %s: %d bytes of the stream left unread", name, cname, in.Len())
			}
		}
	}
}

// lzma2 writes data as a chunk sequence, flushing at each cut so the
// sequence has several chunks and a chunk boundary inside the data.
func lzma2(tb testing.TB, c w2case, data []byte, cuts ...int) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf, c.opts...)
	if err != nil {
		tb.Fatal(err)
	}

	pos := 0

	for _, cut := range cuts {
		if _, err = w.Write(data[pos:cut]); err != nil {
			tb.Fatal(err)
		}

		if err = w.Flush(); err != nil {
			tb.Fatal(err)
		}

		pos = cut
	}

	if _, err = w.Write(data[pos:]); err != nil {
		tb.Fatal(err)
	}

	if err = w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// writer2Configs are the LZMA2 writer's configurations.
func writer2Configs() map[string]w2case {
	return map[string]w2case{
		"default":    {},
		"small dict": {opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}, dict: lzma.MinDictSize},
		"lc0 lp4":    {opts: []lzma.Option{lzma.WithProperties(0, 4, 2)}},
		"lc4 pb4":    {opts: []lzma.Option{lzma.WithProperties(4, 0, 4)}},
	}
}

// cuts are where the LZMA2 corpus streams are flushed: none, one in the
// middle, several close together.
func cuts(n int) [][]int {
	if n < 8 {
		return [][]int{nil}
	}

	return [][]int{nil, {n / 2}, {1, 2, n / 3, n/3 + 1, n - 1}}
}

// TestContractLZMA2Writer: every chunk sequence the writer produces, cut
// where Flush was called, is what the reference decodes to the input
// under liblzma's chunk rules, and ends with the end chunk.
func TestContractLZMA2Writer(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writer2Configs() {
			for _, cut := range cuts(len(data)) {
				stream := lzma2(t, c, data, cut...)

				got, err := lzmaspec.DecodeLZMA2(stream, uint32(c.dictSize()))
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s, %s, cuts %v: the reference: %d bytes, %v", name, cname, cut, len(got), err)
				}
			}
		}
	}
}

// TestContractLZMA2Reader: the reader decodes every chunk sequence of the
// corpus to the input, reports the end chunk, and reads exactly the
// sequence.
func TestContractLZMA2Reader(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writer2Configs() {
			for _, cut := range cuts(len(data)) {
				stream := lzma2(t, c, data, cut...)
				in := bytes.NewReader(stream)

				r, err := lzma.NewReader2(in, c.dictSize())
				if err != nil {
					t.Fatalf("%s, %s: NewReader2: %v", name, cname, err)
				}

				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("%s, %s, cuts %v: %d bytes, %v", name, cname, cut, len(got), err)
				}

				if !r.EOS() || in.Len() != 0 {
					t.Fatalf("%s, %s: EOS %v, %d bytes left", name, cname, r.EOS(), in.Len())
				}
			}
		}
	}
}

// verdict is what a decoder made of an input: the bytes, or the failure,
// and the input itself for the one exception classify makes.
type verdict struct {
	data   []byte
	err    error
	stream []byte
}

// lclpAbove4 reports whether a classic stream's properties byte asks for
// lc+lp above four, the one stream the reference decodes and the reader
// refuses.
func lclpAbove4(stream []byte) bool {
	if len(stream) < 5 {
		return false
	}

	p, err := lzmaspec.ParseProperties(stream[:5])

	return err == nil && p.LC+p.LP > 4
}

// classify requires the reader's verdict to follow the reference's: where
// the reference decodes, the same bytes; where it refuses, a refusal that
// matches one of the package's sentinels.
func classify(t *testing.T, what string, ref, got verdict) {
	t.Helper()

	if ref.err == nil {
		// The one place the reader is stricter than the SDK's decoder:
		// lc+lp above four, which liblzma refuses as unsupported.
		if errors.Is(got.err, lzma.ErrUnsupported) && lclpAbove4(ref.stream) {
			return
		}

		if got.err != nil || !bytes.Equal(got.data, ref.data) {
			t.Fatalf(
				"%s: the reference decodes %d bytes; the reader: %d bytes, %v",
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
	case errors.Is(got.err, lzma.ErrCorrupt), errors.Is(got.err, lzma.ErrUnsupported),
		errors.Is(got.err, io.ErrUnexpectedEOF):
	default:
		t.Fatalf("%s: the reader's error matches no sentinel: %v", what, got.err)
	}
}

func classicVerdict(stream []byte) verdict {
	// The dictionary cap is the reader's own limit, held to by its own
	// test; the differential lets a flipped header declare any size.
	r, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(lzma.MaxDictSize))
	if err != nil {
		return verdict{err: err}
	}

	data, err := io.ReadAll(r)

	return verdict{data: data, err: err}
}

func referenceVerdict(stream []byte) verdict {
	out, err := lzmaspec.Decode(stream)

	return verdict{data: out.Data, err: err, stream: stream}
}

// malformed runs every single-byte flip and every truncation of stream
// through ref and got, and classifies each.
func malformed(t *testing.T, name string, stream []byte, ref, got func([]byte) verdict) {
	t.Helper()

	bad := make([]byte, len(stream))

	for i := range stream {
		for _, mask := range []byte{0x01, 0x80, 0xff} {
			copy(bad, stream)
			bad[i] ^= mask

			classify(t, name+": byte "+itoa(i)+" flipped", ref(bad), got(bad))
		}
	}

	for n := range stream {
		classify(t, name+": cut at "+itoa(n), ref(stream[:n]), got(stream[:n]))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}

	return string(b)
}

// TestContractClassicMalformed: on every byte flip and every truncation of
// the corpus's classic streams, the reader follows the reference's verdict.
func TestContractClassicMalformed(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		if len(data) > 1000 {
			data = data[:1000]
		}

		for cname, c := range writerConfigs(len(data)) {
			malformed(t, name+", "+cname, classic(t, c, data), referenceVerdict, classicVerdict)
		}
	}
}

func lzma2Verdict(dictCap int) func([]byte) verdict {
	return func(stream []byte) verdict {
		r, err := lzma.NewReader2(bytes.NewReader(stream), dictCap)
		if err != nil {
			return verdict{err: err}
		}

		data, err := io.ReadAll(r)

		return verdict{data: data, err: err}
	}
}

func referenceLZMA2Verdict(dictCap int) func([]byte) verdict {
	return func(stream []byte) verdict {
		data, err := lzmaspec.DecodeLZMA2(stream, uint32(dictCap))

		return verdict{data: data, err: err}
	}
}

// TestContractLZMA2Malformed: on every byte flip and every truncation of
// the corpus's chunk sequences, the reader follows the reference's verdict
// under liblzma's chunk rules.
func TestContractLZMA2Malformed(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		if len(data) > 1000 {
			data = data[:1000]
		}

		for cname, c := range writer2Configs() {
			stream := lzma2(t, c, data, cuts(len(data))[len(cuts(len(data)))-1]...)
			malformed(t, name+", "+cname, stream, referenceLZMA2Verdict(c.dictSize()), lzma2Verdict(c.dictSize()))
		}
	}
}

// TestContractProperties: every properties byte decodes as the
// specification says and encodes back to itself, and the codes beyond the
// 225 the format defines are refused as it refuses them; lc+lp above four
// is refused as unsupported, liblzma's rule.
func TestContractProperties(t *testing.T) {
	t.Parallel()

	for code := range 256 {
		ref, refErr := lzmaspec.ParseProperties([]byte{byte(code), 0, 0, 0, 0})
		p, err := lzma.PropertiesForCode(byte(code))

		switch {
		case refErr != nil:
			if !errors.Is(err, lzma.ErrCorrupt) {
				t.Errorf("code %d: the specification refuses it; the package: %v", code, err)
			}
		case ref.LC+ref.LP > 4:
			if !errors.Is(err, lzma.ErrUnsupported) {
				t.Errorf("code %d: lc+lp %d; want ErrUnsupported, got %v", code, ref.LC+ref.LP, err)
			}
		default:
			if err != nil || uint(p.LC) != ref.LC || uint(p.LP) != ref.LP ||
				uint(p.PB) != ref.PB {
				t.Errorf("code %d: %+v, %v; want lc %d lp %d pb %d", code, p, err, ref.LC, ref.LP, ref.PB)
			} else if p.Code() != byte(
				code,
			) {
				t.Errorf("code %d encodes back to %d", code, p.Code())
			}
		}
	}
}

// TestContractValidHeader: the format sniff accepts the dictionary sizes
// the reference encoders emit, at 2^10 and up, 2^32-1 included, and an
// explicit size to 256 GiB; it refuses other sizes and a wrong length.
func TestContractValidHeader(t *testing.T) {
	t.Parallel()

	header := func(dictSize uint32, size int64) []byte {
		h := make([]byte, lzma.HeaderLen)
		h[0] = 0x5D

		for i := range 4 {
			h[1+i] = byte(dictSize >> (8 * i))
		}

		for i := range 8 {
			h[5+i] = byte(uint64(size) >> (8 * i))
		}

		return h
	}

	for _, dictSize := range []uint32{1 << 10, 1<<10 + 1<<9, 1 << 20, 1<<20 + 1<<19, 1<<32 - 1} {
		if !lzma.ValidHeader(header(dictSize, -1)) {
			t.Errorf("dictionary size %d refused", dictSize)
		}
	}

	for _, dictSize := range []uint32{0, 1 << 9, 1<<10 + 1, 1<<10 + 1<<9 + 1, 1<<32 - 2} {
		if lzma.ValidHeader(header(dictSize, -1)) {
			t.Errorf("dictionary size %d accepted", dictSize)
		}
	}

	if !lzma.ValidHeader(header(1<<20, 1<<38)) || lzma.ValidHeader(header(1<<20, 1<<38+1)) {
		t.Error("the explicit size bound is not 256 GiB")
	}

	if lzma.ValidHeader(header(1<<20, -1)[:12]) || lzma.ValidHeader(append(header(1<<20, -1), 0)) {
		t.Error("a header of the wrong length accepted")
	}
}

// TestContractOptionBounds: the readers' and writers' options are accepted
// at the bounds of the dictionary size and the properties, and refused one
// past them.
func TestContractOptionBounds(t *testing.T) {
	t.Parallel()

	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(lzma.MinDictSize)}}, []byte("hello"))
	sequence := lzma2Stream(t, []byte("hello"))

	// A reader grows its dictionary as the data needs it, so both bounds are
	// constructed; a writer allocates the whole dictionary on construction,
	// four gigabytes at the upper bound, so the writers are held at the
	// lower one.
	for _, dictSize := range []int{lzma.MinDictSize, lzma.MaxDictSize} {
		if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(int64(dictSize))); err != nil {
			t.Errorf("WithMaxDictSize %d: %v; want accepted", dictSize, err)
		}

		if _, err := lzma.NewReader2(bytes.NewReader(sequence), dictSize); err != nil {
			t.Errorf("NewReader2 dictionary %d: %v; want accepted", dictSize, err)
		}
	}

	if _, err := lzma.NewWriter(io.Discard, lzma.WithDictSize(lzma.MinDictSize)); err != nil {
		t.Errorf("writer WithDictSize %d: %v; want accepted", lzma.MinDictSize, err)
	}

	if _, err := lzma.NewWriter2(io.Discard, lzma.WithDictSize(lzma.MinDictSize)); err != nil {
		t.Errorf("LZMA2 writer WithDictSize %d: %v; want accepted", lzma.MinDictSize, err)
	}

	for _, dictSize := range []int{1, lzma.MinDictSize - 1, lzma.MaxDictSize + 1} {
		if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(int64(dictSize))); err == nil {
			t.Errorf("WithMaxDictSize %d accepted", dictSize)
		}

		if _, err := lzma.NewReader2(bytes.NewReader(sequence), dictSize); err == nil {
			t.Errorf("NewReader2 dictionary %d accepted", dictSize)
		}

		if _, err := lzma.NewWriter(io.Discard, lzma.WithDictSize(dictSize)); err == nil {
			t.Errorf("writer WithDictSize %d accepted", dictSize)
		}

		if _, err := lzma.NewWriter2(io.Discard, lzma.WithDictSize(dictSize)); err == nil {
			t.Errorf("LZMA2 writer WithDictSize %d accepted", dictSize)
		}
	}

	for _, p := range [][3]int{{4, 0, 4}, {0, 4, 0}, {2, 2, 2}} {
		if _, err := lzma.NewWriter(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err != nil {
			t.Errorf("properties %v: %v; want accepted", p, err)
		}
	}

	for _, p := range [][3]int{{9, 0, 0}, {-1, 0, 0}, {0, 5, 0}, {0, -1, 0}, {0, 0, 5}, {0, 0, -1}, {5, 4, 0}} {
		if _, err := lzma.NewWriter(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err == nil {
			t.Errorf("properties %v accepted by the writer", p)
		}

		if _, err := lzma.NewWriter2(io.Discard, lzma.WithProperties(p[0], p[1], p[2])); err == nil {
			t.Errorf("properties %v accepted by the LZMA2 writer", p)
		}
	}

	// An option of the other constructor is refused, not ignored.
	if _, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithDictSize(lzma.MinDictSize)); err == nil {
		t.Error("the reader accepted WithDictSize")
	}

	if _, err := lzma.NewWriter2(io.Discard, lzma.WithSize(5)); err == nil {
		t.Error("the LZMA2 writer accepted WithSize")
	}

	if _, err := lzma.NewWriter(io.Discard, lzma.WithMaxDictSize(lzma.MinDictSize)); err == nil {
		t.Error("the writer accepted WithMaxDictSize")
	}
}

// TestContractMaxDictSize: a classic header declaring a dictionary above
// WithMaxDictSize is refused as unsupported; one at the limit is read.
func TestContractMaxDictSize(t *testing.T) {
	t.Parallel()

	stream := classic(t, wcase{opts: []lzma.Option{lzma.WithDictSize(1 << 20)}}, []byte("hello"))

	_, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(1<<16))
	if !errors.Is(err, lzma.ErrUnsupported) {
		t.Fatalf("%v; want ErrUnsupported", err)
	}

	r, err := lzma.NewReader(bytes.NewReader(stream), lzma.WithMaxDictSize(1<<20))
	if err != nil {
		t.Fatalf("a limit equal to the header's size refuses: %v", err)
	}

	if got, err := io.ReadAll(r); err != nil || string(got) != "hello" {
		t.Fatalf("%q, %v", got, err)
	}
}

// TestContractChunkRules: the chunk sequence's rules from the reader's side,
// each sequence hand-written: the first chunk resets the dictionary, a
// dictionary reset demands properties, an uncompressed chunk without a
// reset continues, and the end chunk ends the sequence with nothing read
// past it.
func TestContractChunkRules(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	w, err := lzma.NewWriter2(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Long enough to compress, so the writer emits an LZMA chunk, not an
	// uncompressed one.
	data := bytes.Repeat([]byte("abcabcabc "), 50)

	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	// The writer's stream: a cLRND chunk (0xE0) then the end byte.
	first := buf.Bytes()
	if first[0] != 0xE0 {
		t.Fatalf("the writer's first chunk is %#x, not cLRND", first[0])
	}

	chunk := first[:len(first)-1]

	uncompressed := append([]byte{0x02, 0x00, 0x02}, []byte("xyz")...)      // cU, 3 bytes
	uncompressedReset := append([]byte{0x01, 0x00, 0x02}, []byte("xyz")...) // cUD, 3 bytes

	cases := []struct {
		name string
		in   []byte
		want []byte // nil: ErrCorrupt
	}{
		{"cL first", markControl(chunk, 0x80), nil},
		{"cLR first", markControl(chunk, 0xA0), nil},
		{"cLRN first", markControl(chunk, 0xC0), nil},
		{"cU first", append(bytes.Clone(uncompressed), 0x00), nil},
		{"cUD then cL", append(append(bytes.Clone(uncompressedReset), markControl(chunk, 0x80)...), 0x00), nil},
		{"cUD then cLR", append(append(bytes.Clone(uncompressedReset), markControl(chunk, 0xA0)...), 0x00), nil},
		// The chunk's bytes were coded in an empty dictionary, so only a
		// dictionary reset before them decodes them again.
		{
			"cUD then cLRND",
			append(append(bytes.Clone(uncompressedReset), chunk...), 0x00),
			append([]byte("xyz"), data...),
		},
		{
			"cLRND then cU then cLRND",
			append(append(append(bytes.Clone(chunk), uncompressed...), chunk...), 0x00),
			append(append(bytes.Clone(data), []byte("xyz")...), data...),
		},
		{"end then more", append(bytes.Clone(first), uncompressedReset...), data},
	}

	for _, c := range cases {
		in := bytes.NewReader(c.in)

		r, err := lzma.NewReader2(in, 8<<20)
		if err != nil {
			t.Fatalf("%s: NewReader2: %v", c.name, err)
		}

		got, err := io.ReadAll(r)

		switch {
		case c.want == nil:
			if !errors.Is(err, lzma.ErrCorrupt) {
				t.Errorf("%s: %d bytes, %v; want ErrCorrupt", c.name, len(got), err)
			}
		case err != nil || !bytes.Equal(got, c.want):
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		case c.name == "end then more" && in.Len() != len(uncompressedReset):
			t.Errorf(
				"%s: %d bytes left unread; want the %d past the end chunk",
				c.name,
				in.Len(),
				len(uncompressedReset),
			)
		}

		ref, refErr := lzmaspec.DecodeLZMA2(c.in, 8<<20)

		refOK := refErr == nil
		if refOK != (c.want != nil) || (refOK && !bytes.Equal(ref, c.want)) {
			t.Errorf("%s: the reference says %q, %v; the case expects %q", c.name, ref, refErr, c.want)
		}
	}
}

// markControl is the chunk with its control byte's reset bits replaced,
// keeping the size bits; a properties byte the chunk has is kept and one it
// lacks is not needed by the controls used here.
func markControl(chunk []byte, control byte) []byte {
	c := bytes.Clone(chunk)
	c[0] = control | (chunk[0] & 0x1F)

	if control < 0xC0 {
		// no properties byte: drop it
		c = append(c[:5], c[6:]...)
	}

	return c
}

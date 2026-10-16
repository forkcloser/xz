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

	"github.com/forkcloser/xz/internal/lzmaspec"
	"github.com/forkcloser/xz/lzma"
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

// writerConfigs are the classic writer's configurations the contracts run
// with: the default, an explicit size, size and marker both, the smallest
// dictionary, and the properties at their extremes.
func writerConfigs(n int) map[string]lzma.WriterConfig {
	return map[string]lzma.WriterConfig{
		"default":         {},
		"size":            {SizeInHeader: true, Size: int64(n)},
		"size and marker": {SizeInHeader: true, Size: int64(n), EOSMarker: true},
		"small dict":      {DictCap: lzma.MinDictCap, BufSize: 273},
		"lc0 lp4":         {Properties: &lzma.Properties{LC: 0, LP: 4, PB: 2}},
		"lc4 lp0 pb0":     {Properties: &lzma.Properties{LC: 4, LP: 0, PB: 0}},
		"pb4":             {Properties: &lzma.Properties{LC: 3, LP: 0, PB: 4}},
	}
}

func classic(tb testing.TB, c lzma.WriterConfig, data []byte) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := c.NewWriter(&buf)
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

			if wantMarker := c.EOSMarker || !c.SizeInHeader; out.Marker != wantMarker {
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

			if r.EOSMarker() != (c.EOSMarker || !c.SizeInHeader) {
				t.Fatalf("%s, %s: EOSMarker %v", name, cname, r.EOSMarker())
			}
		}
	}
}

// lzma2 writes data as a chunk sequence, flushing at each cut so the
// sequence has several chunks and a chunk boundary inside the data.
func lzma2(tb testing.TB, c lzma.Writer2Config, data []byte, cuts ...int) []byte {
	tb.Helper()

	var buf bytes.Buffer

	w, err := c.NewWriter2(&buf)
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
func writer2Configs() map[string]lzma.Writer2Config {
	return map[string]lzma.Writer2Config{
		"default":    {},
		"small dict": {DictCap: lzma.MinDictCap, BufSize: 273},
		"lc0 lp4":    {Properties: &lzma.Properties{LC: 0, LP: 4, PB: 2}},
		"lc4 pb4":    {Properties: &lzma.Properties{LC: 4, LP: 0, PB: 4}},
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

				dictCap := c.DictCap
				if dictCap == 0 {
					dictCap = 8 << 20
				}

				got, err := lzmaspec.DecodeLZMA2(stream, uint32(dictCap))
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

				r, err := lzma.Reader2Config{DictCap: c.DictCap}.NewReader2(in)
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
	r, err := lzma.ReaderConfig{DictCap: lzma.MaxDictCap}.NewReader(bytes.NewReader(stream))
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
		r, err := lzma.Reader2Config{DictCap: dictCap}.NewReader2(bytes.NewReader(stream))
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
			dictCap := c.DictCap
			if dictCap == 0 {
				dictCap = 8 << 20
			}

			stream := lzma2(t, c, data, cuts(len(data))[len(cuts(len(data)))-1]...)
			malformed(t, name+", "+cname, stream, referenceLZMA2Verdict(dictCap), lzma2Verdict(dictCap))
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

// TestContractDictCapCodes: the 41 dictionary codes decode to 2^n and
// 2^n+2^(n-1) up to 2^32-1, encode back to themselves, a value between two
// codes encodes to the next code up, and code 41 is refused.
func TestContractDictCapCodes(t *testing.T) {
	t.Parallel()

	for code := range byte(41) {
		n, err := lzma.DecodeDictCap(code)
		if err != nil {
			t.Fatalf("code %d: %v", code, err)
		}

		var want int64

		switch {
		case code == 40:
			want = 1<<32 - 1
		case code%2 == 0:
			want = 1 << (code/2 + 12)
		default:
			want = 3 << (code/2 + 11)
		}

		if n != want {
			t.Errorf("code %d: %d, want %d", code, n, want)
		}

		if back := lzma.EncodeDictCap(n); back != code {
			t.Errorf("value %d encodes to code %d; want %d", n, back, code)
		}

		if code > 0 && code < 40 {
			if up := lzma.EncodeDictCap(n - 1); up != code {
				t.Errorf("value %d encodes to code %d; want %d (the next code up)", n-1, up, code)
			}
		}
	}

	if _, err := lzma.DecodeDictCap(41); err == nil {
		t.Error("code 41 decodes")
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

// TestContractConfigBounds: the readers' and writers' configurations are
// accepted at the bounds of the dictionary capacity and the properties,
// and refused one past them.
func TestContractConfigBounds(t *testing.T) {
	t.Parallel()

	stream := classic(t, lzma.WriterConfig{DictCap: lzma.MinDictCap}, []byte("hello"))
	sequence := lzma2Stream(t, []byte("hello"))

	// A reader grows its dictionary as the data needs it; a writer
	// allocates the whole capacity on construction, four gigabytes at the
	// upper bound, so the writers are held at Verify, where acceptance is
	// decided.
	for _, dictCap := range []int{lzma.MinDictCap, lzma.MaxDictCap} {
		if _, err := (lzma.ReaderConfig{DictCap: dictCap}).NewReader(bytes.NewReader(stream)); err != nil {
			t.Errorf("ReaderConfig DictCap %d: %v; want accepted", dictCap, err)
		}

		if _, err := (lzma.Reader2Config{DictCap: dictCap}).NewReader2(bytes.NewReader(sequence)); err != nil {
			t.Errorf("Reader2Config DictCap %d: %v; want accepted", dictCap, err)
		}

		if err := (&lzma.WriterConfig{DictCap: dictCap}).Verify(); err != nil {
			t.Errorf("WriterConfig DictCap %d: %v; want accepted", dictCap, err)
		}

		if err := (&lzma.Writer2Config{DictCap: dictCap}).Verify(); err != nil {
			t.Errorf("Writer2Config DictCap %d: %v; want accepted", dictCap, err)
		}
	}

	for _, dictCap := range []int{1, lzma.MinDictCap - 1, lzma.MaxDictCap + 1} {
		if _, err := (lzma.ReaderConfig{DictCap: dictCap}).NewReader(bytes.NewReader(stream)); err == nil {
			t.Errorf("ReaderConfig DictCap %d accepted", dictCap)
		}

		if _, err := (lzma.Reader2Config{DictCap: dictCap}).NewReader2(bytes.NewReader(sequence)); err == nil {
			t.Errorf("Reader2Config DictCap %d accepted", dictCap)
		}

		if _, err := (lzma.WriterConfig{DictCap: dictCap}).NewWriter(io.Discard); err == nil {
			t.Errorf("WriterConfig DictCap %d accepted", dictCap)
		}

		if _, err := (lzma.Writer2Config{DictCap: dictCap}).NewWriter2(io.Discard); err == nil {
			t.Errorf("Writer2Config DictCap %d accepted", dictCap)
		}
	}

	for _, p := range []lzma.Properties{{LC: 4, LP: 0, PB: 4}, {LC: 0, LP: 4, PB: 0}, {LC: 2, LP: 2, PB: 2}} {
		if _, err := (lzma.WriterConfig{Properties: &p}).NewWriter(io.Discard); err != nil {
			t.Errorf("properties %+v: %v; want accepted", p, err)
		}
	}

	for _, p := range []lzma.Properties{{LC: 9}, {LC: -1}, {LP: 5}, {LP: -1}, {PB: 5}, {PB: -1}, {LC: 5, LP: 4}} {
		if _, err := (lzma.WriterConfig{Properties: &p}).NewWriter(io.Discard); err == nil {
			t.Errorf("properties %+v accepted by the writer", p)
		}

		if _, err := (lzma.Writer2Config{Properties: &p}).NewWriter2(io.Discard); err == nil {
			t.Errorf("properties %+v accepted by the LZMA2 writer", p)
		}
	}
}

// TestContractDictCapLimit: a classic header declaring a dictionary above
// ReaderConfig.DictCap is refused with ErrDictSize carrying both sizes.
func TestContractDictCapLimit(t *testing.T) {
	t.Parallel()

	stream := classic(t, lzma.WriterConfig{DictCap: 1 << 20}, []byte("hello"))

	_, err := lzma.ReaderConfig{DictCap: 1 << 16}.NewReader(bytes.NewReader(stream))

	var dictErr *lzma.ErrDictSize
	if !errors.As(err, &dictErr) || dictErr.ConfigDictCap != 1<<16 || dictErr.HeaderDictSize != 1<<20 {
		t.Fatalf("%v; want ErrDictSize with 65536 and 1048576", err)
	}

	r, err := lzma.ReaderConfig{DictCap: 1 << 20}.NewReader(bytes.NewReader(stream))
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

		r, err := lzma.NewReader2(in)
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

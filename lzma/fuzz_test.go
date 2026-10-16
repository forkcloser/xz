// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma_test

import (
	"bytes"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// FuzzClassic holds the classic reader to the reference decoder on
// arbitrary bytes: where the reference decodes, the same bytes; where it
// refuses, a refusal matching a sentinel. Seeds are the corpus's streams.
func FuzzClassic(f *testing.F) {
	for _, data := range corpus(f) {
		for _, c := range writerConfigs(len(data)) {
			f.Add(classic(f, c, data))
		}
	}

	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 1<<16 {
			return
		}

		classify(t, "fuzz", referenceVerdict(stream), classicVerdict(stream))
	})
}

// FuzzLZMA2 holds the LZMA2 reader to the reference decoder on arbitrary
// bytes, as FuzzClassic does the classic one.
func FuzzLZMA2(f *testing.F) {
	for _, data := range corpus(f) {
		for _, c := range writer2Configs() {
			f.Add(lzma2(f, c, data, cuts(len(data))[len(cuts(len(data)))-1]...))
		}
	}

	f.Fuzz(func(t *testing.T, stream []byte) {
		if len(stream) > 1<<16 {
			return
		}

		classify(t, "fuzz", referenceLZMA2Verdict(lzma.MinDictCap<<4)(stream), lzma2Verdict(lzma.MinDictCap<<4)(stream))
	})
}

// FuzzRoundTrip holds both writers to the readers and the reference on
// arbitrary input.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("x"))
	f.Add(pattern(5000))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}

		stream := classic(t, lzma.WriterConfig{}, data)

		got := classicVerdict(stream)
		if got.err != nil || !bytes.Equal(got.data, data) {
			t.Fatalf("classic: %d bytes, %v", len(got.data), got.err)
		}

		ref := referenceVerdict(stream)
		if ref.err != nil || !bytes.Equal(ref.data, data) {
			t.Fatalf("classic through the reference: %d bytes, %v", len(ref.data), ref.err)
		}

		stream = lzma2(t, lzma.Writer2Config{}, data)

		got = lzma2Verdict(0)(stream)
		if got.err != nil || !bytes.Equal(got.data, data) {
			t.Fatalf("lzma2: %d bytes, %v", len(got.data), got.err)
		}

		ref = referenceLZMA2Verdict(8 << 20)(stream)
		if ref.err != nil || !bytes.Equal(ref.data, data) {
			t.Fatalf("lzma2 through the reference: %d bytes, %v", len(ref.data), ref.err)
		}
	})
}

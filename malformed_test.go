// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"
)

// The parser's error branches are most of its code and were among its least
// exercised. These tests do not check which error comes back — that would
// just pin today's messages — but that one comes back at all, and that no
// input reaches a panic or a hang.

// wellFormed returns a small multi-block, CRC-64 checked stream.
func wellFormed(tb testing.TB) []byte {
	tb.Helper()
	return compressMultiBlock(tb, parallelTestData(4096), 1024)
}

// TestTruncatedAtEveryOffset feeds every prefix of a valid file to both
// readers. A prefix is never a valid xz file, so each must either refuse it or
// stop short with an error — never succeed, never crash.
func TestTruncatedAtEveryOffset(t *testing.T) {
	full := wellFormed(t)
	want := parallelTestData(4096)

	for n := range full {
		prefix := full[:n]

		if r, err := NewReader(bytes.NewReader(prefix)); err == nil {
			got, err := io.ReadAll(r)
			if err == nil && bytes.Equal(got, want) {
				t.Fatalf("sequential reader accepted a %d byte prefix "+
					"of a %d byte file as complete", n, len(full))
			}
		}

		pr, err := NewParallelReader(bytes.NewReader(prefix), int64(n))
		if err != nil {
			continue
		}
		got, err := io.ReadAll(pr)
		_ = pr.Close()
		if err == nil && bytes.Equal(got, want) {
			t.Fatalf("parallel reader accepted a %d byte prefix "+
				"of a %d byte file as complete", n, len(full))
		}
	}
}

// TestMissingIndexAndFooterIsTruncation pins the specific shape that
// TestTruncatedAtEveryOffset caught: a file cut off exactly at the end of its
// last block, so the index and footer are gone but every byte of compressed
// data is present.
//
// This used to decode as a complete, successful read. The last block finished
// cleanly, the next read found no bytes where a block header or the index
// indicator belongs, and that bare EOF was taken to mean the stream had ended
// rather than that it had been cut. Every xz stream carries an index and a
// footer, so their absence is truncation — the reference tool exits non-zero
// on the same input.
func TestMissingIndexAndFooterIsTruncation(t *testing.T) {
	payload := parallelTestData(4096)
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	full := buf.Bytes()

	// Walk back over the tail so the exact index size does not matter; every
	// cut that removes any part of the index or footer must be reported.
	for cut := 1; cut <= 48 && cut < len(full); cut++ {
		r, err := NewReader(bytes.NewReader(full[:len(full)-cut]))
		if err != nil {
			continue
		}
		got, err := io.ReadAll(r)
		if err == nil {
			t.Fatalf("a file missing its last %d bytes decoded to %d bytes "+
				"with no error", cut, len(got))
		}
	}
}

// TestSingleByteCorruptionAtEveryOffset flips one bit at each position. Any
// change to a checked stream has to be caught: the whole point of the header,
// index and block checksums is that no single-bit change goes unnoticed.
func TestSingleByteCorruptionAtEveryOffset(t *testing.T) {
	full := wellFormed(t)
	want := parallelTestData(4096)

	for i := range full {
		bad := append([]byte{}, full...)
		bad[i] ^= 0x40

		if r, err := NewReader(bytes.NewReader(bad)); err == nil {
			got, err := io.ReadAll(r)
			if err == nil && !bytes.Equal(got, want) {
				t.Fatalf("byte %d: sequential reader returned different "+
					"data with no error", i)
			}
		}

		pr, err := NewParallelReader(bytes.NewReader(bad), int64(len(bad)))
		if err != nil {
			continue
		}
		got, err := io.ReadAll(pr)
		_ = pr.Close()
		if err == nil && !bytes.Equal(got, want) {
			t.Fatalf("byte %d: parallel reader returned different data "+
				"with no error", i)
		}
	}
}

// TestGarbageInputIsRejected covers inputs that are not xz at all, including
// the shapes most likely to confuse a backwards index walk.
func TestGarbageInputIsRejected(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"one byte":         {0xfd},
		"header prefix":    headerMagic,
		"all zeros":        make([]byte, 4096),
		"header only":      append(append([]byte{}, headerMagic...), 0, 0, 0, 0, 0, 0),
		"footer only":      {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'Y', 'Z'},
		"magic then zeros": append(append([]byte{}, headerMagic...), make([]byte, 512)...),
	}
	rng := rand.New(rand.NewSource(11))
	for i := range 8 {
		p := make([]byte, 64*(i+1))
		rng.Read(p)
		cases[string(rune('a'+i))+" random"] = p
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if r, err := NewReader(bytes.NewReader(data)); err == nil {
				if _, err = io.ReadAll(r); err == nil && len(data) != 0 {
					// An empty stream of streams is legitimately empty; any
					// other garbage that reads clean is a problem.
					t.Errorf("sequential reader accepted %d bytes of garbage",
						len(data))
				}
			}
			pr, err := NewParallelReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return
			}
			defer func() { _ = pr.Close() }()
			if _, err = io.ReadAll(pr); err == nil {
				t.Errorf("parallel reader accepted %d bytes of garbage",
					len(data))
			}
		})
	}
}

// TestUnsupportedFilterID covers the filter table, whose error paths are the
// only thing standing between an unknown filter and a decode that quietly
// produces the wrong bytes.
func TestUnsupportedFilterID(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		kind error
	}{
		{"wrong id", []byte{0x22, 0x01, 0x00}, ErrCorrupt},
		{"wrong size", []byte{lzmaFilterID, 0x02, 0x00}, ErrCorrupt},
		{"bad dict cap", []byte{lzmaFilterID, 0x01, 0xff}, ErrCorrupt},
		{"short", []byte{lzmaFilterID, 0x01}, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f lzmaFilter
			err := f.UnmarshalBinary(tc.data)
			if err == nil {
				t.Fatalf("accepted %#v", tc.data)
			}
			if !errors.Is(err, tc.kind) {
				t.Errorf("got %v; want a match for %v", err, tc.kind)
			}
		})
	}

	// A reserved filter id must be reported as reserved rather than merely
	// unknown, and an unknown one as unsupported.
	if _, err := readFilter(bytes.NewReader(
		uvarintBytes(minReservedID))); err == nil {
		t.Error("reserved filter id accepted")
	}
	if _, err := readFilter(bytes.NewReader([]byte{0x22})); !errors.Is(
		err, ErrUnsupported) {
		t.Errorf("unknown filter id gave %v; want a match for ErrUnsupported", err)
	}
	if _, err := readFilters(bytes.NewReader([]byte{0x21, 0x01, 0x00}), 2); !errors.Is(
		err, ErrUnsupported) {
		t.Errorf("two filters gave %v; want a match for ErrUnsupported", err)
	}
}

// TestHeaderAndFooterValidation covers the fixed-size records directly, since
// reaching every branch through a whole file is awkward.
func TestHeaderAndFooterValidation(t *testing.T) {
	good, err := (&header{flags: CRC64}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidHeader(good) {
		t.Fatal("ValidHeader rejected a header we just marshalled")
	}
	if ValidHeader(good[:HeaderLen-1]) {
		t.Error("ValidHeader accepted a short header")
	}

	for name, mutate := range map[string]func([]byte){
		"magic":          func(p []byte) { p[0] ^= 0xff },
		"reserved flags": func(p []byte) { p[6] = 1 },
		"unknown check":  func(p []byte) { p[7] = 0x7 },
		"crc":            func(p []byte) { p[8] ^= 0xff },
	} {
		t.Run("header "+name, func(t *testing.T) {
			bad := append([]byte{}, good...)
			mutate(bad)
			var h header
			if err := h.UnmarshalBinary(bad); err == nil {
				t.Error("accepted a corrupted header")
			} else if !errors.Is(err, ErrCorrupt) {
				t.Errorf("got %v; want a match for ErrCorrupt", err)
			}
		})
	}

	goodFooter, err := (&footer{indexSize: 8, flags: CRC64}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var f footer
	if err := f.UnmarshalBinary(goodFooter); err != nil {
		t.Fatalf("rejected a footer we just marshalled: %s", err)
	}
	if f.indexSize != 8 || f.flags != CRC64 {
		t.Errorf("footer round trip gave %+v", f)
	}
	for name, mutate := range map[string]func([]byte){
		"magic":          func(p []byte) { p[10] = 'X' },
		"reserved flags": func(p []byte) { p[8] = 1 },
		"unknown check":  func(p []byte) { p[9] = 0x7 },
		"crc":            func(p []byte) { p[0] ^= 0xff },
	} {
		t.Run("footer "+name, func(t *testing.T) {
			bad := append([]byte{}, goodFooter...)
			mutate(bad)
			var f footer
			if err := f.UnmarshalBinary(bad); err == nil {
				t.Error("accepted a corrupted footer")
			} else if !errors.Is(err, ErrCorrupt) {
				t.Errorf("got %v; want a match for ErrCorrupt", err)
			}
		})
	}
	var short footer
	if err := short.UnmarshalBinary(make([]byte, footerLen-1)); err == nil {
		t.Error("accepted a short footer")
	}
}

// TestWriterConfigValidation covers the configuration errors, which are the
// difference between a clear failure at construction and a confusing one
// later.
func TestWriterConfigValidation(t *testing.T) {
	for name, c := range map[string]WriterConfig{
		"negative block size": {BlockSize: -1},
		"bad checksum":        {CheckSum: 0x7},
		"tiny dict":           {DictCap: 1},
		"huge dict":           {DictCap: 1 << 40},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := c
			if err := cfg.Verify(); err == nil {
				t.Errorf("Verify accepted %+v", c)
			}
			if _, err := c.NewWriter(io.Discard); err == nil {
				t.Errorf("NewWriter accepted %+v", c)
			}
		})
	}

	// A nil config pointer must be reported, not dereferenced.
	var nilCfg *WriterConfig
	if err := nilCfg.Verify(); err == nil {
		t.Error("nil WriterConfig.Verify returned no error")
	}
	var nilReader *ReaderConfig
	if err := nilReader.Verify(); err == nil {
		t.Error("nil ReaderConfig.Verify returned no error")
	}
	var nilParallel *ParallelReaderConfig
	if err := nilParallel.Verify(); err == nil {
		t.Error("nil ParallelReaderConfig.Verify returned no error")
	}
}

// TestWriterCloseTwice and friends cover the writer's own state machine.
func TestWriterCloseTwice(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("second Close gave %v; want a match for ErrClosed", err)
	}
	if _, err = w.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Close gave %v; want a match for ErrClosed", err)
	}
	// An empty stream must still be a valid, readable xz file.
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("NewReader on an empty stream: %s", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading an empty stream: %s", err)
	}
	if len(got) != 0 {
		t.Errorf("empty stream decoded to %d bytes", len(got))
	}
}

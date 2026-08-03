// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// This package decodes untrusted input, so the failure mode that matters is
// not a wrong answer but a panic, a hang, or an allocation large enough to
// take the process down. Every one of those has happened here: the audit
// found a 44-byte file that panicked on a worker goroutine, a 40-byte file
// that reserved 16 TiB, and a 104-byte file that spun forever. TODO.md records
// an earlier one found by fuzzing in 2021.
//
// The rule these targets enforce is simply that malformed input produces an
// error. Run them with, for example:
//
//	go test -run '^$' -fuzz FuzzReader -fuzztime 2m

// fuzzOutputLimit caps how much a fuzz case may decode. A legitimate small
// input can expand enormously, and that is not what these targets are looking
// for; anything past the limit is accepted and ignored.
const fuzzOutputLimit = 8 << 20

// fuzzSeeds returns the corpus files committed with the package plus a few
// streams built here, so the fuzzer starts from inputs that already parse.
func fuzzSeeds(tb testing.TB) [][]byte {
	tb.Helper()
	var seeds [][]byte
	for _, name := range []string{"fox.xz", "fox-check-none.xz", "example.xz"} {
		if data, err := os.ReadFile(name); err == nil {
			seeds = append(seeds, data)
		}
	}
	// A multi-block, multi-stream file, so the index walk and the parallel
	// reader's block splitting are reachable from the seed corpus.
	body := parallelTestData(1 << 14)
	multi := compressMultiBlock(tb, body, 2<<10)
	seeds = append(seeds, multi)
	seeds = append(seeds, append(append(append([]byte{}, multi...),
		0, 0, 0, 0), multi...))
	for _, check := range []byte{None, CRC32, CRC64, SHA256} {
		var buf bytes.Buffer
		w, err := WriterConfig{CheckSum: check}.NewWriter(&buf)
		if err != nil {
			continue
		}
		if _, err = w.Write([]byte("the quick brown fox")); err != nil {
			continue
		}
		if err = w.Close(); err != nil {
			continue
		}
		seeds = append(seeds, buf.Bytes())
	}
	return seeds
}

// FuzzReader checks that the sequential reader survives arbitrary input.
func FuzzReader(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		// The result is discarded: a decoder that returns the wrong bytes for
		// a corrupt file is not interesting, one that crashes on it is.
		_, _ = io.Copy(io.Discard, io.LimitReader(r, fuzzOutputLimit))
	})
}

// FuzzSingleStreamReader exercises the SingleStream path, which has its own
// end-of-stream handling.
func FuzzSingleStreamReader(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := ReaderConfig{SingleStream: true}.NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r, fuzzOutputLimit))
	})
}

// FuzzParallelReader covers the index walk, which reads every block offset and
// size out of the file before decoding anything and so is the part most
// exposed to a hostile index.
func FuzzParallelReader(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := ParallelReaderConfig{Workers: 2}.NewParallelReader(
			bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		defer func() { _ = r.Close() }()
		if n := r.Size(); n < 0 {
			t.Fatalf("Size() is negative: %d", n)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r, fuzzOutputLimit))
	})
}

// FuzzReadersAgree requires the sequential and parallel readers to produce the
// same bytes for any input both accept. They share the block decoder but not
// the framing logic, so a disagreement means one of them is reading the file
// wrongly.
func FuzzReadersAgree(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		sr, serr := NewReader(bytes.NewReader(data))
		var serial []byte
		if serr == nil {
			serial, serr = io.ReadAll(io.LimitReader(sr, fuzzOutputLimit))
		}

		pr, perr := NewParallelReader(bytes.NewReader(data), int64(len(data)))
		var parallel []byte
		if perr == nil {
			defer func() { _ = pr.Close() }()
			parallel, perr = io.ReadAll(io.LimitReader(pr, fuzzOutputLimit))
		}

		// Only compare when both succeeded outright. Either may stop early
		// for its own reasons on a malformed file, and neither is wrong to.
		if serr != nil || perr != nil {
			return
		}
		if len(serial) == fuzzOutputLimit || len(parallel) == fuzzOutputLimit {
			return // truncated by the limit, not comparable
		}
		if !bytes.Equal(serial, parallel) {
			t.Fatalf("readers disagree: sequential produced %d bytes, "+
				"parallel produced %d", len(serial), len(parallel))
		}
	})
}

// FuzzRoundTrip checks the other direction: anything this package compresses,
// it must decompress back to exactly the input. The writer is far less exposed
// than the reader, but a round trip that loses data is the worst bug a
// compression library can have.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte(""), uint8(0), uint16(0))
	f.Add([]byte("the quick brown fox jumps over the lazy dog"), uint8(1), uint16(0))
	f.Add(bytes.Repeat([]byte("a"), 5000), uint8(4), uint16(1<<10))
	f.Add([]byte{0, 1, 2, 3, 0xff, 0xfe}, uint8(10), uint16(4<<10))

	f.Fuzz(func(t *testing.T, data []byte, check uint8, blockSize uint16) {
		cfg := WriterConfig{}
		switch check {
		case None, CRC32, CRC64, SHA256:
			cfg.CheckSum = check
		default:
			return // not a check type the format defines
		}
		if blockSize > 0 {
			cfg.BlockSize = int64(blockSize)
		}

		var buf bytes.Buffer
		w, err := cfg.NewWriter(&buf)
		if err != nil {
			t.Fatalf("NewWriter with check %d, block size %d: %s",
				check, blockSize, err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatalf("Write: %s", err)
		}
		if err = w.Close(); err != nil {
			t.Fatalf("Close: %s", err)
		}

		compressed := buf.Bytes()
		r, err := NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("NewReader on our own output: %s", err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("reading back our own output: %s", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round trip changed the data: got %d bytes, want %d",
				len(got), len(data))
		}

		// The same file has to come back identically through the parallel
		// reader, which finds the blocks through the index rather than by
		// walking the stream.
		pr, err := NewParallelReader(bytes.NewReader(compressed),
			int64(len(compressed)))
		if err != nil {
			t.Fatalf("NewParallelReader on our own output: %s", err)
		}
		defer func() { _ = pr.Close() }()
		if pr.Size() != int64(len(data)) {
			t.Fatalf("Size() is %d; want %d", pr.Size(), len(data))
		}
		got, err = io.ReadAll(pr)
		if err != nil {
			t.Fatalf("parallel read of our own output: %s", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("parallel round trip changed the data")
		}
	})
}

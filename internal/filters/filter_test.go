// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filters_test

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/forkcloser/xz/v2/internal/filters"
)

// bcjIDs lists every BCJ filter with its alignment.
func bcjIDs() []struct {
	name  string
	id    uint64
	align uint32
} {
	return []struct {
		name  string
		id    uint64
		align uint32
	}{
		{"x86", filters.X86ID, 1},
		{"powerpc", filters.PowerPCID, 4},
		{"ia64", filters.IA64ID, 16},
		{"arm", filters.ARMID, 4},
		{"armthumb", filters.ARMThumbID, 2},
		{"sparc", filters.SPARCID, 4},
		{"arm64", filters.ARM64ID, 4},
		{"riscv", filters.RISCVID, 2},
	}
}

// corpus returns inputs that exercise every converter: uniform random
// bytes, and bytes drawn from the opcodes the filters look for, so that
// the instruction patterns are dense and their neighbours vary.
func corpus(seed uint64, n int) [][]byte {
	rng := rand.New(rand.NewPCG(seed, 1))

	random := make([]byte, n)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	opcodes := []byte{
		0x00, 0xFF, 0xE8, 0xE9, 0xEB, 0xF0, 0xF8, 0x48, 0x4B, 0x40, 0x7F,
		0xC0, 0x94, 0x97, 0x90, 0xB0, 0x17, 0xEF, 0x03, 0x67, 0x05, 0x10, 0x16, 0x18,
	}

	dense := make([]byte, n)
	for i := range dense {
		dense[i] = opcodes[rng.IntN(len(opcodes))]
	}

	return [][]byte{random, dense, craftedBranches(rng)}
}

// craftedBranches returns instructions the sparser patterns of random bytes
// rarely form: IA-64 bundles whose template admits a branch and whose slots
// hold a br.call, and PowerPC bl instructions, each with random operands.
func craftedBranches(rng *rand.Rand) []byte {
	templates := []byte{16, 17, 18, 19, 22, 23, 24, 25, 28, 29}

	var out []byte

	for range 128 {
		// A bundle is 5 template bits then three 41-bit slots, least
		// significant bit first.
		var bits [3]uint64
		for s := range bits {
			v := rng.Uint64() & (1<<41 - 1)
			v &^= 0xF << 37
			v |= 0x5 << 37
			v &^= 0x7 << 9
			bits[s] = v
		}

		var bundle [16]byte

		bundle[0] = templates[rng.IntN(len(templates))] | byte(rng.Uint32()&0xE0)

		pos := uint(5)

		for _, v := range bits {
			for b := range uint(41) {
				if v>>b&1 != 0 {
					bundle[(pos+b)>>3] |= 1 << ((pos + b) & 7)
				}
			}

			pos += 41
		}

		out = append(out, bundle[:]...)
	}

	for range 256 {
		r := rng.Uint32()
		out = append(out, 0x48|byte(r&3), byte(r>>8), byte(r>>16), byte(r>>24)&^3|1)
	}

	return out
}

// codeAll runs a converter over data in one call per chunk, carrying the
// unconverted tail into the next chunk as a stream would, and converts the
// final tail by leaving it alone, as the format does at the end of the data.
func codeAll(t *testing.T, b *filters.BCJ, data []byte, chunk func() int) []byte {
	t.Helper()

	out := make([]byte, 0, len(data))
	buf := make([]byte, 0, len(data))

	for len(data) > 0 {
		k := min(chunk(), len(data))
		buf = append(buf, data[:k]...)
		data = data[k:]

		n := b.Code(buf)
		if n > len(buf) {
			t.Fatalf("Code converted %d of %d bytes", n, len(buf))
		}

		if n == 0 && len(buf) >= b.Lookahead() && len(data) > 0 {
			t.Fatalf("no progress on %d bytes, lookahead %d", len(buf), b.Lookahead())
		}

		out = append(out, buf[:n]...)
		buf = append(buf[:0], buf[n:]...)
	}

	return append(out, buf...)
}

func TestBCJRoundTrip(t *testing.T) {
	t.Parallel()

	for _, f := range bcjIDs() {
		for _, start := range []uint32{0, f.align * 3, 0x1000, 0xFFFFF000} {
			t.Run(f.name, func(t *testing.T) {
				t.Parallel()

				for _, data := range corpus(f.id, 1<<16) {
					enc, err := filters.NewBCJ(f.id, start, true)
					if err != nil {
						t.Fatal(err)
					}

					dec, err := filters.NewBCJ(f.id, start, false)
					if err != nil {
						t.Fatal(err)
					}

					rng := rand.New(rand.NewPCG(uint64(start), 2))
					chunk := func() int { return 1 + rng.IntN(300) }

					encoded := codeAll(t, enc, bytes.Clone(data), chunk)
					if len(encoded) != len(data) {
						t.Fatalf("encoded %d bytes, want %d", len(encoded), len(data))
					}

					decoded := codeAll(t, dec, bytes.Clone(encoded), chunk)
					if !bytes.Equal(decoded, data) {
						t.Fatalf("start 0x%x: decode(encode(data)) differs from data", start)
					}
				}
			})
		}
	}
}

// TestBCJChunking holds a converter to the same output whatever the chunk
// sizes it sees the data in: the reader's contract.
func TestBCJChunking(t *testing.T) {
	t.Parallel()

	for _, f := range bcjIDs() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()

			for _, data := range corpus(f.id+100, 1<<15) {
				whole, err := filters.NewBCJ(f.id, 0, true)
				if err != nil {
					t.Fatal(err)
				}

				want := codeAll(t, whole, bytes.Clone(data), func() int { return len(data) })

				for _, size := range []int{1, 2, 3, 5, 7, 16, 17, 64, 1000} {
					b, err := filters.NewBCJ(f.id, 0, true)
					if err != nil {
						t.Fatal(err)
					}

					got := codeAll(t, b, bytes.Clone(data), func() int { return size })
					if !bytes.Equal(got, want) {
						t.Fatalf("chunks of %d bytes convert differently from one call", size)
					}
				}
			}
		})
	}
}

func TestBCJConverts(t *testing.T) {
	t.Parallel()

	// A CALL at offset 0 with displacement 0x10: the encoder makes the
	// target absolute, 0x10 + 5.
	b, err := filters.NewBCJ(filters.X86ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}

	// Of the ten bytes, the call and the NOP after it are decided; the
	// last four could begin a call whose displacement is not here yet.
	buf := []byte{0xE8, 0x10, 0x00, 0x00, 0x00, 0x90, 0x90, 0x90, 0x90, 0x90}
	if n := b.Code(buf); n != 6 {
		t.Fatalf("converted %d bytes, want 6", n)
	}

	if want := []byte{0xE8, 0x15, 0x00, 0x00, 0x00}; !bytes.Equal(buf[:5], want) {
		t.Fatalf("got % x, want % x", buf[:5], want)
	}
}

func TestBCJErrors(t *testing.T) {
	t.Parallel()

	if _, err := filters.NewBCJ(0x21, 0, false); err == nil {
		t.Fatal("LZMA2's id accepted as a BCJ filter")
	}

	for _, f := range bcjIDs() {
		if f.align == 1 {
			continue
		}

		if _, err := filters.NewBCJ(f.id, f.align/2, false); !errors.Is(err, filters.ErrAlignment) {
			t.Fatalf("%s: start offset %d: %v, want ErrAlignment", f.name, f.align/2, err)
		}
	}

	if !filters.IsBCJ(filters.X86ID) || filters.IsBCJ(filters.DeltaID) {
		t.Fatal("IsBCJ wrong")
	}
}

func TestDelta(t *testing.T) {
	t.Parallel()

	d, err := filters.NewDelta(1)
	if err != nil {
		t.Fatal(err)
	}

	buf := []byte{1, 2, 3, 5, 8}
	d.Encode(buf)

	if want := []byte{1, 1, 1, 2, 3}; !bytes.Equal(buf, want) {
		t.Fatalf("encode: got %v, want %v", buf, want)
	}

	for _, dist := range []int{1, 2, 3, 4, 255, 256} {
		for _, data := range corpus(uint64(dist), 1<<14) {
			enc, err := filters.NewDelta(dist)
			if err != nil {
				t.Fatal(err)
			}

			dec, err := filters.NewDelta(dist)
			if err != nil {
				t.Fatal(err)
			}

			buf := bytes.Clone(data)
			// In pieces: the coder's history spans calls.
			for i := 0; i < len(buf); i += 777 {
				enc.Encode(buf[i:min(i+777, len(buf))])
			}

			for i := 0; i < len(buf); i += 333 {
				dec.Decode(buf[i:min(i+333, len(buf))])
			}

			if !bytes.Equal(buf, data) {
				t.Fatalf("distance %d: decode(encode(data)) differs from data", dist)
			}
		}
	}

	for _, dist := range []int{0, 257} {
		if _, err := filters.NewDelta(dist); !errors.Is(err, filters.ErrDistance) {
			t.Fatalf("distance %d: %v, want ErrDistance", dist, err)
		}
	}
}

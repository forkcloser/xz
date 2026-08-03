// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"math/rand"
	"testing"
)

// eagerDecoderDict builds a dictionary that allocates dictCap up front and
// never grows — the behaviour that predates on-demand growth. It is the
// reference the growing dictionary is compared against.
func eagerDecoderDict(dictCap int) *decoderDict {
	return &decoderDict{
		buf:     *newBuffer(dictCap),
		dictCap: dictCap,
		growAt:  maxInt,
	}
}

// dictState captures everything a decoder can observe about a dictionary:
// head, dictLen, the buffered count, and the history reachable through
// byteAt. Capacity itself is deliberately excluded — that is the one thing
// growth is allowed to differ on.
//
// maxDist bounds how far back the history is compared, and 0 means all of it.
// Scanning the whole dictionary after every single step is quadratic and
// dominates the runtime, so callers check a recent window per step — enough to
// catch a divergence at the step that caused it — and the full history once at
// the end.
func dictState(d *decoderDict, maxDist int) []byte {
	var b bytes.Buffer
	fmtInt := func(x int64) {
		for i := range 8 {
			b.WriteByte(byte(x >> (8 * i)))
		}
	}
	fmtInt(d.head)
	fmtInt(int64(d.dictLen()))
	fmtInt(int64(d.buf.Buffered()))
	n := d.dictLen()
	if maxDist > 0 && n > maxDist {
		n = maxDist
	}
	for dist := 1; dist <= n; dist++ {
		b.WriteByte(d.byteAt(dist))
	}
	return b.Bytes()
}

// TestDecoderDictGrowMatchesEager drives a growing dictionary and an eagerly
// allocated one through identical random operation sequences and requires that
// every observable — head, dictLen, buffered count, the full history reachable
// through byteAt, and the bytes read out — stays identical. Growth must be
// invisible to the decoder; only the allocation differs.
func TestDecoderDictGrowMatchesEager(t *testing.T) {
	caps := []int{1, 2, 3, 7, 273, 274, 1000, 4096,
		initialDictCap - 1, initialDictCap, initialDictCap + 1,
		3 * initialDictCap}
	// Starting from one byte forces a growth step on nearly every write, so
	// the small capacities exercise growth and wrapping together instead of
	// being allocated whole up front.
	initials := []int{1, 2, 300, initialDictCap}

	for _, dictCap := range caps {
		for _, initial := range initials {
			t.Run("", func(t *testing.T) {
				testGrowMatchesEager(t, dictCap, initial)
			})
		}
	}
}

func testGrowMatchesEager(t *testing.T, dictCap, initial int) {
	t.Helper()
	for seed := range int64(16) {
		grow, err := newDecoderDictSize(dictCap, initial)
		if err != nil {
			t.Fatalf("dictCap %d: newDecoderDict error %s", dictCap, err)
		}
		eager := eagerDecoderDict(dictCap)

		rng := rand.New(rand.NewSource(seed))
		var gotOut, wantOut bytes.Buffer
		drain := make([]byte, 4096)

		for step := range 400 {
			switch rng.Intn(10) {
			case 0, 1, 2, 3, 4: // literal
				c := byte(rng.Intn(256))
				gErr := grow.WriteByte(c)
				eErr := eager.WriteByte(c)
				if (gErr == nil) != (eErr == nil) {
					t.Fatalf("dictCap %d seed %d step %d: WriteByte error %v vs %v",
						dictCap, seed, step, gErr, eErr)
				}
			case 5, 6, 7: // match, sometimes overlapping
				dl := eager.dictLen()
				if dl == 0 {
					continue
				}
				dist := int64(1 + rng.Intn(dl))
				length := 1 + rng.Intn(maxMatchLen)
				gErr := grow.writeMatch(dist, length)
				eErr := eager.writeMatch(dist, length)
				if (gErr == nil) != (eErr == nil) {
					t.Fatalf("dictCap %d seed %d step %d: writeMatch(%d,%d) error %v vs %v",
						dictCap, seed, step, dist, length, gErr, eErr)
				}
			case 8: // bulk write
				p := make([]byte, rng.Intn(500))
				rng.Read(p)
				gn, _ := grow.Write(p)
				en, _ := eager.Write(p)
				if gn != en {
					t.Fatalf("dictCap %d seed %d step %d: Write wrote %d vs %d",
						dictCap, seed, step, gn, en)
				}
			case 9: // reader drains some output
				n := rng.Intn(len(drain))
				gn, _ := grow.Read(drain[:n])
				gotOut.Write(drain[:gn])
				en, _ := eager.Read(drain[:n])
				wantOut.Write(drain[:en])
				if gn != en {
					t.Fatalf("dictCap %d seed %d step %d: Read returned %d vs %d",
						dictCap, seed, step, gn, en)
				}
			}

			if !bytes.Equal(dictState(grow, 256), dictState(eager, 256)) {
				t.Fatalf("dictCap %d seed %d step %d: dictionary state diverged",
					dictCap, seed, step)
			}
		}

		// Full history, once, now that the run is over.
		if !bytes.Equal(dictState(grow, 0), dictState(eager, 0)) {
			t.Fatalf("dictCap %d seed %d: dictionary history diverged",
				dictCap, seed)
		}
		if !bytes.Equal(gotOut.Bytes(), wantOut.Bytes()) {
			t.Fatalf("dictCap %d seed %d: output differs", dictCap, seed)
		}
		if grow.buf.Cap() > dictCap {
			t.Fatalf("dictCap %d seed %d: grew to %d, past the declared capacity",
				dictCap, seed, grow.buf.Cap())
		}
	}
}

// TestDecoderDictGrowWithReset covers the dictionary reset that an LZMA2
// cLRND chunk performs: it zeroes head while the buffer keeps its write
// position, so growth has to stay correct across it.
func TestDecoderDictGrowWithReset(t *testing.T) {
	for _, dictCap := range []int{1000, initialDictCap, 3 * initialDictCap} {
		grow, err := newDecoderDict(dictCap)
		if err != nil {
			t.Fatal(err)
		}
		eager := eagerDecoderDict(dictCap)
		rng := rand.New(rand.NewSource(99))
		drain := make([]byte, 1024)

		for step := range 3000 {
			if step%700 == 699 {
				grow.Reset()
				eager.Reset()
			}
			c := byte(rng.Intn(256))
			if err := grow.WriteByte(c); err == nil {
				if err := eager.WriteByte(c); err != nil {
					t.Fatalf("dictCap %d step %d: eager refused a byte the "+
						"growing dictionary accepted", dictCap, step)
				}
			} else {
				n := rng.Intn(len(drain))
				gn, _ := grow.Read(drain[:n])
				en, _ := eager.Read(drain[:n])
				if gn != en {
					t.Fatalf("dictCap %d step %d: Read %d vs %d",
						dictCap, step, gn, en)
				}
			}
			if !bytes.Equal(dictState(grow, 256), dictState(eager, 256)) {
				t.Fatalf("dictCap %d step %d: state diverged after reset",
					dictCap, step)
			}
		}
	}
}

// TestDecoderDictGrowsOnlyAsFarAsNeeded is the point of the change: a stream
// that decodes very little must not pay for a dictionary the header merely
// claims.
func TestDecoderDictGrowsOnlyAsFarAsNeeded(t *testing.T) {
	const huge = 1 << 30
	d, err := newDecoderDict(huge)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.buf.Cap(); got > initialDictCap {
		t.Fatalf("fresh dictionary for a %d byte capacity allocated %d bytes",
			huge, got)
	}
	for range 45 {
		if err := d.WriteByte('x'); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.buf.Cap(); got > initialDictCap {
		t.Fatalf("after 45 bytes the dictionary holds %d bytes", got)
	}
	if d.dictLen() != 45 {
		t.Fatalf("dictLen is %d; want 45", d.dictLen())
	}
}

// TestDecoderDictGrowReachesDeclaredCap checks the other end: a stream that
// really does use its dictionary still gets the full capacity, so long
// distance matches keep resolving.
func TestDecoderDictGrowReachesDeclaredCap(t *testing.T) {
	const dictCap = 4 * initialDictCap
	d, err := newDecoderDict(dictCap)
	if err != nil {
		t.Fatal(err)
	}
	drain := make([]byte, 8192)
	for written := 0; written < dictCap; {
		if err := d.WriteByte(byte(written)); err != nil {
			n, _ := d.Read(drain)
			if n == 0 {
				t.Fatal("dictionary is full but yields no data")
			}
			continue
		}
		written++
	}
	if got := d.buf.Cap(); got != dictCap {
		t.Fatalf("after writing %d bytes the capacity is %d; want %d",
			dictCap, got, dictCap)
	}
	if got := d.dictLen(); got != dictCap {
		t.Fatalf("dictLen is %d; want %d", got, dictCap)
	}
	// The furthest legal distance must still resolve to the right byte.
	if got, want := d.byteAt(dictCap), byte(0); got != want {
		t.Fatalf("byteAt(%d) is %d; want %d", dictCap, got, want)
	}
}

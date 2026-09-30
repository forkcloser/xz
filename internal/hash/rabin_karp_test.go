// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hash //nolint:testpackage // white-box: defines the benchmark input cyclic_poly_test.go uses

import (
	"math/rand/v2"
	"testing"
)

func TestRabinKarpSimple(t *testing.T) {
	t.Parallel()

	p := []byte("abcde")
	r := NewRabinKarp(4)

	h2 := Hashes(r, p)
	for i, h := range h2 {
		ws := Hashes(r, p[i:i+4])
		if len(ws) != 1 {
			t.Fatalf("%d hashes of a 4-byte window; want 1", len(ws))
		}

		w := ws[0]
		t.Logf("%d h=%#016x w=%#016x", i, h, w)

		if h != w {
			t.Errorf("rolling hash %d: %#016x; want %#016x",
				i, h, w)
		}
	}
}

func makeBenchmarkBytes(n int) []byte {
	rnd := rand.New(rand.NewPCG(42, 0))

	p := make([]byte, n)
	for i := range p {
		p[i] = byte(rnd.Uint32())
	}

	return p
}

func BenchmarkRabinKarp(b *testing.B) {
	p := makeBenchmarkBytes(4096)

	r := NewRabinKarp(4)
	for b.Loop() {
		Hashes(r, p)
	}
}

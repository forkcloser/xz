// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "testing"

// TestPropertiesForCodeRejectsLargeLCLP pins the lc+lp limit on the decode
// path. Without it a single properties byte sizes the literal codec at
// 0x300<<12 probabilities — 6 MiB that every property-resetting chunk
// reallocates and refills, turning a small file into hours of memset.
//
// The limit is not ours: the reference implementation applies it when decoding
// the properties byte of both LZMA and LZMA2 streams, and this package's
// writer has always refused to emit anything above it. Only the decoder was
// missing the check.
func TestPropertiesForCodeRejectsLargeLCLP(t *testing.T) {
	var accepted, rejected int
	for code := 0; code <= 0xff; code++ {
		p, err := PropertiesForCode(byte(code))
		if err != nil {
			rejected++
			continue
		}
		accepted++
		if p.LC+p.LP > maxLCLP {
			t.Errorf("code %d accepted with lc=%d lp=%d, sum %d > %d",
				code, p.LC, p.LP, p.LC+p.LP, maxLCLP)
		}
		if n := 0x300 << uint(p.LC+p.LP); n > 0x300<<maxLCLP {
			t.Errorf("code %d sizes the literal codec at %d probabilities",
				code, n)
		}
	}
	if accepted == 0 {
		t.Fatal("every properties code was rejected")
	}
	t.Logf("%d codes accepted, %d rejected", accepted, rejected)
}

// TestPropertiesForCodeAcceptsRealWorldValues guards against the limit being
// tightened past what real files use. lc=3 lp=0 pb=2 is the default that
// virtually every xz file in existence carries.
func TestPropertiesForCodeAcceptsRealWorldValues(t *testing.T) {
	for _, want := range []Properties{
		{LC: 3, LP: 0, PB: 2}, // the xz default
		{LC: 0, LP: 0, PB: 0},
		{LC: 4, LP: 0, PB: 2},
		{LC: 0, LP: 4, PB: 4},
		{LC: 2, LP: 2, PB: 1},
	} {
		got, err := PropertiesForCode(want.Code())
		if err != nil {
			t.Errorf("%v rejected: %s", &want, err)
			continue
		}
		if got != want {
			t.Errorf("code %d decoded to %v; want %v",
				want.Code(), &got, &want)
		}
	}
}

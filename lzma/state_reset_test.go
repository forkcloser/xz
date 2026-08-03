// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"math/rand"
	"reflect"
	"testing"
)

// dirtyState runs enough probability updates over a state that every codec has
// moved away from its initial values, so a Reset that misses a field shows up.
func dirtyState(s *state, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	touch := func(p *prob) {
		if rng.Intn(2) == 0 {
			p.inc()
		} else {
			p.dec()
		}
	}
	for i := range s.isMatch {
		touch(&s.isMatch[i])
	}
	for i := range s.isRepG0Long {
		touch(&s.isRepG0Long[i])
	}
	for i := range s.isRep {
		touch(&s.isRep[i])
		touch(&s.isRepG0[i])
		touch(&s.isRepG1[i])
		touch(&s.isRepG2[i])
	}
	for i := range s.litCodec.probs {
		touch(&s.litCodec.probs[i])
	}
	for _, lc := range []*lengthCodec{&s.lenCodec, &s.repLenCodec} {
		touch(&lc.choice[0])
		touch(&lc.choice[1])
		for i := range lc.low {
			for j := range lc.low[i].probs {
				touch(&lc.low[i].probs[j])
			}
			for j := range lc.mid[i].probs {
				touch(&lc.mid[i].probs[j])
			}
		}
		for j := range lc.high.probs {
			touch(&lc.high.probs[j])
		}
	}
	for i := range s.distCodec.posSlotCodecs {
		for j := range s.distCodec.posSlotCodecs[i].probs {
			touch(&s.distCodec.posSlotCodecs[i].probs[j])
		}
	}
	for i := range s.distCodec.posModel {
		for j := range s.distCodec.posModel[i].probs {
			touch(&s.distCodec.posModel[i].probs[j])
		}
	}
	for j := range s.distCodec.alignCodec.probs {
		touch(&s.distCodec.alignCodec.probs[j])
	}
	s.state = uint32(rng.Intn(states))
	for i := range s.rep {
		s.rep[i] = rng.Uint32()
	}
}

// TestStateResetReusesWithoutDrift is what lets Reset keep the probability
// arrays instead of allocating new ones: a state that has been used and reset
// must be indistinguishable from a state that was just built. Reset no longer
// assigns a zero struct, so a field added later without a matching reset line
// would silently survive — this catches that.
func TestStateResetReusesWithoutDrift(t *testing.T) {
	props := []Properties{
		{LC: 3, LP: 0, PB: 2},
		{LC: 0, LP: 0, PB: 0},
		{LC: 4, LP: 0, PB: 2},
		{LC: 0, LP: 4, PB: 4},
		{LC: 2, LP: 2, PB: 1},
	}
	for _, p := range props {
		reused := newState(p)
		dirtyState(reused, 1)
		reused.Reset()

		fresh := newState(p)
		if !reflect.DeepEqual(reused, fresh) {
			t.Errorf("%v: a reset state differs from a fresh one", &p)
		}
	}
}

// TestStateResetAcrossProperties covers the LZMA2 chunk that changes the
// properties mid-stream, which the reader now handles by resetting in place
// rather than building a new state. Switching to properties with a different
// lc+lp resizes the literal codec, so the reused array must end up holding
// exactly what a fresh one would.
func TestStateResetAcrossProperties(t *testing.T) {
	seq := []Properties{
		{LC: 3, LP: 0, PB: 2},
		{LC: 0, LP: 4, PB: 4}, // larger literal codec
		{LC: 1, LP: 0, PB: 0}, // much smaller
		{LC: 4, LP: 0, PB: 2},
		{LC: 3, LP: 0, PB: 2},
	}
	s := newState(seq[0])
	for i, p := range seq {
		dirtyState(s, int64(i))
		s.Properties = p
		s.Reset()

		fresh := newState(p)
		if !reflect.DeepEqual(s, fresh) {
			t.Fatalf("step %d (%v): reset-in-place state differs from a fresh one",
				i, &p)
		}
		if got, want := len(s.litCodec.probs), 0x300<<uint(p.LC+p.LP); got != want {
			t.Fatalf("step %d: literal codec has %d probabilities; want %d",
				i, got, want)
		}
	}
}

// TestStateResetKeepsBackingArrays is the performance claim itself: resetting
// to the same properties must not hand back a different array.
func TestStateResetKeepsBackingArrays(t *testing.T) {
	s := newState(Properties{LC: 3, LP: 0, PB: 2})
	before := &s.litCodec.probs[0]
	beforeHigh := &s.lenCodec.high.probs[0]
	s.Reset()
	if &s.litCodec.probs[0] != before {
		t.Error("Reset reallocated the literal codec probabilities")
	}
	if &s.lenCodec.high.probs[0] != beforeHigh {
		t.Error("Reset reallocated a length codec tree")
	}
}

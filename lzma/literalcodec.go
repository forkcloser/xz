// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

// The literal coder's sizes, from the LZMA specification: each literal
// state has literalProbs probabilities, a bit tree over the eight bits plus
// the match-byte variants; a symbol walking the tree reaches
// literalSymbolEnd once all eight bits are in.
const (
	literalProbs     = 0x300
	literalSymbolEnd = 0x100
)

// literalCodec supports the encoding of literal. It provides 768 probability
// values per literal state. The upper 512 probabilities are used with the
// context of a match bit.
type literalCodec struct {
	probs []prob
}

// Encode encodes the byte s using a range encoder as well as the current LZMA
// encoder state, a match byte and the literal state.
func (c *literalCodec) Encode(e *rangeEncoder, s byte,
	state uint32, match byte, litState uint32,
) (err error) {
	k := litState * literalProbs
	probs := c.probs[k : k+literalProbs]
	symbol := uint32(1)
	r := uint32(s)

	if state >= 7 {
		m := uint32(match)
		for {
			matchBit := (m >> 7) & 1
			m <<= 1
			bit := (r >> 7) & 1
			r <<= 1

			i := ((1 + matchBit) << 8) | symbol
			if err = probs[i].Encode(e, bit); err != nil {
				return err
			}

			symbol = (symbol << 1) | bit
			if matchBit != bit {
				break
			}

			if symbol >= literalSymbolEnd {
				break
			}
		}
	}

	for symbol < literalSymbolEnd {
		bit := (r >> 7) & 1
		r <<= 1

		if err = probs[symbol].Encode(e, bit); err != nil {
			return err
		}

		symbol = (symbol << 1) | bit
	}

	return nil
}

// init initializes the literal codec.
func (c *literalCodec) init(lc, lp int) {
	switch {
	case !(minLC <= lc && lc <= maxLC):
		panic("lc out of range")
	case !(minLP <= lp && lp <= maxLP):
		panic("lp out of range")
	}

	n := literalProbs << uint(lc+lp)
	if cap(c.probs) < n {
		c.probs = make([]prob, n)
	}

	c.probs = c.probs[:n]
	initProbSlice(c.probs)
}

// deepcopy initializes literal codec c as a deep copy of the source, keeping
// the existing backing array when it is big enough, as probTree.deepcopy does.
func (c *literalCodec) deepcopy(src *literalCodec) {
	if c == src {
		return
	}

	if cap(c.probs) < len(src.probs) {
		c.probs = make([]prob, len(src.probs))
	}

	c.probs = c.probs[:len(src.probs)]
	copy(c.probs, src.probs)
}

// decode decodes a literal byte using the range decoder as well as the LZMA
// state, a match byte, and the literal state. The decoder state range/code is
// threaded through in registers (see readOp); the bit decode inlines via
// decodeBitArith and the renormalization byte read is hand-inlined, so the
// loops are free of calls and error branches; read errors are sticky on the
// decoder and checked once per operation.
func (c *literalCodec) decode(d *rangeDecoder,
	state uint32, match byte, litState, rng, code uint32,
) (s byte, nrng, ncode uint32) {
	k := litState * literalProbs
	probs := c.probs[k : k+literalProbs]
	symbol := uint32(1)

	if state >= 7 {
		m := uint32(match)
		for {
			matchBit := (m >> 7) & 1
			m <<= 1
			i := ((1 + matchBit) << 8) | symbol

			var bit uint32

			bit, rng, code = decodeBitArith(&probs[i], rng, code)
			if rng < rcTop {
				rng <<= 8

				code <<= 8
				if pos := d.pos; pos < len(d.buf) {
					code |= uint32(d.buf[pos])
					d.pos = pos + 1
				} else {
					code |= uint32(d.readByteSlow())
				}
			}

			symbol = (symbol << 1) | bit
			if matchBit != bit {
				break
			}

			if symbol >= literalSymbolEnd {
				break
			}
		}
	}

	for symbol < literalSymbolEnd {
		var bit uint32

		bit, rng, code = decodeBitArith(&probs[symbol], rng, code)
		if rng < rcTop {
			rng <<= 8

			code <<= 8
			if pos := d.pos; pos < len(d.buf) {
				code |= uint32(d.buf[pos])
				d.pos = pos + 1
			} else {
				code |= uint32(d.readByteSlow())
			}
		}

		symbol = (symbol << 1) | bit
	}

	s = byte(symbol - literalSymbolEnd)

	return s, rng, code
}

// minLC and maxLC define the range for LC values.
const (
	minLC = 0
	maxLC = 8
)

// minLC and maxLC define the range for LP values.
const (
	minLP = 0
	maxLP = 4
)

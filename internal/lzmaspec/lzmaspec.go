// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package lzmaspec is a port of the LZMA SDK's reference decoder
// (LzmaSpec.cpp, Igor Pavlov, public domain), with an LZMA2 chunk layer
// written from the format's rules as liblzma applies them. It is the oracle
// the lzma package's tests hold the package to: it shares no code with it,
// is written for clarity, not speed, and decodes whole inputs held in
// memory.
//
// The decoder's answers are the specification's: the bytes, whether the
// stream ended with the end marker, and the one ambiguity the reference
// decoder flags as a warning, a range coder state no encoder produces
// (Corrupted).
package lzmaspec

import (
	"errors"
	"io"
)

// Errors of the reference decoder.
var (
	// ErrProperties is a properties byte outside the 225 the format defines.
	ErrProperties = errors.New("lzmaspec: incorrect LZMA properties")
	// ErrData is what the reference decoder calls LZMA_RES_ERROR: a
	// distance before the window's start or beyond the dictionary, a
	// match or literal past the declared size, a marker the range coder
	// does not end on.
	ErrData = errors.New("lzmaspec: LZMA decoding error")
	// ErrMarkerBeforeSize is the end marker met before the declared size.
	ErrMarkerBeforeSize = errors.New("lzmaspec: finished with end marker before the specified size")
	// ErrChunk is an LZMA2 chunk sequence that breaks the chunk rules.
	ErrChunk = errors.New("lzmaspec: invalid LZMA2 chunk")
)

const (
	numBitModelTotalBits = 11
	numMoveBits          = 5
	probInitVal          = (1 << numBitModelTotalBits) / 2
	topValue             = uint32(1) << 24

	numPosBitsMax     = 4
	numStates         = 12
	numLenToPosStates = 4
	numAlignBits      = 4
	endPosModelIndex  = 14
	numFullDistances  = 1 << (endPosModelIndex >> 1)
	matchMinLen       = 2

	// DictMin is the smallest dictionary the decoder allocates.
	DictMin = 1 << 12

	// HeaderLen is the classic format's header.
	HeaderLen = 13
)

type prob uint16

type inputStream struct {
	data []byte
	pos  int
	err  error
}

// readByte returns the next byte, or zero past the end with the error
// recorded; the reference decoder throws there, which the result's error
// stands for.
func (s *inputStream) readByte() byte {
	if s.pos >= len(s.data) {
		s.err = io.ErrUnexpectedEOF

		return 0
	}

	b := s.data[s.pos]
	s.pos++

	return b
}

type outWindow struct {
	buf      []byte
	pos      uint32
	size     uint32
	isFull   bool
	totalPos uint32
	out      []byte
}

// create sizes the window at dictSize without allocating it: a header may
// declare 4 GiB, and the window is only ever as long as the bytes decoded
// into it, so the buffer grows as pos advances and reaches dictSize only
// when the output does.
func (w *outWindow) create(dictSize uint32) {
	w.buf = w.buf[:0]
	w.pos = 0
	w.size = dictSize
	w.isFull = false
	w.totalPos = 0
}

func (w *outWindow) putByte(b byte) {
	if int(w.pos) == len(w.buf) {
		grown := min(uint64(w.size), max(uint64(len(w.buf))*2, 1<<16))
		next := make([]byte, grown)
		copy(next, w.buf)
		w.buf = next
	}

	w.totalPos++
	w.buf[w.pos] = b
	w.pos++

	if w.pos == w.size {
		w.pos = 0
		w.isFull = true
	}

	w.out = append(w.out, b)
}

// getByte reads the byte dist back. A position the window has not reached
// yet reads as zero, which is what the reference's fresh window holds; only
// a match byte of a corrupt stream looks there, since a match's distance is
// checked against the window first.
func (w *outWindow) getByte(dist uint32) byte {
	i := w.pos - dist
	if dist > w.pos {
		i = w.size - dist + w.pos
	}

	if int(i) >= len(w.buf) {
		return 0
	}

	return w.buf[i]
}

func (w *outWindow) copyMatch(dist, length uint32) {
	for ; length > 0; length-- {
		w.putByte(w.getByte(dist))
	}
}

// checkDistance is the one place the port departs from the reference: the
// SDK admits dist == pos, a match starting one byte before the window's
// first, and reads memory it never wrote there; liblzma and the package
// refuse it, and so does the oracle, so a fuzz case finishing through it
// would be the reader's finding and not the oracle's.
func (w *outWindow) checkDistance(dist uint32) bool {
	return dist < w.pos || w.isFull
}

func (w *outWindow) isEmpty() bool {
	return w.pos == 0 && !w.isFull
}

type rangeDecoder struct {
	rng       uint32
	code      uint32
	in        *inputStream
	corrupted bool
}

func (rc *rangeDecoder) init() bool {
	rc.corrupted = false
	rc.rng = 0xFFFFFFFF
	rc.code = 0

	b := rc.in.readByte()

	for range 4 {
		rc.code = (rc.code << 8) | uint32(rc.in.readByte())
	}

	if b != 0 || rc.code == rc.rng {
		rc.corrupted = true
	}

	return b == 0
}

func (rc *rangeDecoder) isFinishedOK() bool { return rc.code == 0 }

func (rc *rangeDecoder) normalize() {
	if rc.rng < topValue {
		rc.rng <<= 8
		rc.code = (rc.code << 8) | uint32(rc.in.readByte())
	}
}

func (rc *rangeDecoder) decodeDirectBits(numBits uint) uint32 {
	var res uint32

	for ; numBits > 0; numBits-- {
		rc.rng >>= 1
		rc.code -= rc.rng
		t := 0 - (rc.code >> 31)
		rc.code += rc.rng & t

		if rc.code == rc.rng {
			rc.corrupted = true
		}

		rc.normalize()

		res <<= 1
		res += t + 1
	}

	return res
}

func (rc *rangeDecoder) decodeBit(p *prob) uint32 {
	v := uint32(*p)
	bound := (rc.rng >> numBitModelTotalBits) * v

	var symbol uint32

	if rc.code < bound {
		v += ((1 << numBitModelTotalBits) - v) >> numMoveBits
		rc.rng = bound
		symbol = 0
	} else {
		v -= v >> numMoveBits
		rc.code -= bound
		rc.rng -= bound
		symbol = 1
	}

	*p = prob(v) // #nosec G115 -- a probability stays within its eleven bits

	rc.normalize()

	return symbol
}

func initProbs(p []prob) {
	for i := range p {
		p[i] = probInitVal
	}
}

func bitTreeReverseDecode(probs []prob, numBits uint, rc *rangeDecoder) uint32 {
	m := uint32(1)

	var symbol uint32

	for i := range numBits {
		bit := rc.decodeBit(&probs[m])
		m <<= 1
		m += bit
		symbol |= bit << i
	}

	return symbol
}

type bitTreeDecoder struct {
	numBits uint
	probs   []prob
}

func newBitTreeDecoder(numBits uint) bitTreeDecoder {
	return bitTreeDecoder{numBits: numBits, probs: make([]prob, 1<<numBits)}
}

func (d *bitTreeDecoder) init() { initProbs(d.probs) }

func (d *bitTreeDecoder) decode(rc *rangeDecoder) uint32 {
	m := uint32(1)
	for range d.numBits {
		m = (m << 1) + rc.decodeBit(&d.probs[m])
	}

	return m - (uint32(1) << d.numBits)
}

func (d *bitTreeDecoder) reverseDecode(rc *rangeDecoder) uint32 {
	return bitTreeReverseDecode(d.probs, d.numBits, rc)
}

type lenDecoder struct {
	choice    prob
	choice2   prob
	lowCoder  [1 << numPosBitsMax]bitTreeDecoder
	midCoder  [1 << numPosBitsMax]bitTreeDecoder
	highCoder bitTreeDecoder
}

func newLenDecoder() lenDecoder {
	d := lenDecoder{highCoder: newBitTreeDecoder(8)}
	for i := range d.lowCoder {
		d.lowCoder[i] = newBitTreeDecoder(3)
		d.midCoder[i] = newBitTreeDecoder(3)
	}

	return d
}

func (d *lenDecoder) init() {
	d.choice = probInitVal
	d.choice2 = probInitVal
	d.highCoder.init()

	for i := range d.lowCoder {
		d.lowCoder[i].init()
		d.midCoder[i].init()
	}
}

func (d *lenDecoder) decode(rc *rangeDecoder, posState uint32) uint32 {
	if rc.decodeBit(&d.choice) == 0 {
		return d.lowCoder[posState].decode(rc)
	}

	if rc.decodeBit(&d.choice2) == 0 {
		return 8 + d.midCoder[posState].decode(rc)
	}

	return 16 + d.highCoder.decode(rc)
}

func updateStateLiteral(state uint32) uint32 {
	switch {
	case state < 4:
		return 0
	case state < 10:
		return state - 3
	default:
		return state - 6
	}
}

func updateStateMatch(state uint32) uint32 {
	if state < 7 {
		return 7
	}

	return 10
}

func updateStateRep(state uint32) uint32 {
	if state < 7 {
		return 8
	}

	return 11
}

func updateStateShortRep(state uint32) uint32 {
	if state < 7 {
		return 9
	}

	return 11
}

// Decoder is the reference LZMA decoder: a window, a range decoder and the
// probability models, with the coder state kept across calls so LZMA2
// chunks can continue one another.
type Decoder struct {
	rangeDec  rangeDecoder
	outWindow outWindow

	markerIsMandatory bool
	lc, pb, lp        uint
	dictSize          uint32

	litProbs []prob

	posSlotDecoder [numLenToPosStates]bitTreeDecoder
	alignDecoder   bitTreeDecoder
	posDecoders    [1 + numFullDistances - endPosModelIndex]prob

	isMatch    [numStates << numPosBitsMax]prob
	isRep      [numStates]prob
	isRepG0    [numStates]prob
	isRepG1    [numStates]prob
	isRepG2    [numStates]prob
	isRep0Long [numStates << numPosBitsMax]prob

	lenDecoder    lenDecoder
	repLenDecoder lenDecoder

	// the coder state, which an LZMA2 chunk may continue
	rep0, rep1, rep2, rep3 uint32
	state                  uint32
}

// Result is how a decode ended.
type Result int

// The three endings of the reference decoder's Decode.
const (
	ResultError Result = iota
	ResultFinishedWithMarker
	ResultFinishedWithoutMarker
)

// Properties are lc, lp, pb and the dictionary size as the classic header
// carries them.
type Properties struct {
	LC, LP, PB uint
	DictSize   uint32
}

// ParseProperties decodes the five property bytes of the classic header.
func ParseProperties(properties []byte) (Properties, error) {
	d := uint(properties[0])
	if d >= 9*5*5 {
		return Properties{}, ErrProperties
	}

	p := Properties{LC: d % 9}
	d /= 9
	p.PB = d / 5
	p.LP = d % 5

	for i := range 4 {
		p.DictSize |= uint32(properties[i+1]) << (8 * i)
	}

	return p, nil
}

// NewDecoder allocates a decoder for the properties, the window at the
// dictionary size or DictMin, whichever is larger.
func NewDecoder(p Properties) *Decoder {
	d := &Decoder{lc: p.LC, lp: p.LP, pb: p.PB, dictSize: max(p.DictSize, DictMin)}
	d.outWindow.create(d.dictSize)
	d.setProperties(p.LC, p.LP, p.PB)

	for i := range d.posSlotDecoder {
		d.posSlotDecoder[i] = newBitTreeDecoder(6)
	}

	d.alignDecoder = newBitTreeDecoder(numAlignBits)
	d.lenDecoder = newLenDecoder()
	d.repLenDecoder = newLenDecoder()

	return d
}

// setProperties sizes the literal coder for lc and lp.
func (d *Decoder) setProperties(lc, lp, pb uint) {
	d.lc, d.lp, d.pb = lc, lp, pb
	d.litProbs = make([]prob, uint32(0x300)<<(lc+lp))
}

func (d *Decoder) initLiterals() { initProbs(d.litProbs) }

func (d *Decoder) decodeLiteral(state, rep0 uint32) {
	var prevByte uint32
	if !d.outWindow.isEmpty() {
		prevByte = uint32(d.outWindow.getByte(1))
	}

	symbol := uint32(1)
	litState := ((d.outWindow.totalPos & ((1 << d.lp) - 1)) << d.lc) + (prevByte >> (8 - d.lc))
	probs := d.litProbs[0x300*litState:]

	if state >= 7 {
		matchByte := uint32(d.outWindow.getByte(rep0 + 1))

		for {
			matchBit := (matchByte >> 7) & 1
			matchByte <<= 1
			bit := d.rangeDec.decodeBit(&probs[((1+matchBit)<<8)+symbol])
			symbol = (symbol << 1) | bit

			if matchBit != bit {
				break
			}

			if symbol >= 0x100 {
				break
			}
		}
	}

	for symbol < 0x100 {
		symbol = (symbol << 1) | d.rangeDec.decodeBit(&probs[symbol])
	}

	d.outWindow.putByte(byte(symbol - 0x100)) // #nosec G115 -- the loop ends at 0x1FF
}

func (d *Decoder) initDist() {
	for i := range d.posSlotDecoder {
		d.posSlotDecoder[i].init()
	}

	d.alignDecoder.init()
	initProbs(d.posDecoders[:])
}

func (d *Decoder) decodeDistance(length uint32) uint32 {
	lenState := min(length, numLenToPosStates-1)

	posSlot := d.posSlotDecoder[lenState].decode(&d.rangeDec)
	if posSlot < 4 {
		return posSlot
	}

	numDirectBits := uint((posSlot >> 1) - 1)
	dist := (2 | (posSlot & 1)) << numDirectBits

	if posSlot < endPosModelIndex {
		dist += bitTreeReverseDecode(d.posDecoders[dist-posSlot:], numDirectBits, &d.rangeDec)
	} else {
		dist += d.rangeDec.decodeDirectBits(numDirectBits-numAlignBits) << numAlignBits
		dist += d.alignDecoder.reverseDecode(&d.rangeDec)
	}

	return dist
}

// resetState initializes every probability model and the coder state, as
// the reference decoder does at the start of a stream.
func (d *Decoder) resetState() {
	d.initLiterals()
	d.initDist()

	initProbs(d.isMatch[:])
	initProbs(d.isRep[:])
	initProbs(d.isRepG0[:])
	initProbs(d.isRepG1[:])
	initProbs(d.isRepG2[:])
	initProbs(d.isRep0Long[:])

	d.lenDecoder.init()
	d.repLenDecoder.init()

	d.rep0, d.rep1, d.rep2, d.rep3 = 0, 0, 0, 0
	d.state = 0
}

// decode is the reference decoder's Decode: the range coder is started on
// the input, the state is reset when asked, and operations are applied
// until the end. It returns the result and the bytes it appended to out.
//
//nolint:gocognit // the reference decode loop, kept in the specification's shape
func (d *Decoder) decode(in *inputStream, resetState, unpackSizeDefined bool, unpackSize uint64) Result {
	d.rangeDec.in = in
	if !d.rangeDec.init() {
		return ResultError
	}

	if resetState {
		d.resetState()
	}

	for {
		// The reference throws at the end of the input; past it every byte
		// read is zero and the loop would run on garbage.
		if in.err != nil {
			return ResultError
		}

		if unpackSizeDefined && unpackSize == 0 && !d.markerIsMandatory {
			if d.rangeDec.isFinishedOK() {
				return ResultFinishedWithoutMarker
			}
		}

		posState := d.outWindow.totalPos & ((1 << d.pb) - 1)

		if d.rangeDec.decodeBit(&d.isMatch[(d.state<<numPosBitsMax)+posState]) == 0 {
			if unpackSizeDefined && unpackSize == 0 {
				return ResultError
			}

			d.decodeLiteral(d.state, d.rep0)
			d.state = updateStateLiteral(d.state)
			unpackSize--

			continue
		}

		var length uint32

		if d.rangeDec.decodeBit(&d.isRep[d.state]) != 0 {
			if unpackSizeDefined && unpackSize == 0 {
				return ResultError
			}

			if d.outWindow.isEmpty() {
				return ResultError
			}

			if d.rangeDec.decodeBit(&d.isRepG0[d.state]) == 0 {
				if d.rangeDec.decodeBit(&d.isRep0Long[(d.state<<numPosBitsMax)+posState]) == 0 {
					d.state = updateStateShortRep(d.state)
					d.outWindow.putByte(d.outWindow.getByte(d.rep0 + 1))

					unpackSize--

					continue
				}
			} else {
				var dist uint32

				if d.rangeDec.decodeBit(&d.isRepG1[d.state]) == 0 {
					dist = d.rep1
				} else {
					if d.rangeDec.decodeBit(&d.isRepG2[d.state]) == 0 {
						dist = d.rep2
					} else {
						dist = d.rep3
						d.rep3 = d.rep2
					}

					d.rep2 = d.rep1
				}

				d.rep1 = d.rep0
				d.rep0 = dist
			}

			length = d.repLenDecoder.decode(&d.rangeDec, posState)
			d.state = updateStateRep(d.state)
		} else {
			d.rep3 = d.rep2
			d.rep2 = d.rep1
			d.rep1 = d.rep0
			length = d.lenDecoder.decode(&d.rangeDec, posState)
			d.state = updateStateMatch(d.state)
			d.rep0 = d.decodeDistance(length)

			if d.rep0 == 0xFFFFFFFF {
				if d.rangeDec.isFinishedOK() {
					return ResultFinishedWithMarker
				}

				return ResultError
			}

			if unpackSizeDefined && unpackSize == 0 {
				return ResultError
			}

			if d.rep0 >= d.dictSize || !d.outWindow.checkDistance(d.rep0) {
				return ResultError
			}
		}

		length += matchMinLen

		isError := false

		if unpackSizeDefined && unpackSize < uint64(length) {
			length = uint32(unpackSize) // #nosec G115 -- below a match length
			isError = true
		}

		d.outWindow.copyMatch(d.rep0+1, length)
		unpackSize -= uint64(length)

		if isError {
			return ResultError
		}
	}
}

// Outcome is what the reference decoder reports for a classic stream.
type Outcome struct {
	// Data is the decoded bytes.
	Data []byte
	// Marker is whether the stream ended with the end marker.
	Marker bool
	// Corrupted is the reference decoder's warning: a range coder state no
	// encoder produces, on a stream that otherwise decoded.
	Corrupted bool
	// Read is how many input bytes the decoder consumed.
	Read int
}

// Decode decodes a classic LZMA stream (a 13-byte header and the data), as
// the reference decoder's main does: the error is ErrProperties, ErrData,
// ErrMarkerBeforeSize, or io.ErrUnexpectedEOF when the input ends inside
// the stream.
func Decode(data []byte) (Outcome, error) {
	if len(data) < HeaderLen {
		return Outcome{}, io.ErrUnexpectedEOF
	}

	p, err := ParseProperties(data[:5])
	if err != nil {
		return Outcome{}, err
	}

	var unpackSize uint64

	unpackSizeDefined := false

	for i := range 8 {
		b := data[5+i]
		if b != 0xFF {
			unpackSizeDefined = true
		}

		unpackSize |= uint64(b) << (8 * i)
	}

	d := NewDecoder(p)
	d.markerIsMandatory = !unpackSizeDefined

	in := &inputStream{data: data, pos: HeaderLen}
	res := d.decode(in, true, unpackSizeDefined, unpackSize)

	if in.err != nil {
		return Outcome{}, in.err
	}

	out := Outcome{Data: d.outWindow.out, Corrupted: d.rangeDec.corrupted, Read: in.pos}

	switch res {
	case ResultError:
		return Outcome{}, ErrData
	case ResultFinishedWithMarker:
		out.Marker = true
		if unpackSizeDefined && uint64(len(out.Data)) != unpackSize {
			return Outcome{}, ErrMarkerBeforeSize
		}
	case ResultFinishedWithoutMarker:
	}

	return out, nil
}

// DecodeLZMA2 decodes an LZMA2 chunk sequence with a dictionary of
// dictSize bytes, applying the chunk rules as liblzma does: the first chunk
// resets the dictionary, a dictionary reset demands new properties before
// the next LZMA chunk, a compressed chunk ends exactly at its sizes with
// the range coder finished and no end marker, and the sequence ends with
// the end chunk. The error is ErrChunk for a rule broken, ErrProperties
// and ErrData as Decode has them, and io.ErrUnexpectedEOF when the input
// ends inside the sequence.
func DecodeLZMA2(data []byte, dictSize uint32) ([]byte, error) {
	out, _, err := DecodeLZMA2N(data, dictSize)

	return out, err
}

// DecodeLZMA2N is DecodeLZMA2 reporting how many bytes the sequence took,
// the end chunk included, for a container that holds more after it.
//
//nolint:gocognit // the chunk rules, each in place
func DecodeLZMA2N(data []byte, dictSize uint32) ([]byte, int, error) {
	in := &inputStream{data: data}

	var d *Decoder

	needProperties := true
	needDictReset := true
	window := outWindow{}
	window.create(max(dictSize, DictMin))

	for {
		control := in.readByte()
		if in.err != nil {
			return nil, in.pos, in.err
		}

		if control == 0x00 {
			return window.out, in.pos, nil
		}

		if control >= 0xE0 || control == 0x01 {
			needProperties = true
			needDictReset = true
		} else if needDictReset {
			return nil, in.pos, ErrChunk
		}

		if control < 0x80 {
			if control > 2 {
				return nil, in.pos, ErrChunk
			}

			size := uint32(in.readByte())<<8 | uint32(in.readByte())
			if in.err != nil {
				return nil, in.pos, in.err
			}

			if needDictReset {
				window.create(window.size)

				needDictReset = false
			}

			for range size + 1 {
				b := in.readByte()
				if in.err != nil {
					return nil, in.pos, in.err
				}

				window.putByte(b)
			}

			continue
		}

		unpacked := uint64(control&0x1F) << 16
		unpacked |= uint64(in.readByte()) << 8
		unpacked |= uint64(in.readByte())
		unpacked++
		packed := uint32(in.readByte())<<8 | uint32(in.readByte())
		packed++

		if in.err != nil {
			return nil, in.pos, in.err
		}

		if control >= 0xC0 {
			props := in.readByte()
			if in.err != nil {
				return nil, in.pos, in.err
			}

			p, err := ParseProperties([]byte{props, 0, 0, 0, 0})
			if err != nil {
				return nil, in.pos, err
			}

			if p.LC+p.LP > 4 {
				return nil, in.pos, ErrProperties
			}

			if d == nil {
				d = NewDecoder(Properties{LC: p.LC, LP: p.LP, PB: p.PB, DictSize: window.size})
			} else {
				d.setProperties(p.LC, p.LP, p.PB)
			}

			needProperties = false
		} else if needProperties || d == nil {
			return nil, in.pos, ErrChunk
		}

		if needDictReset {
			window.create(window.size)

			needDictReset = false
		}

		if in.pos+int(packed) > len(data) {
			return nil, in.pos, io.ErrUnexpectedEOF
		}

		chunk := &inputStream{data: data[in.pos : in.pos+int(packed)]}
		in.pos += int(packed)

		d.outWindow = window
		res := d.decode(chunk, control >= 0xA0, true, unpacked)
		window = d.outWindow

		if chunk.err != nil || res != ResultFinishedWithoutMarker || chunk.pos != len(chunk.data) {
			return nil, in.pos, ErrData
		}
	}
}

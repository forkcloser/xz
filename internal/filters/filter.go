// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package filters implements the xz format's non-LZMA2 filters: the eight
// branch/call/jump (BCJ) converters, which rewrite the relative addresses of
// an architecture's branch instructions as absolute ones, and the delta
// filter. Each converts a buffer in place, in either direction.
//
// The BCJ converters are the file format specification's (section 5.3.2) as
// XZ Utils implements them: every instruction pattern, the distance it is
// relative to and the number of bytes it occupies are fixed there, and an
// encoder's output decodes only under the same rules.
package filters

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The filter IDs of the xz block header.
const (
	DeltaID    = 0x03
	X86ID      = 0x04
	PowerPCID  = 0x05
	IA64ID     = 0x06
	ARMID      = 0x07
	ARMThumbID = 0x08
	SPARCID    = 0x09
	ARM64ID    = 0x0A
	RISCVID    = 0x0B
)

// ErrAlignment reports a BCJ start offset that is not a multiple of the
// filter's alignment.
var ErrAlignment = errors.New("filter: start offset not aligned")

// errUnknownID reports a filter ID that is not a BCJ filter's.
var errUnknownID = errors.New("filter: not a BCJ filter id")

// A BCJ converter rewrites the branch instructions of one architecture.
// Code converts what it can of a buffer and returns how many bytes it
// handled: the bytes after that may be the start of an instruction it has
// not seen the end of, and must be handed to it again at the front of the
// next buffer. At the end of the data, the unconverted tail is the data.
type BCJ struct {
	id     uint64
	pos    uint32 // the stream position of the next buffer's first byte
	encode bool
	// x86 keeps the previous call's end across buffers.
	prevMask uint32
	prevPos  uint32
}

// alignment is the alignment of a BCJ filter's instructions, and the
// granularity of its start offset.
func alignment(id uint64) (uint32, error) {
	switch id {
	case X86ID:
		return 1, nil
	case ARMThumbID, RISCVID:
		return 2, nil
	case PowerPCID, ARMID, SPARCID, ARM64ID:
		return 4, nil
	case IA64ID:
		return 16, nil
	}

	return 0, fmt.Errorf("%w: 0x%02x", errUnknownID, id)
}

// IsBCJ reports whether id is one of the BCJ filters.
func IsBCJ(id uint64) bool {
	_, err := alignment(id)
	return err == nil
}

// NewBCJ returns the converter for a BCJ filter ID, starting at the start
// offset of the block header's filter properties (zero without them), in
// the given direction.
func NewBCJ(id uint64, startOffset uint32, encode bool) (*BCJ, error) {
	a, err := alignment(id)
	if err != nil {
		return nil, err
	}

	if startOffset&(a-1) != 0 {
		return nil, fmt.Errorf("%w: 0x%x for filter 0x%02x", ErrAlignment, startOffset, id)
	}

	return &BCJ{id: id, pos: startOffset, encode: encode, prevPos: startOffset - 5}, nil
}

// Lookahead is the longest tail Code may leave unconverted before the end
// of the data: a buffer at least this long always makes progress.
func (b *BCJ) Lookahead() int {
	switch b.id {
	case X86ID:
		return 5
	case IA64ID:
		return 16
	case RISCVID:
		return 8
	}

	return 4
}

// Code converts buf in place and returns the number of leading bytes it
// converted; the rest must come back at the front of the next call.
func (b *BCJ) Code(buf []byte) int {
	var n int

	switch b.id {
	case X86ID:
		n = b.x86(buf)
	case PowerPCID:
		n = b.powerpc(buf)
	case IA64ID:
		n = b.ia64(buf)
	case ARMID:
		n = b.arm(buf)
	case ARMThumbID:
		n = b.armThumb(buf)
	case SPARCID:
		n = b.sparc(buf)
	case ARM64ID:
		n = b.arm64(buf)
	case RISCVID:
		if b.encode {
			n = b.riscvEncode(buf)
		} else {
			n = b.riscvDecode(buf)
		}
	}

	b.pos += uint32(n)

	return n
}

// test86MSByte is true for the most significant byte of a near call or
// jump displacement, 0x00 or 0xFF.
func test86MSByte(b byte) bool { return b == 0 || b == 0xFF }

// x86 is the BCJ filter for x86 and x86-64: the four-byte displacement
// after an E8 (CALL) or E9 (JMP) opcode, when its top byte is 0x00 or 0xFF,
// with the history of the previous five bytes deciding how many of the
// displacement's bytes are trusted.
//
//nolint:gocognit // the converter is the reference's, step for step
func (b *BCJ) x86(buf []byte) int {
	maskToBitNumber := [5]uint32{0, 1, 2, 2, 3}

	prevMask, prevPos := b.prevMask, b.prevPos
	nowPos := b.pos

	if len(buf) < 5 {
		return 0
	}

	if nowPos-prevPos > 5 {
		prevPos = nowPos - 5
	}

	limit := len(buf) - 5
	i := 0

	for i <= limit {
		c := buf[i]
		if c != 0xE8 && c != 0xE9 {
			i++
			continue
		}

		offset := nowPos + uint32(i) - prevPos
		prevPos = nowPos + uint32(i)

		if offset > 5 {
			prevMask = 0
		} else {
			for range offset {
				prevMask &= 0x77
				prevMask <<= 1
			}
		}

		c = buf[i+4]

		if test86MSByte(c) && (prevMask>>1) <= 4 && (prevMask>>1) != 3 {
			src := uint32(c)<<24 | uint32(buf[i+3])<<16 | uint32(buf[i+2])<<8 | uint32(buf[i+1])

			var dest uint32

			for {
				cur := nowPos + uint32(i) + 5
				if b.encode {
					dest = src + cur
				} else {
					dest = src - cur
				}

				if prevMask == 0 {
					break
				}

				k := maskToBitNumber[prevMask>>1]

				c = byte(dest >> (24 - k*8))
				if !test86MSByte(c) {
					break
				}

				src = dest ^ (uint32(1)<<(32-k*8) - 1)
			}

			buf[i+4] = byte(^(((dest >> 24) & 1) - 1))
			buf[i+3] = byte(dest >> 16)
			buf[i+2] = byte(dest >> 8)
			buf[i+1] = byte(dest)
			i += 5
			prevMask = 0
		} else {
			i++

			prevMask |= 1
			if test86MSByte(c) {
				prevMask |= 0x10
			}
		}
	}

	b.prevMask, b.prevPos = prevMask, prevPos

	return i
}

// arm is the BCJ filter for 32-bit ARM: the 24-bit word offset of a BL
// instruction (condition code 0xEB), little endian.
func (b *BCJ) arm(buf []byte) int {
	size := len(buf) &^ 3

	var i int
	for i = 0; i < size; i += 4 {
		if buf[i+3] != 0xEB {
			continue
		}

		src := uint32(buf[i+2])<<16 | uint32(buf[i+1])<<8 | uint32(buf[i])
		src <<= 2

		pc := b.pos + uint32(i) + 8

		var dest uint32
		if b.encode {
			dest = pc + src
		} else {
			dest = src - pc
		}

		dest >>= 2
		buf[i+2] = byte(dest >> 16)
		buf[i+1] = byte(dest >> 8)
		buf[i] = byte(dest)
	}

	return i
}

// armThumb is the BCJ filter for ARM Thumb: the 22-bit halfword offset of
// a BL instruction pair, two 16-bit little endian halves.
func (b *BCJ) armThumb(buf []byte) int {
	if len(buf) < 4 {
		return 0
	}

	size := len(buf) - 4

	var i int
	for i = 0; i <= size; i += 2 {
		if buf[i+1]&0xF8 != 0xF0 || buf[i+3]&0xF8 != 0xF8 {
			continue
		}

		src := (uint32(buf[i+1])&7)<<19 | uint32(buf[i])<<11 | (uint32(buf[i+3])&7)<<8 | uint32(buf[i+2])
		src <<= 1

		pc := b.pos + uint32(i) + 4

		var dest uint32
		if b.encode {
			dest = pc + src
		} else {
			dest = src - pc
		}

		dest >>= 1
		buf[i+1] = 0xF0 | byte((dest>>19)&0x7)
		buf[i] = byte(dest >> 11)
		buf[i+3] = 0xF8 | byte((dest>>8)&0x7)
		buf[i+2] = byte(dest)
		i += 2
	}

	return i
}

// arm64 is the BCJ filter for ARM64: the 26-bit word offset of a BL
// instruction, and the 21-bit page offset of an ADRP instruction when it is
// within +-512 MiB, little endian.
func (b *BCJ) arm64(buf []byte) int {
	size := len(buf) &^ 3

	var i int
	for i = 0; i < size; i += 4 {
		pc := b.pos + uint32(i)
		instr := binary.LittleEndian.Uint32(buf[i:])

		switch {
		case instr>>26 == 0x25:
			src := instr
			instr = 0x94000000

			pc >>= 2
			if !b.encode {
				pc = 0 - pc
			}

			instr |= (src + pc) & 0x03FFFFFF
			binary.LittleEndian.PutUint32(buf[i:], instr)
		case instr&0x9F000000 == 0x90000000:
			src := (instr>>29)&3 | (instr>>3)&0x001FFFFC
			if (src+0x00020000)&0x001C0000 != 0 {
				continue
			}

			instr &= 0x9000001F

			pc >>= 12
			if !b.encode {
				pc = 0 - pc
			}

			dest := src + pc
			instr |= (dest & 3) << 29
			instr |= (dest & 0x0003FFFC) << 3
			instr |= (0 - (dest & 0x00020000)) & 0x00E00000
			binary.LittleEndian.PutUint32(buf[i:], instr)
		}
	}

	return i
}

// powerpc is the BCJ filter for big endian PowerPC: the 24-bit word offset
// of a bl instruction (opcode 18, AA 0, LK 1).
func (b *BCJ) powerpc(buf []byte) int {
	size := len(buf) &^ 3

	var i int
	for i = 0; i < size; i += 4 {
		if buf[i]>>2 != 0x12 || buf[i+3]&3 != 1 {
			continue
		}

		src := (uint32(buf[i])&3)<<24 | uint32(buf[i+1])<<16 | uint32(buf[i+2])<<8 | uint32(buf[i+3])&^3

		pc := b.pos + uint32(i)

		var dest uint32
		if b.encode {
			dest = pc + src
		} else {
			dest = src - pc
		}

		buf[i] = 0x48 | byte((dest>>24)&0x03)
		buf[i+1] = byte(dest >> 16)
		buf[i+2] = byte(dest >> 8)
		buf[i+3] &= 0x03
		buf[i+3] |= byte(dest)
	}

	return i
}

// sparc is the BCJ filter for SPARC: the 22 significant bits of a call
// instruction's 30-bit word offset, big endian, when the offset is within
// the range the sign-extended 22 bits reach.
func (b *BCJ) sparc(buf []byte) int {
	size := len(buf) &^ 3

	var i int
	for i = 0; i < size; i += 4 {
		if !(buf[i] == 0x40 && buf[i+1]&0xC0 == 0x00) && !(buf[i] == 0x7F && buf[i+1]&0xC0 == 0xC0) {
			continue
		}

		src := uint32(buf[i])<<24 | uint32(buf[i+1])<<16 | uint32(buf[i+2])<<8 | uint32(buf[i+3])
		src <<= 2

		pc := b.pos + uint32(i)

		var dest uint32
		if b.encode {
			dest = pc + src
		} else {
			dest = src - pc
		}

		dest >>= 2
		dest = ((0-((dest>>22)&1))<<22)&0x3FFFFFFF | dest&0x3FFFFF | 0x40000000

		buf[i] = byte(dest >> 24)
		buf[i+1] = byte(dest >> 16)
		buf[i+2] = byte(dest >> 8)
		buf[i+3] = byte(dest)
	}

	return i
}

// ia64 is the BCJ filter for IA-64: the 21-bit bundle offset of a br.call
// instruction in each slot of a 16-byte bundle whose template allows one,
// the 41-bit slots unaligned to bytes.
//
//nolint:gocognit // the converter is the reference's, step for step
func (b *BCJ) ia64(buf []byte) int {
	branchTable := [32]uint32{
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
		4, 4, 6, 6, 0, 0, 7, 7,
		4, 4, 0, 0, 4, 4, 0, 0,
	}

	size := len(buf) &^ 15

	var i int
	for i = 0; i < size; i += 16 {
		mask := branchTable[buf[i]&0x1F]
		bitPos := uint32(5)

		for slot := uint32(0); slot < 3; slot, bitPos = slot+1, bitPos+41 {
			if (mask>>slot)&1 == 0 {
				continue
			}

			bytePos := int(bitPos >> 3)
			bitRes := bitPos & 7

			var instruction uint64
			for j := range 6 {
				instruction += uint64(buf[i+j+bytePos]) << (8 * j)
			}

			instNorm := instruction >> bitRes

			if (instNorm>>37)&0xF != 0x5 || (instNorm>>9)&0x7 != 0 {
				continue
			}

			src := uint32((instNorm >> 13) & 0xFFFFF)
			src |= uint32((instNorm>>36)&1) << 20
			src <<= 4

			pc := b.pos + uint32(i)

			var dest uint32
			if b.encode {
				dest = pc + src
			} else {
				dest = src - pc
			}

			dest >>= 4

			instNorm &^= uint64(0x8FFFFF) << 13
			instNorm |= uint64(dest&0xFFFFF) << 13
			instNorm |= uint64(dest&0x100000) << (36 - 20)

			instruction &= uint64(1)<<bitRes - 1
			instruction |= instNorm << bitRes

			for j := range 6 {
				buf[i+j+bytePos] = byte(instruction >> (8 * j))
			}
		}
	}

	return i
}

// riscvDecode is the decoding half of the RISC-V BCJ filter: a JAL with
// rd x1 (the 0xEF byte), and an AUIPC+inst2 pair the encoder turned into
// its absolute form, told apart from a plain AUIPC by the bits the encoder
// set.
//
//nolint:gocognit // the converter is the reference's, step for step
func (b *BCJ) riscvDecode(buf []byte) int {
	if len(buf) < 8 {
		return 0
	}

	size := len(buf) - 8

	var i int
	for i = 0; i <= size; i += 2 {
		inst := uint32(buf[i])

		switch {
		case inst == 0xEF:
			b1 := uint32(buf[i+1])
			if b1&0x0D != 0 {
				continue
			}

			b2 := uint32(buf[i+2])
			b3 := uint32(buf[i+3])
			pc := b.pos + uint32(i)

			addr := (b1&0xF0)<<13 | b2<<9 | b3<<1
			addr -= pc

			buf[i+1] = byte(b1&0x0F | (addr>>8)&0xF0)
			buf[i+2] = byte((addr>>16)&0x0F | (addr>>7)&0x10 | (addr<<4)&0xE0)
			buf[i+3] = byte((addr>>4)&0x7F | (addr>>13)&0x80)

			i += 4 - 2
		case inst&0x7F == 0x17:
			inst |= uint32(buf[i+1]) << 8
			inst |= uint32(buf[i+2]) << 16
			inst |= uint32(buf[i+3]) << 24

			var inst2 uint32

			if inst&0xE80 != 0 {
				inst2 = binary.LittleEndian.Uint32(buf[i+4:])

				if notAUIPCPair(inst, inst2) {
					i += 6 - 2
					continue
				}

				addr := inst & 0xFFFFF000
				addr += inst2 >> 20

				inst = 0x17 | 2<<7 | inst2<<12
				inst2 = addr
			} else {
				inst2RS1 := inst >> 27

				if notSpecialAUIPC(inst, inst2RS1) {
					i += 4 - 2
					continue
				}

				addr := binary.BigEndian.Uint32(buf[i+4:])
				addr -= b.pos + uint32(i)

				inst2 = inst>>12 | addr<<20

				inst = 0x17 | inst2RS1<<7 | (addr+0x800)&0xFFFFF000
			}

			binary.LittleEndian.PutUint32(buf[i:], inst)
			binary.LittleEndian.PutUint32(buf[i+4:], inst2)

			i += 8 - 2
		}
	}

	return i
}

// riscvEncode is the encoding half of the RISC-V BCJ filter.
//
//nolint:gocognit // the converter is the reference's, step for step
func (b *BCJ) riscvEncode(buf []byte) int {
	if len(buf) < 8 {
		return 0
	}

	size := len(buf) - 8

	var i int
	for i = 0; i <= size; i += 2 {
		inst := uint32(buf[i])

		switch {
		case inst == 0xEF:
			b1 := uint32(buf[i+1])
			if b1&0x0D != 0 {
				continue
			}

			b2 := uint32(buf[i+2])
			b3 := uint32(buf[i+3])
			pc := b.pos + uint32(i)

			addr := (b1&0xF0)<<8 | (b2&0x0F)<<16 | (b2&0x10)<<7 | (b2&0xE0)>>4 | (b3&0x7F)<<4 | (b3&0x80)<<13
			addr += pc

			buf[i+1] = byte(b1&0x0F | (addr>>13)&0xF0)
			buf[i+2] = byte(addr >> 9)
			buf[i+3] = byte(addr >> 1)

			i += 4 - 2
		case inst&0x7F == 0x17:
			inst |= uint32(buf[i+1]) << 8
			inst |= uint32(buf[i+2]) << 16
			inst |= uint32(buf[i+3]) << 24

			if inst&0xE80 != 0 {
				inst2 := binary.LittleEndian.Uint32(buf[i+4:])

				if notAUIPCPair(inst, inst2) {
					i += 6 - 2
					continue
				}

				addr := inst & 0xFFFFF000
				addr += inst2>>20 - (inst2>>19)&0x1000
				addr += b.pos + uint32(i)

				inst = 0x17 | 2<<7 | inst2<<12

				binary.LittleEndian.PutUint32(buf[i:], inst)
				binary.BigEndian.PutUint32(buf[i+4:], addr)
			} else {
				fakeRS1 := inst >> 27

				if notSpecialAUIPC(inst, fakeRS1) {
					i += 4 - 2
					continue
				}

				fakeAddr := binary.LittleEndian.Uint32(buf[i+4:])
				fakeInst2 := inst>>12 | fakeAddr<<20

				inst = 0x17 | fakeRS1<<7 | fakeAddr&0xFFFFF000

				binary.LittleEndian.PutUint32(buf[i:], inst)
				binary.LittleEndian.PutUint32(buf[i+4:], fakeInst2)
			}

			i += 8 - 2
		}
	}

	return i
}

// notAUIPCPair reports that an AUIPC and the instruction after it are not
// a pair the filter converts: the second must be a load or JALR (low opcode
// bits 3) whose rs1 is the AUIPC's rd.
func notAUIPCPair(auipc, inst2 uint32) bool {
	return (auipc<<8^(inst2-3))&0xF8003 != 0
}

// notSpecialAUIPC reports that an AUIPC with rd x2 is not the encoder's
// marker of a converted pair.
func notSpecialAUIPC(auipc, inst2RS1 uint32) bool {
	return (auipc-0x3117)<<18 >= inst2RS1&0x1D
}

// Delta is the delta filter: each byte minus (encoding) or plus (decoding)
// the byte a fixed distance before it.
type Delta struct {
	distance int
	pos      byte
	history  [256]byte
}

// MinDistance and MaxDistance bound the delta distance.
const (
	MinDistance = 1
	MaxDistance = 256
)

// ErrDistance reports a delta distance outside MinDistance to MaxDistance.
var ErrDistance = errors.New("filter: delta distance out of range")

// NewDelta returns a delta coder for a distance.
func NewDelta(distance int) (*Delta, error) {
	if distance < MinDistance || distance > MaxDistance {
		return nil, fmt.Errorf("%w: %d", ErrDistance, distance)
	}

	return &Delta{distance: distance}, nil
}

// Decode converts buf in place, every byte.
func (d *Delta) Decode(buf []byte) {
	for i := range buf {
		buf[i] += d.history[(d.distance+int(d.pos))&0xFF]
		d.history[d.pos] = buf[i]
		d.pos--
	}
}

// Encode converts buf in place, every byte.
func (d *Delta) Encode(buf []byte) {
	for i := range buf {
		tmp := d.history[(d.distance+int(d.pos))&0xFF]
		d.history[d.pos] = buf[i]
		d.pos--
		buf[i] -= tmp
	}
}

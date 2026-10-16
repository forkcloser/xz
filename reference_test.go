// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"hash/crc64"

	"github.com/forkcloser/xz/internal/lzmaspec"
)

// The reference container: a .xz stream written and read from the file
// format specification (xz-file-format.txt), with the LZMA2 payload decoded
// by the reference decoder. It shares nothing with the package: the tests
// hold the package's writer to refParse and its readers to refStream.

// refBlock is one block of a reference stream: its data and that data as
// an LZMA2 chunk sequence.
type refBlock struct {
	data  []byte
	lzma2 []byte
}

// refOptions shape a reference stream.
type refOptions struct {
	check    byte // None, CRC32, CRC64 or SHA256
	sizes    bool // Compressed Size and Uncompressed Size in every block header
	dictCode byte // the LZMA2 filter's dictionary size code
}

// refVLI encodes a variable-length integer: seven bits per byte, least
// significant first, the high bit set on every byte but the last.
func refVLI(v uint64) []byte {
	var out []byte

	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}

	return append(out, byte(v))
}

// refCRC32 appends the little-endian CRC-32 of data.
func refCRC32(data []byte) []byte {
	var c [4]byte

	binary.LittleEndian.PutUint32(c[:], crc32.ChecksumIEEE(data))

	return c[:]
}

// refCheck is the block check of data for the check type.
func refCheck(check byte, data []byte) []byte {
	switch check {
	case 0:
		return nil
	case 1:
		return refCRC32(data)
	case 4:
		var c [8]byte

		binary.LittleEndian.PutUint64(c[:], crc64.Checksum(data, crc64.MakeTable(crc64.ECMA)))

		return c[:]
	case 0xa:
		sum := sha256.Sum256(data)

		return sum[:]
	default:
		panic("reference: check type without a size")
	}
}

// refCheckLen is the check's size in bytes.
func refCheckLen(check byte) int {
	switch check {
	case 0:
		return 0
	case 1:
		return 4
	case 4:
		return 8
	case 0xa:
		return 32
	default:
		return -1
	}
}

// refPad appends zero bytes to a multiple of four.
func refPad(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}

	return b
}

// refBlockHeader is a block header: the size byte, the flags, the sizes if
// asked, the LZMA2 filter with its dictionary code, zero padding to the
// declared size, and the CRC-32.
func refBlockHeader(o refOptions, b refBlock) []byte {
	var flags byte
	if o.sizes {
		flags |= 0x40 | 0x80
	}

	content := []byte{flags}
	if o.sizes {
		content = append(content, refVLI(uint64(len(b.lzma2)))...)
		content = append(content, refVLI(uint64(len(b.data)))...)
	}

	content = append(content, 0x21, 0x01, o.dictCode)

	// 1 size byte + content + padding + 4 CRC bytes = (size+1)*4
	size := byte(0)
	for int(size+1)*4 < 5+len(content) {
		size++
	}

	h := append([]byte{size}, content...)
	for len(h) < int(size+1)*4-4 {
		h = append(h, 0)
	}

	return append(h, refCRC32(h)...)
}

// refStream writes one stream: header, blocks, index, footer.
func refStream(o refOptions, blocks []refBlock) []byte {
	flags := []byte{0x00, o.check}
	out := append([]byte("\xfd7zXZ\x00"), flags...)
	out = append(out, refCRC32(flags)...)

	index := []byte{0x00}
	index = append(index, refVLI(uint64(len(blocks)))...)

	for _, b := range blocks {
		header := refBlockHeader(o, b)
		out = append(out, header...)
		out = append(out, b.lzma2...)
		out = refPad(out)
		out = append(out, refCheck(o.check, b.data)...)

		unpadded := len(header) + len(b.lzma2) + refCheckLen(o.check)
		index = append(index, refVLI(uint64(unpadded))...)
		index = append(index, refVLI(uint64(len(b.data)))...)
	}

	index = refPad(index)
	index = append(index, refCRC32(index)...)
	out = append(out, index...)

	var backward [4]byte

	binary.LittleEndian.PutUint32(backward[:], uint32(len(index)/4-1))

	footer := append(backward[:], flags...)
	out = append(out, refCRC32(footer)...)
	out = append(out, footer...)

	return append(out, "YZ"...)
}

// refFile concatenates streams with padding (zero bytes, a multiple of
// four) between and after them.
func refFile(padding int, streams ...[]byte) []byte {
	var out []byte

	for i, s := range streams {
		if i > 0 {
			out = append(out, make([]byte, padding)...)
		}

		out = append(out, s...)
	}

	return append(out, make([]byte, padding)...)
}

// Errors of the reference parser: the file is not a .xz file, uses a
// feature the reference does not support, or ends early. The readers are
// held to refusing what the reference refuses, not to its reasons.
var (
	errRefCorrupt     = errors.New("reference: corrupt")
	errRefUnsupported = errors.New("reference: unsupported")
	errRefShort       = errors.New("reference: input ends inside the stream")
)

// refParser walks a file.
type refParser struct {
	data []byte
	pos  int
}

func (p *refParser) take(n int) ([]byte, error) {
	if p.pos+n > len(p.data) {
		return nil, errRefShort
	}

	b := p.data[p.pos : p.pos+n]
	p.pos += n

	return b, nil
}

// vli reads a variable-length integer: at most nine bytes, the last
// without the continuation bit, and no leading zero byte but the first.
func (p *refParser) vli() (uint64, error) {
	var v uint64

	for i := range 9 {
		b, err := p.take(1)
		if err != nil {
			return 0, err
		}

		if i > 0 && b[0] == 0 {
			return 0, errRefCorrupt
		}

		v |= uint64(b[0]&0x7F) << (7 * i)

		if b[0]&0x80 == 0 {
			return v, nil
		}
	}

	return 0, errRefCorrupt
}

// refDictSize is the dictionary size a code stands for.
func refDictSize(code byte) (uint32, error) {
	switch {
	case code > 40:
		return 0, errRefUnsupported
	case code == 40:
		return 1<<32 - 1, nil
	default:
		return (2 | uint32(code&1)) << (code/2 + 11), nil
	}
}

type refRecord struct {
	unpadded     uint64
	uncompressed uint64
}

// block parses one block starting at the size byte already read.
func (p *refParser) block(size, check byte) ([]byte, refRecord, error) {
	headerLen := (int(size) + 1) * 4
	headerStart := p.pos - 1

	rest, err := p.take(headerLen - 1)
	if err != nil {
		return nil, refRecord{}, err
	}

	header := p.data[headerStart : headerStart+headerLen]
	if crc32.ChecksumIEEE(header[:headerLen-4]) != binary.LittleEndian.Uint32(header[headerLen-4:]) {
		return nil, refRecord{}, errRefCorrupt
	}

	flags := rest[0]
	if flags&0x3C != 0 {
		return nil, refRecord{}, errRefCorrupt
	}

	if flags&0x03 != 0 {
		return nil, refRecord{}, errRefUnsupported // more than one filter
	}

	fields := &refParser{data: rest[1 : len(rest)-4]}

	compressed, uncompressed := int64(-1), int64(-1)

	if flags&0x40 != 0 {
		v, verr := fields.vli()
		if verr != nil || v == 0 || v >= 1<<63 {
			return nil, refRecord{}, errRefCorrupt
		}

		compressed = int64(v)
	}

	if flags&0x80 != 0 {
		v, verr := fields.vli()
		if verr != nil || v >= 1<<63 {
			return nil, refRecord{}, errRefCorrupt
		}

		uncompressed = int64(v)
	}

	id, err := fields.vli()
	if err != nil {
		return nil, refRecord{}, errRefCorrupt
	}

	if id != 0x21 {
		if id >= 1<<62 {
			return nil, refRecord{}, errRefCorrupt
		}

		return nil, refRecord{}, errRefUnsupported
	}

	propsLen, err := fields.vli()
	if err != nil || propsLen != 1 {
		return nil, refRecord{}, errRefCorrupt
	}

	dictCode, err := fields.take(1)
	if err != nil {
		return nil, refRecord{}, errRefCorrupt
	}

	dictSize, err := refDictSize(dictCode[0])
	if err != nil {
		return nil, refRecord{}, err
	}

	if !bytes.Equal(fields.data[fields.pos:], make([]byte, len(fields.data)-fields.pos)) {
		return nil, refRecord{}, errRefCorrupt
	}

	data, n, err := lzmaspec.DecodeLZMA2N(p.data[p.pos:], dictSize)
	if err != nil {
		if errors.Is(err, errRefShort) || errors.Is(err, lzmaspec.ErrChunk) || errors.Is(err, lzmaspec.ErrData) ||
			errors.Is(err, lzmaspec.ErrProperties) {
			return nil, refRecord{}, errRefCorrupt
		}

		return nil, refRecord{}, errRefShort
	}

	p.pos += n

	if (compressed >= 0 && compressed != int64(n)) || (uncompressed >= 0 && uncompressed != int64(len(data))) {
		return nil, refRecord{}, errRefCorrupt
	}

	pad, err := p.take((4 - n%4) % 4)
	if err != nil {
		return nil, refRecord{}, err
	}

	if !bytes.Equal(pad, make([]byte, len(pad))) {
		return nil, refRecord{}, errRefCorrupt
	}

	sum, err := p.take(refCheckLen(check))
	if err != nil {
		return nil, refRecord{}, err
	}

	if !bytes.Equal(sum, refCheck(check, data)) {
		return nil, refRecord{}, errRefCorrupt
	}

	return data, refRecord{uint64(headerLen + n + refCheckLen(check)), uint64(len(data))}, nil
}

// stream parses one stream at the current position.
func (p *refParser) stream() ([]byte, error) {
	magic, err := p.take(6)
	if err != nil {
		return nil, err
	}

	if string(magic) != "\xfd7zXZ\x00" {
		return nil, errRefCorrupt
	}

	flagsAndCRC, err := p.take(6)
	if err != nil {
		return nil, err
	}

	flags := flagsAndCRC[:2]
	if crc32.ChecksumIEEE(flags) != binary.LittleEndian.Uint32(flagsAndCRC[2:]) {
		return nil, errRefCorrupt
	}

	if flags[0] != 0 || flags[1]&0xF0 != 0 {
		return nil, errRefCorrupt
	}

	check := flags[1]
	if refCheckLen(check) < 0 {
		return nil, errRefUnsupported
	}

	var (
		out     []byte
		records []refRecord
	)

	for {
		b, terr := p.take(1)
		if terr != nil {
			return nil, terr
		}

		if b[0] == 0 {
			break
		}

		data, rec, berr := p.block(b[0], check)
		if berr != nil {
			return nil, berr
		}

		out = append(out, data...)
		records = append(records, rec)
	}

	indexStart := p.pos - 1

	count, err := p.vli()
	if err != nil {
		return nil, err
	}

	if count != uint64(len(records)) {
		return nil, errRefCorrupt
	}

	for _, rec := range records {
		unpadded, uerr := p.vli()
		if uerr != nil {
			return nil, uerr
		}

		uncompressed, uerr := p.vli()
		if uerr != nil {
			return nil, uerr
		}

		if unpadded != rec.unpadded || uncompressed != rec.uncompressed {
			return nil, errRefCorrupt
		}
	}

	pad, err := p.take((4 - (p.pos-indexStart)%4) % 4)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(pad, make([]byte, len(pad))) {
		return nil, errRefCorrupt
	}

	indexCRC, err := p.take(4)
	if err != nil {
		return nil, err
	}

	if crc32.ChecksumIEEE(p.data[indexStart:p.pos-4]) != binary.LittleEndian.Uint32(indexCRC) {
		return nil, errRefCorrupt
	}

	indexLen := p.pos - indexStart

	footer, err := p.take(12)
	if err != nil {
		return nil, err
	}

	if crc32.ChecksumIEEE(footer[4:10]) != binary.LittleEndian.Uint32(footer[:4]) {
		return nil, errRefCorrupt
	}

	if int(binary.LittleEndian.Uint32(footer[4:8]))+1 != indexLen/4 {
		return nil, errRefCorrupt
	}

	if !bytes.Equal(footer[8:10], flags) || string(footer[10:]) != "YZ" {
		return nil, errRefCorrupt
	}

	return out, nil
}

// refParse decodes a whole file: one stream, then any number of padding
// groups and streams, to the end. Padding before the first stream, a
// partial padding group, or anything else is corrupt.
func refParse(data []byte) ([]byte, error) {
	p := &refParser{data: data}

	var out []byte

	first := true

	for p.pos < len(p.data) {
		if !first {
			for p.pos+4 <= len(p.data) && bytes.Equal(p.data[p.pos:p.pos+4], []byte{0, 0, 0, 0}) {
				p.pos += 4
			}

			if p.pos == len(p.data) {
				break
			}

			if len(p.data)-p.pos < 4 && bytes.Equal(p.data[p.pos:], make([]byte, len(p.data)-p.pos)) {
				return nil, errRefCorrupt
			}
		}

		s, err := p.stream()
		if err != nil {
			return nil, err
		}

		out = append(out, s...)
		first = false
	}

	if first {
		return nil, errRefShort
	}

	return out, nil
}

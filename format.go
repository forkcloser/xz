// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"github.com/forkcloser/xz/lzma"
)

// Values the footer and block-header encoders refuse to write.
var (
	errIndexSize    = errors.New("xz: index size out of range")
	errIndexAlign   = errors.New("xz: index size not aligned to four bytes")
	errFilterCount  = errors.New("xz: filter count wrong")
	errLZMA2NotLast = errors.New("xz: LZMA2 filter is not the last")
	errLastNotLZMA2 = errors.New("xz: last filter must be the LZMA2 filter")
)

// initialIndexCap is how many index records readIndex makes room for
// before any has arrived.
const initialIndexCap = 64

// allZeros checks whether a given byte slice has only zeros.
func allZeros(p []byte) bool {
	for _, c := range p {
		if c != 0 {
			return false
		}
	}

	return true
}

// padLen returns the length of the padding required for the given
// argument.
func padLen(n int64) int {
	k := int(n % 4)
	if k > 0 {
		k = 4 - k
	}

	return k
}

// The stream header.

// headerMagic stores the magic bytes for the header.
const headerMagic = "\xfd7zXZ\x00"

// HeaderLen provides the length of the xz file header.
const HeaderLen = 12

// Constants for the checksum methods supported by xz.
const (
	None   byte = 0x0
	CRC32  byte = 0x1
	CRC64  byte = 0x4
	SHA256 byte = 0xa
)

// errInvalidFlags indicates that flags are invalid.
var errInvalidFlags = corruptf("xz: invalid flags")

// verifyFlags returns the error errInvalidFlags if the value is
// invalid.
func verifyFlags(flags byte) error {
	switch flags {
	case None, CRC32, CRC64, SHA256:
		return nil
	default:
		return errInvalidFlags
	}
}

// flagString returns the string representation for the given flags.
func flagString(flags byte) string {
	switch flags {
	case None:
		return "None"
	case CRC32:
		return "CRC-32"
	case CRC64:
		return "CRC-64"
	case SHA256:
		return "SHA-256"
	}

	return "invalid"
}

// newHashFunc returns a function that creates hash instances for the
// hash method encoded in flags.
func newHashFunc(flags byte) (newHash func() hash.Hash, err error) {
	switch flags {
	case None:
		newHash = newNoneHash
	case CRC32:
		newHash = newCRC32
	case CRC64:
		newHash = newCRC64
	case SHA256:
		newHash = sha256.New
	default:
		err = errInvalidFlags
	}

	return newHash, err
}

// header provides the actual content of the xz file header: the flags.
//
//nolint:recvcheck // String takes a value, so values and pointers print alike; the binary codecs need the pointer
type header struct {
	flags byte
}

// Errors returned by readHeader.
var errHeaderMagic = corruptf("xz: invalid header magic bytes")

// ValidHeader checks whether data is a correct xz file header. The
// length of data must be HeaderLen.
func ValidHeader(data []byte) bool {
	var h header

	err := h.UnmarshalBinary(data)

	return err == nil
}

// String returns a string representation of the flags.
func (h header) String() string {
	return flagString(h.flags)
}

// UnmarshalBinary reads header from the provided data slice.
func (h *header) UnmarshalBinary(data []byte) error {
	// header length
	if len(data) != HeaderLen {
		return corruptf("xz: wrong file header length")
	}

	// magic header
	if string(data[:6]) != headerMagic {
		return errHeaderMagic
	}

	// checksum
	crc := crc32.NewIEEE()
	crc.Write(data[6:8])

	if binary.LittleEndian.Uint32(data[8:]) != crc.Sum32() {
		return corruptf("xz: invalid checksum for file header")
	}

	// stream flags
	if data[6] != 0 {
		return errInvalidFlags
	}

	flags := data[7]
	if err := verifyFlags(flags); err != nil {
		return err
	}

	h.flags = flags

	return nil
}

// MarshalBinary generates the xz file header.
func (h *header) MarshalBinary() (data []byte, err error) {
	if err = verifyFlags(h.flags); err != nil {
		return nil, err
	}

	data = make([]byte, HeaderLen)
	copy(data, headerMagic)
	data[7] = h.flags

	crc := crc32.NewIEEE()
	crc.Write(data[6:8])
	binary.LittleEndian.PutUint32(data[8:], crc.Sum32())

	return data, nil
}

// The stream footer.

// footerLen defines the length of the footer.
const footerLen = 12

// footerMagic contains the footer magic bytes.
const footerMagic = "YZ"

// footer represents the content of the xz file footer.
//
//nolint:recvcheck // String takes a value, so values and pointers print alike; the binary codecs need the pointer
type footer struct {
	indexSize int64
	flags     byte
}

// String prints a string representation of the footer structure.
func (f footer) String() string {
	return fmt.Sprintf("%s index size %d", flagString(f.flags), f.indexSize)
}

// Minimum and maximum for the size of the index (backward size).
const (
	minIndexSize = 4
	maxIndexSize = (1 << 32) * 4
)

// MarshalBinary converts footer values into an xz file footer. Note
// that the footer value is checked for correctness.
func (f *footer) MarshalBinary() (data []byte, err error) {
	if err = verifyFlags(f.flags); err != nil {
		return nil, err
	}

	if !(minIndexSize <= f.indexSize && f.indexSize <= maxIndexSize) {
		return nil, errIndexSize
	}

	if f.indexSize%4 != 0 {
		return nil, errIndexAlign
	}

	data = make([]byte, footerLen)

	// backward size (index size)
	s := (f.indexSize / 4) - 1
	binary.LittleEndian.PutUint32(data[4:], uint32(s))
	// flags
	data[9] = f.flags
	// footer magic
	copy(data[10:], footerMagic)

	// CRC-32
	crc := crc32.NewIEEE()
	crc.Write(data[4:10])
	binary.LittleEndian.PutUint32(data, crc.Sum32())

	return data, nil
}

// UnmarshalBinary sets the footer value by unmarshalling an xz file
// footer.
func (f *footer) UnmarshalBinary(data []byte) error {
	if len(data) != footerLen {
		return corruptf("xz: wrong footer length")
	}

	// magic bytes
	if string(data[10:]) != footerMagic {
		return corruptf("xz: footer magic invalid")
	}

	// CRC-32
	crc := crc32.NewIEEE()
	crc.Write(data[4:10])

	if binary.LittleEndian.Uint32(data) != crc.Sum32() {
		return corruptf("xz: footer checksum error")
	}

	var g footer
	// backward size (index size)
	g.indexSize = (int64(binary.LittleEndian.Uint32(data[4:])) + 1) * 4

	// flags
	if data[8] != 0 {
		return errInvalidFlags
	}

	g.flags = data[9]
	if err := verifyFlags(g.flags); err != nil {
		return err
	}

	*f = g

	return nil
}

// The block header.

// blockHeader represents the content of an xz block header.
//
//nolint:recvcheck // String takes a value, so values and pointers print alike; the binary codecs need the pointer
type blockHeader struct {
	compressedSize   int64
	uncompressedSize int64
	filters          []filter
}

// String converts the block header into a string.
func (h blockHeader) String() string {
	var buf bytes.Buffer

	first := true

	if h.compressedSize >= 0 {
		fmt.Fprintf(&buf, "compressed size %d", h.compressedSize)

		first = false
	}

	if h.uncompressedSize >= 0 {
		if !first {
			buf.WriteString(" ")
		}

		fmt.Fprintf(&buf, "uncompressed size %d", h.uncompressedSize)

		first = false
	}

	for _, f := range h.filters {
		if !first {
			buf.WriteString(" ")
		}

		fmt.Fprintf(&buf, "filter %s", f)

		first = false
	}

	return buf.String()
}

// Masks for the block flags.
const (
	filterCountMask         = 0x03
	compressedSizePresent   = 0x40
	uncompressedSizePresent = 0x80
	reservedBlockFlags      = 0x3C
)

// errIndexIndicator signals that an index indicator (0x00) has been found
// instead of an expected block header indicator.
var errIndexIndicator = errors.New("xz: found index indicator")

// readBlockHeader reads the block header.
func readBlockHeader(r io.Reader) (h *blockHeader, n int, err error) {
	var buf bytes.Buffer
	buf.Grow(20)

	// block header size
	z, err := io.CopyN(&buf, r, 1)

	n = int(z)
	if err != nil {
		return nil, n, err
	}

	s := buf.Bytes()[0]
	if s == 0 {
		return nil, n, errIndexIndicator
	}

	// read complete header
	headerLen := (int(s) + 1) * 4
	buf.Grow(headerLen - 1)
	z, err = io.CopyN(&buf, r, int64(headerLen-1))

	n += int(z)
	if err != nil {
		return nil, n, err
	}

	// unmarshal block header
	h = new(blockHeader)
	if err = h.UnmarshalBinary(buf.Bytes()); err != nil {
		return nil, n, err
	}

	return h, n, nil
}

// readSizeInBlockHeader reads the uncompressed or compressed size
// fields in the block header. The present value informs the function
// whether the respective field is actually present in the header.
func readSizeInBlockHeader(r io.ByteReader, present bool) (n int64, err error) {
	if !present {
		return -1, nil
	}

	x, _, err := readUvarint(r)
	if err != nil {
		return 0, err
	}

	if x >= 1<<63 {
		return 0, corruptf("xz: size overflow in block header")
	}

	// #nosec G115 -- x is checked below 1<<63 just above
	return int64(x), nil
}

// UnmarshalBinary unmarshals the block header.
func (h *blockHeader) UnmarshalBinary(data []byte) error {
	// Check header length
	if len(data) == 0 {
		return corruptf("xz: empty block header")
	}

	s := data[0]
	if s == 0 {
		return errIndexIndicator
	}

	headerLen := (int(s) + 1) * 4
	if len(data) != headerLen {
		return corruptf("xz: data length %d; want %d", len(data),
			headerLen)
	}

	n := headerLen - 4

	// Check CRC-32
	crc := crc32.NewIEEE()
	crc.Write(data[:n])

	if crc.Sum32() != binary.LittleEndian.Uint32(data[n:]) {
		return corruptf("xz: checksum error for block header")
	}

	// Block header flags
	flags := data[1]
	if flags&reservedBlockFlags != 0 {
		return corruptf("xz: reserved block header flags set")
	}

	r := bytes.NewReader(data[2:n])

	// Compressed size
	var err error

	h.compressedSize, err = readSizeInBlockHeader(
		r, flags&compressedSizePresent != 0,
	)
	if err != nil {
		return err
	}

	// Uncompressed size
	h.uncompressedSize, err = readSizeInBlockHeader(
		r, flags&uncompressedSizePresent != 0,
	)
	if err != nil {
		return err
	}

	h.filters, err = readFilters(r, int(flags&filterCountMask)+1)
	if err != nil {
		return err
	}

	// Check padding
	// Since headerLen is a multiple of 4 we don't need to check
	// alignment.
	k := r.Len()
	// The standard spec says that the padding should have not more
	// than 3 bytes. However we found paddings of 4 or 5 in the
	// wild. See https://github.com/ulikunitz/xz/pull/11 and
	// https://github.com/ulikunitz/xz/issues/15 (upstream).
	//
	// The only reasonable approach seems to be to ignore the
	// padding size. We still check that all padding bytes are zero.
	if !allZeros(data[n-k : n]) {
		return corruptf("xz: non-zero block header padding")
	}

	return nil
}

// MarshalBinary marshals the binary header.
//
//nolint:gocognit // encodes the header field by field, in the order the xz format fixes
func (h *blockHeader) MarshalBinary() (data []byte, err error) {
	if !(minFilters <= len(h.filters) && len(h.filters) <= maxFilters) {
		return nil, errFilterCount
	}

	for i, f := range h.filters {
		if i < len(h.filters)-1 {
			if f.id() == lzmaFilterID {
				return nil, errLZMA2NotLast
			}
		} else {
			// last filter
			if f.id() != lzmaFilterID {
				return nil, errLastNotLZMA2
			}
		}
	}

	var buf bytes.Buffer
	// header size must set at the end
	buf.WriteByte(0)

	// flags
	// #nosec G115 -- the filter count is checked to be one to four
	flags := byte(len(h.filters) - 1)
	if h.compressedSize >= 0 {
		flags |= compressedSizePresent
	}

	if h.uncompressedSize >= 0 {
		flags |= uncompressedSizePresent
	}

	buf.WriteByte(flags)

	p := make([]byte, binary.MaxVarintLen64)
	if h.compressedSize >= 0 {
		k := putUvarint(p, uint64(h.compressedSize))
		buf.Write(p[:k])
	}

	if h.uncompressedSize >= 0 {
		k := putUvarint(p, uint64(h.uncompressedSize))
		buf.Write(p[:k])
	}

	for _, f := range h.filters {
		fp, err := f.MarshalBinary()
		if err != nil {
			return nil, err
		}

		buf.Write(fp)
	}

	// padding
	for i := padLen(int64(buf.Len())); i > 0; i-- {
		buf.WriteByte(0)
	}

	// crc place holder
	buf.Write(p[:4])

	data = buf.Bytes()
	if len(data)%4 != 0 {
		panic("data length not aligned")
	}

	s := len(data)/4 - 1
	if !(1 < s && s <= 255) {
		panic("wrong block header size")
	}

	data[0] = byte(s)

	crc := crc32.NewIEEE()
	crc.Write(data[:len(data)-4])
	binary.LittleEndian.PutUint32(data[len(data)-4:], crc.Sum32())

	return data, nil
}

// Constants used for marshalling and unmarshalling filters in the xz
// block header.
const (
	minFilters    = 1
	maxFilters    = 4
	minReservedID = 1 << 62
)

// filter represents a filter in the block header.
//
//nolint:interfacebloat // a filter is encoded in the header (id, un/marshal), built for each direction, and ordered (last)
type filter interface {
	id() uint64
	UnmarshalBinary(data []byte) error
	MarshalBinary() (data []byte, err error)
	reader(r io.Reader, c *ReaderConfig, cache *lzma2Cache) (fr io.Reader, err error)
	writeCloser(w io.WriteCloser, c *WriterConfig) (fw io.WriteCloser, err error)
	// filter must be last filter
	last() bool
}

// readFilter reads a block filter from the block header. At this point
// in time only the LZMA2 filter is supported.
//
//nolint:iface // a factory keyed on the filter ID, where the format's other filters (delta, BCJ) would go
func readFilter(r io.Reader) (f filter, err error) {
	br := lzma.ByteReader(r)

	// index
	id, _, err := readUvarint(br)
	if err != nil {
		return nil, err
	}

	var data []byte

	switch id {
	case lzmaFilterID:
		data = make([]byte, lzmaFilterLen)

		data[0] = lzmaFilterID
		if _, err = io.ReadFull(r, data[1:]); err != nil {
			return nil, err
		}

		f = new(lzmaFilter)
	default:
		if id >= minReservedID {
			return nil, corruptf(
				"xz: reserved filter id in block stream header",
			)
		}

		return nil, unsupportedf("xz: invalid filter id")
	}

	if err = f.UnmarshalBinary(data); err != nil {
		return nil, err
	}

	return f, err
}

// readFilters reads count filters. At this point in time only the count
// 1 is supported.
func readFilters(r io.Reader, count int) (filters []filter, err error) {
	if count != 1 {
		return nil, unsupportedf("xz: unsupported filter count")
	}

	f, err := readFilter(r)
	if err != nil {
		return nil, err
	}

	return []filter{f}, err
}

// The index.

// record describes a block in the xz file index.
type record struct {
	unpaddedSize     int64
	uncompressedSize int64
}

// readRecord reads an index record.
func readRecord(r io.ByteReader) (rec record, n int, err error) {
	u, k, err := readUvarint(r)

	n += k
	if err != nil {
		return rec, n, err
	}

	// #nosec G115 -- a value past 1<<63 wraps negative, which the next line rejects as corrupt
	rec.unpaddedSize = int64(u)
	if rec.unpaddedSize < 0 {
		return rec, n, corruptf("xz: unpadded size negative")
	}

	u, k, err = readUvarint(r)

	n += k
	if err != nil {
		return rec, n, err
	}

	// #nosec G115 -- a value past 1<<63 wraps negative, which the next line rejects as corrupt
	rec.uncompressedSize = int64(u)
	if rec.uncompressedSize < 0 {
		return rec, n, corruptf("xz: uncompressed size negative")
	}

	return rec, n, nil
}

// MarshalBinary converts an index record in its binary encoding.
func (rec *record) MarshalBinary() (data []byte, err error) {
	// maximum length of a uvarint is 10
	p := make([]byte, 20)
	// #nosec G115 -- record sizes are never negative: checked where they are read and built
	n := putUvarint(p, uint64(rec.unpaddedSize))
	// #nosec G115 -- record sizes are never negative: checked where they are read and built
	n += putUvarint(p[n:], uint64(rec.uncompressedSize))

	return p[:n], nil
}

// writeIndex writes the index, a sequence of records.
func writeIndex(w io.Writer, index []record) (n int64, err error) {
	crc := crc32.NewIEEE()
	mw := io.MultiWriter(w, crc)

	// index indicator
	k, err := mw.Write([]byte{0})

	n += int64(k)
	if err != nil {
		return n, err
	}

	// number of records
	p := make([]byte, binary.MaxVarintLen64)
	k = putUvarint(p, uint64(len(index)))
	k, err = mw.Write(p[:k])

	n += int64(k)
	if err != nil {
		return n, err
	}

	// list of records
	for _, rec := range index {
		var encoded []byte

		encoded, err = rec.MarshalBinary()
		if err != nil {
			return n, err
		}

		k, err = mw.Write(encoded)

		n += int64(k)
		if err != nil {
			return n, err
		}
	}

	// index padding
	k, err = mw.Write(make([]byte, padLen(n)))

	n += int64(k)
	if err != nil {
		return n, err
	}

	// crc32 checksum
	binary.LittleEndian.PutUint32(p, crc.Sum32())
	k, err = w.Write(p[:4])
	n += int64(k)

	return n, err
}

// minBlockSize is the smallest number of bytes a block can occupy in a
// stream: an eight-byte header (the smallest encodable size), the one-byte
// LZMA2 end-of-stream chunk of an empty block, and padding to a multiple of
// four. Any check adds to that.
const minBlockSize = 12

// readIndexBody reads the index from the reader. It assumes that the
// index indicator has already been read. A negative expectedRecordLen
// disables the record-count check (used when the index is parsed before
// the blocks, as the parallel reader does); the records are then read
// incrementally, so a hostile count is bounded by the index itself rather
// than by that check, and a non-negative maxRecords rejects a count that
// more blocks than the stream has room for would need before any record is
// read.
func readIndexBody(r io.Reader, expectedRecordLen, maxRecords int) (records []record, n int64, err error) {
	crc := crc32.NewIEEE()
	// index indicator
	crc.Write([]byte{0})

	br := lzma.ByteReader(io.TeeReader(r, crc))

	// number of records
	u, k, err := readUvarint(br)

	n += int64(k)
	if err != nil {
		return nil, n, err
	}

	// #nosec G115 -- the next line rejects a count that does not survive the conversion
	recLen := int(u)
	if recLen < 0 || uint64(recLen) != u {
		return nil, n, corruptf("xz: record number overflow")
	}

	if expectedRecordLen >= 0 && recLen != expectedRecordLen {
		return nil, n, corruptf(
			"xz: index length is %d; want %d",
			recLen, expectedRecordLen,
		)
	}

	if maxRecords >= 0 && recLen > maxRecords {
		return nil, n, corruptf(
			"xz: index declares %d records but the stream has room for at most %d blocks",
			recLen, maxRecords,
		)
	}

	// List of records. The count is attacker controlled and the parallel
	// reader cannot cross-check it against blocks it has not read yet, so the
	// slice grows with the records that actually arrive instead of being sized
	// from the declared count. A hostile count simply runs into the end of the
	// index; it never reaches an allocator.
	initialCap := min(recLen, initialIndexCap)
	records = make([]record, 0, initialCap)

	for range recLen {
		var rec record

		rec, k, err = readRecord(br)

		n += int64(k)
		if err != nil {
			return nil, n, err
		}

		records = append(records, rec)
	}

	p := make([]byte, padLen(n+1), 4)
	k, err = io.ReadFull(br.(io.Reader), p)

	n += int64(k)
	if err != nil {
		return nil, n, err
	}

	if !allZeros(p) {
		return nil, n, corruptf("xz: non-zero byte in index padding")
	}

	// crc32
	s := crc.Sum32()
	p = p[:4]
	k, err = io.ReadFull(br.(io.Reader), p)

	n += int64(k)
	if err != nil {
		return records, n, err
	}

	if binary.LittleEndian.Uint32(p) != s {
		return nil, n, corruptf("xz: wrong checksum for index")
	}

	return records, n, nil
}

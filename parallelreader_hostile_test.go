// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"hash/crc32"
	"io"
	"testing"
)

// The parallel reader parses the stream index before it decodes anything, so
// every number in the index is attacker controlled and reaches an allocation
// or a loop bound. These helpers build syntactically valid xz files — correct
// magic, correct header, index and footer CRCs — whose index contents are
// hostile.

func uvarintBytes(x uint64) []byte {
	var p []byte
	for x >= 0x80 {
		p = append(p, byte(x)|0x80)
		x >>= 7
	}
	return append(p, byte(x))
}

func le32Bytes(x uint32) []byte {
	p := make([]byte, 4)
	putUint32LE(p, x)
	return p
}

// hostileRecord is one index record, written verbatim without validation.
type hostileRecord struct {
	unpaddedSize     uint64
	uncompressedSize uint64
}

// hostileStream builds one xz stream. blockArea is the raw bytes placed
// between the stream header and the index; recCount overrides the record count
// actually written into the index when it is non-negative, so that the count
// and the number of records that follow it can disagree.
func hostileStream(blockArea []byte, recs []hostileRecord, recCount int64) []byte {
	var out bytes.Buffer

	header := []byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0x00, 0x01}
	out.Write(header)
	out.Write(le32Bytes(crc32.ChecksumIEEE(header[6:8])))

	out.Write(blockArea)

	var idx bytes.Buffer
	idx.WriteByte(0) // index indicator
	n := int64(len(recs))
	if recCount >= 0 {
		n = recCount
	}
	idx.Write(uvarintBytes(uint64(n)))
	for _, rec := range recs {
		idx.Write(uvarintBytes(rec.unpaddedSize))
		idx.Write(uvarintBytes(rec.uncompressedSize))
	}
	for idx.Len()%4 != 0 {
		idx.WriteByte(0)
	}
	indexSize := int64(idx.Len()) + 4
	idx.Write(le32Bytes(crc32.ChecksumIEEE(idx.Bytes())))
	out.Write(idx.Bytes())

	footer := make([]byte, footerLen)
	putUint32LE(footer[4:], uint32(indexSize/4-1))
	footer[9] = 0x01 // CRC32 check, matching the stream header flags
	copy(footer[10:], footerMagic)
	putUint32LE(footer, crc32.ChecksumIEEE(footer[4:10]))
	out.Write(footer)

	return out.Bytes()
}

// readAllParallel runs a full parse-and-decode cycle and returns the error.
// Reaching this function at all means no panic escaped a worker goroutine,
// because such a panic aborts the whole test binary.
func readAllParallel(t *testing.T, file []byte, workers int) error {
	t.Helper()
	r, err := ParallelReaderConfig{Workers: workers}.NewParallelReader(
		bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(io.Discard, r)
	return err
}

// TestParallelReaderHostileUncompressedSize covers an index record claiming an
// uncompressed size that no block of that unpadded size could produce. The
// reader allocates the whole block up front from this number, on a worker
// goroutine, so an unvalidated value used to abort the process with
// "makeslice: len out of range" — unrecoverably, since the caller does not own
// that goroutine.
func TestParallelReaderHostileUncompressedSize(t *testing.T) {
	for _, size := range []uint64{1 << 40, 1 << 49, 1 << 62, 1<<63 - 1} {
		file := hostileStream([]byte{0, 0, 0, 0},
			[]hostileRecord{{unpaddedSize: 1, uncompressedSize: size}}, -1)
		if len(file) > 128 {
			t.Fatalf("fixture grew to %d bytes; it must stay tiny to make "+
				"the amplification obvious", len(file))
		}
		err := readAllParallel(t, file, 4)
		if err == nil {
			t.Errorf("uncompressed size %d: no error", size)
			continue
		}
		t.Logf("uncompressed size %d (%d byte file): %s", size, len(file), err)
	}
}

// TestParallelReaderPlausibleSizeStillDecodes guards the bound from becoming so
// tight that it rejects real files: a normally compressed multi-block stream
// must still parse and decode.
func TestParallelReaderPlausibleSizeStillDecodes(t *testing.T) {
	data := parallelTestData(1 << 18)
	xz := compressMultiBlock(t, data, 32<<10)
	testParallelRead(t, xz, data, 4)
}

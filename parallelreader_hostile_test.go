// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"hash/crc32"
	"io"
	"runtime"
	"testing"
	"time"
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

// hostileAllocBudget is what a tiny malformed file may cost us. Real work
// needs orders of magnitude less; the bombs these tests cover asked for
// gigabytes to terabytes.
const hostileAllocBudget = 8 << 20

// readAllParallel runs a full parse-and-decode cycle and returns the error. It
// fails the test if handling the file allocated more than the budget, which is
// the property these fixtures exist to protect: rejecting the file is not
// enough if we blow up the heap on the way to the error.
//
// Reaching this function at all means no panic escaped a worker goroutine,
// because such a panic aborts the whole test binary.
func readAllParallel(t *testing.T, file []byte, workers int) error {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	err := func() error {
		r, err := ParallelReaderConfig{Workers: workers}.NewParallelReader(
			bytes.NewReader(file), int64(len(file)))
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		_, err = io.Copy(io.Discard, r)
		return err
	}()

	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > hostileAllocBudget {
		t.Errorf("a %d byte file allocated %d bytes; budget is %d",
			len(file), used, hostileAllocBudget)
	}
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

// TestParallelReaderHostileRecordCount covers an index that declares far more
// records than it contains. The parallel reader parses the index before the
// blocks and so cannot cross-check the count, which used to leave the declared
// number feeding make([]record, n) directly: a 40-byte file reserved 16 TiB.
func TestParallelReaderHostileRecordCount(t *testing.T) {
	for _, count := range []int64{1 << 20, 1 << 40, 1 << 45, 1<<62 - 1} {
		file := hostileStream([]byte{0, 0, 0, 0},
			[]hostileRecord{{unpaddedSize: 1, uncompressedSize: 1}}, count)
		if len(file) > 128 {
			t.Fatalf("fixture grew to %d bytes; it must stay tiny to make "+
				"the amplification obvious", len(file))
		}
		err := readAllParallel(t, file, 4)
		if err == nil {
			t.Errorf("record count %d: no error", count)
			continue
		}
		t.Logf("record count %d (%d byte file): %s", count, len(file), err)
	}
}

// TestParallelReaderIndexSizeOverflow covers index records whose padded sizes
// sum past MaxInt64. The sum feeds the position of the stream header, so
// wrapping it negative places the header at or after the footer the walk
// started from and the backwards walk stops making progress.
func TestParallelReaderIndexSizeOverflow(t *testing.T) {
	file := hostileStream([]byte{0, 0, 0, 0}, []hostileRecord{
		{unpaddedSize: 1<<63 - 1, uncompressedSize: 0},
		{unpaddedSize: 1<<63 - 52, uncompressedSize: 0},
	}, -1)
	err := readAllParallel(t, file, 4)
	if err == nil {
		t.Fatal("overflowing index sizes accepted")
	}
	t.Logf("%d byte file: %s", len(file), err)
}

// loopFile is the 104-byte file that used to spin parseBlocks at 100% CPU
// forever. Two streams, all CRCs valid; the second index sums two unpadded
// sizes of 2^63-1 so the total wraps to 0 and the stream header lands exactly
// on the position the walk came from.
var loopFile = []byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x02, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x01,
	0xcc, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x01, 0x00, 0x00,
	0x5d, 0x9e, 0xd6, 0xaf, 0x28, 0x72, 0x9c, 0x10, 0x06, 0x00, 0x00, 0x00,
	0x00, 0x01, 0x59, 0x5a, 0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00, 0x00, 0x01,
	0x69, 0x22, 0xde, 0x36, 0x00, 0x02, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0x7f, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0x7f, 0x01, 0x00, 0x00, 0xa9, 0x7a, 0x61, 0xcc, 0x28, 0x72, 0x9c, 0x10,
	0x06, 0x00, 0x00, 0x00, 0x00, 0x01, 0x59, 0x5a,
}

// TestParallelReaderNoHangOnLoopFile pins the non-termination bug directly.
// The construction is fiddly enough that it is worth keeping the exact bytes
// rather than re-deriving them.
func TestParallelReaderNoHangOnLoopFile(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := NewParallelReader(bytes.NewReader(loopFile), int64(len(loopFile)))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hostile two-stream file accepted")
		}
		t.Logf("%d byte file: %s", len(loopFile), err)
	case <-time.After(10 * time.Second):
		// The goroutine is stuck in an uninterruptible loop; it will keep
		// burning a core until the test binary exits.
		t.Fatal("parseBlocks did not return: the backwards walk is not terminating")
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

// TestParallelReaderHostileSizeWithinBound covers an index record whose
// uncompressed size passes the plausibility bound but is still enormously
// larger than anything the block delivers. checkUncompressedSize admits about
// 350,000 times the unpadded size, so an 8 KiB block may declare gigabytes;
// the decode buffer used to be sized from that declaration before anything
// validated it, ~38,000-fold amplification from one small file.
func TestParallelReaderHostileSizeWithinBound(t *testing.T) {
	blockArea := make([]byte, 8192)
	file := hostileStream(blockArea,
		[]hostileRecord{{unpaddedSize: 8192, uncompressedSize: 300 << 20}}, -1)
	err := readAllParallel(t, file, 4)
	if err == nil {
		t.Fatal("index claiming 300 MiB for an 8 KiB block: no error")
	}
	t.Logf("%d byte file: %s", len(file), err)
}

// TestParallelReaderHostileSizeRealBlock is the same attack mounted on a real
// block, so the decode actually runs: the block decodes fine but delivers far
// less than the index declares. The buffer must grow with the decoded data
// rather than with the declaration, and the mismatch must surface as an error.
func TestParallelReaderHostileSizeRealBlock(t *testing.T) {
	data := parallelTestData(32 << 10)
	var out bytes.Buffer
	// CRC32, matching the stream flags hostileStream writes.
	w, err := WriterConfig{CheckSum: CRC32}.NewWriter(&out)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatalf("Write error %s", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("Close error %s", err)
	}
	genuine := out.Bytes() // single block
	blocks, _, err := parseBlocks(bytes.NewReader(genuine), int64(len(genuine)))
	if err != nil {
		t.Fatalf("parseBlocks error %s", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks; want 1", len(blocks))
	}
	bd := blocks[0]
	blockArea := genuine[bd.offset : bd.offset+bd.paddedSize()]
	file := hostileStream(blockArea,
		[]hostileRecord{{
			unpaddedSize:     uint64(bd.unpaddedSize),
			uncompressedSize: 300 << 20,
		}}, -1)
	err = readAllParallel(t, file, 4)
	if err == nil {
		t.Fatal("index claiming 300 MiB for a 32 KiB block: no error")
	}
	t.Logf("%d byte file: %s", len(file), err)
}

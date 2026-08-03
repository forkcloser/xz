// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bufio"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"runtime"
	"slices"
	"sync"
)

// ParallelReaderConfig defines the parameters for the parallel xz
// reader. Workers is the number of blocks decoded concurrently; values
// below 1 select runtime.GOMAXPROCS(0).
type ParallelReaderConfig struct {
	DictCap int
	Workers int
}

// Verify checks the configuration for errors and replaces zero values with
// their defaults, so afterwards DictCap and Workers both hold the values that
// will actually be used.
func (c *ParallelReaderConfig) Verify() error {
	if c == nil {
		return errors.New("xz: parallel reader parameters are nil")
	}
	rc := ReaderConfig{DictCap: c.DictCap}
	if err := rc.Verify(); err != nil {
		return err
	}
	c.DictCap = rc.DictCap
	if c.Workers < 1 {
		c.Workers = runtime.GOMAXPROCS(0)
	}
	return nil
}

// blockDesc describes the location of a single block inside the xz
// file, as derived from the stream indexes.
type blockDesc struct {
	// file offset of the block header
	offset int64
	// size of header, compressed data and check value, without padding
	unpaddedSize int64
	// size of the uncompressed block data
	uncompressedSize int64
	// constructor for the check of the containing stream
	newHash func() hash.Hash
}

// paddedSize returns the total size of the block in the file.
func (d *blockDesc) paddedSize() int64 {
	return d.unpaddedSize + int64(padLen(d.unpaddedSize))
}

// maxLZMA2Expansion bounds how far one byte of a block can expand. The
// smallest LZMA2 chunk that carries compressed data is six header bytes plus
// at least one data byte, and a chunk decodes to at most 2 MiB.
const maxLZMA2Expansion = (1 << 21) / 6

// checkUncompressedSize rejects index records whose uncompressed size cannot
// possibly be produced by a block of the recorded unpadded size. The parallel
// reader allocates a whole block up front from this number, so a record
// claiming a huge size would otherwise turn a few bytes of input into an
// allocation large enough to abort the process — and the allocation happens on
// a worker goroutine, where the caller cannot recover from it.
func checkUncompressedSize(rec record) error {
	if rec.uncompressedSize > math.MaxInt {
		return corruptf(
			"xz: uncompressed size %d in index exceeds the address space",
			rec.uncompressedSize)
	}
	limit := int64(math.MaxInt64)
	if rec.unpaddedSize <= math.MaxInt64/maxLZMA2Expansion {
		limit = rec.unpaddedSize * maxLZMA2Expansion
	}
	if rec.uncompressedSize > limit {
		return corruptf(
			"xz: uncompressed size %d in index exceeds the maximum %d "+
				"for a block of %d bytes",
			rec.uncompressedSize, limit, rec.unpaddedSize)
	}
	return nil
}

// ParallelReader decodes the blocks of an xz file concurrently. It
// requires random access to the input (io.ReaderAt) and its total size,
// because the block locations are read from the stream indexes at the
// end of each stream before any data is decoded. The decoded stream is
// presented in order through the io.Reader (or io.WriterTo) interface.
//
// Only files consisting of multiple blocks — as produced for example by
// xz with a block size limit or in multi-threaded mode, or by this
// package's writer with WriterConfig.BlockSize — decode with real
// concurrency; a single-block file decodes on one worker. Memory usage
// is proportional to Workers times the uncompressed block size.
//
// The ParallelReader verifies the block checks, the block sizes against
// the index, and the header, footer and index checksums of every
// stream.
//
// Read and WriteTo must be called from one goroutine at a time. Close is the
// exception: it may be called from another goroutine to cancel a Read that is
// waiting on a block, which is what makes it usable for abandoning a reader
// whose input has gone slow.
type ParallelReader struct {
	ParallelReaderConfig

	// dec holds everything the dispatcher and the workers touch. Those
	// goroutines must not reference the ParallelReader itself: the cleanup
	// that stops them when a reader is abandoned without Close can only run
	// once the reader is unreachable, which it never becomes while a
	// goroutine stack still roots it.
	dec  *parallelDecoder
	size int64

	// Owned by the reading goroutine.
	started bool
	cur     []byte
	curPos  int

	// err is reachable from Close, so it is the one field that needs a lock.
	mu  sync.Mutex
	err error
}

// parallelDecoder is the part of the parallel reader that the dispatcher
// and the decode workers operate on.
type parallelDecoder struct {
	dictCap int

	xz     io.ReaderAt
	blocks []blockDesc

	// done is closed by Close, and by the first read error, to tell the
	// dispatcher and any waiting read to give up. It is created with the
	// reader rather than by start, so that Close cannot race with a first
	// Read that has not started the workers yet.
	done      chan struct{}
	closeOnce sync.Once

	queue   chan *blockWork
	jobs    chan *blockWork
	bufPool chan []byte
}

// setErr records the first error the reader saw and returns the error it will
// report from here on. Later errors do not displace the first, so the reason a
// stream stopped stays stable across calls.
func (r *ParallelReader) setErr(err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
	return r.err
}

// getErr returns the error the reader is in, or nil.
func (r *ParallelReader) getErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// blockResult is the outcome of decoding one block.
type blockResult struct {
	data []byte
	err  error
}

// blockWork is a unit of work handed to a decode worker. The result
// channel has capacity one, so a worker never blocks delivering.
type blockWork struct {
	d      blockDesc
	result chan blockResult
}

// NewParallelReader creates a reader that decodes the blocks of an xz
// file concurrently using the default parameters. See ParallelReader
// for the conditions under which this actually parallelizes.
func NewParallelReader(xz io.ReaderAt, size int64) (r *ParallelReader, err error) {
	return ParallelReaderConfig{}.NewParallelReader(xz, size)
}

// NewParallelReader creates a new parallel reader using the given
// configuration. It reads and verifies the stream headers, footers and
// indexes, but does not decode any block data yet.
func (c ParallelReaderConfig) NewParallelReader(xz io.ReaderAt, size int64) (r *ParallelReader, err error) {
	if err = c.Verify(); err != nil {
		return nil, err
	}
	blocks, total, err := parseBlocks(xz, size)
	if err != nil {
		return nil, err
	}
	r = &ParallelReader{
		ParallelReaderConfig: c,
		dec: &parallelDecoder{
			xz:     xz,
			blocks: blocks,
			done:   make(chan struct{}),
		},
		size: total,
	}
	// Workers and the dispatcher are goroutines, and goroutines are not
	// collected just because nothing references the reader that started them.
	// They reference only the decoder, so an abandoned reader does become
	// unreachable, and this cleanup then cancels them. Close is still the
	// documented way to release a reader early; this only keeps forgetting it
	// from leaking for the life of the process.
	runtime.AddCleanup(r, func(d *parallelDecoder) { d.stop() }, r.dec)
	return r, nil
}

// Size returns the total number of uncompressed bytes in the file, as
// recorded in the stream indexes.
func (r *ParallelReader) Size() int64 { return r.size }

// paddingScanBufSize is how much trailing padding is examined per read. Stream
// padding comes in groups of four zero bytes and can be arbitrarily long, so
// reading it four bytes at a time turns a large zero-padded file into millions
// of reads — one syscall each when the input is a file.
const paddingScanBufSize = 32 << 10

// skipStreamPadding walks backwards from pos over whole four-byte groups of
// zeros and returns the position of the first one, which is where the stream
// before the padding ends. A return of zero means everything up to pos was
// padding. pos must be a multiple of four.
func skipStreamPadding(xz io.ReaderAt, pos int64) (int64, error) {
	buf := make([]byte, paddingScanBufSize)
	for pos >= 4 {
		n := min(int64(len(buf)), pos)
		n -= n % 4 // only whole groups, so the scan stays aligned
		p := buf[:n]
		if _, err := xz.ReadAt(p, pos-n); err != nil {
			return 0, err
		}
		i := len(p)
		for i >= 4 && allZeros(p[i-4:i]) {
			i -= 4
		}
		pos -= int64(len(p) - i)
		if i != 0 {
			return pos, nil
		}
	}
	return pos, nil
}

// parseBlocks locates all blocks of the xz file by walking the streams
// backwards from the end of the file: footer, index, stream header.
// The header, footer and index checksums of every stream are verified.
func parseBlocks(xz io.ReaderAt, size int64) (blocks []blockDesc, total int64, err error) {
	streams := make([][]blockDesc, 0, 1)
	pos := size
	for pos > 0 {
		if pos%4 != 0 {
			return nil, 0, corruptf("xz: file size not a multiple of four bytes")
		}
		if pos, err = skipStreamPadding(xz, pos); err != nil {
			return nil, 0, err
		}
		if pos == 0 {
			break
		}

		// footer
		if pos < HeaderLen+footerLen {
			return nil, 0, corruptf("xz: stream truncated")
		}
		fdata := make([]byte, footerLen)
		if _, err = xz.ReadAt(fdata, pos-footerLen); err != nil {
			return nil, 0, err
		}
		var f footer
		if err = f.UnmarshalBinary(fdata); err != nil {
			return nil, 0, err
		}

		// index
		indexStart := pos - footerLen - f.indexSize
		if indexStart < HeaderLen {
			return nil, 0, corruptf("xz: index size exceeds stream")
		}
		ir := bufio.NewReader(io.NewSectionReader(xz, indexStart, f.indexSize))
		c, err := ir.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		if c != 0 {
			return nil, 0, corruptf("xz: index indicator missing")
		}
		records, n, err := readIndexBody(ir, -1)
		if err != nil {
			return nil, 0, err
		}
		if n+1 != f.indexSize {
			return nil, 0, corruptf("xz: index size does not match footer")
		}

		// stream header
		//
		// The blocks have to fit between the stream header and the index, so
		// every partial sum is bounded by indexStart. Accumulating without
		// that bound lets a hostile index wrap blocksLen negative, which puts
		// headerPos at or above pos and makes the enclosing loop re-parse the
		// same footer forever.
		var blocksLen int64
		for _, rec := range records {
			if rec.unpaddedSize <= 0 {
				return nil, 0, corruptf("xz: invalid unpadded size in index")
			}
			if err := checkUncompressedSize(rec); err != nil {
				return nil, 0, err
			}
			// remaining is in [0, indexStart], so neither comparison can
			// overflow, and the first one keeps the addition in range.
			remaining := indexStart - blocksLen
			if rec.unpaddedSize > remaining {
				return nil, 0, corruptf("xz: blocks exceed stream size")
			}
			padded := rec.unpaddedSize + int64(padLen(rec.unpaddedSize))
			if padded > remaining {
				return nil, 0, errors.New("xz: blocks exceed stream size")
			}
			blocksLen += padded
		}
		headerPos := indexStart - blocksLen - HeaderLen
		if headerPos < 0 {
			return nil, 0, errors.New("xz: blocks exceed stream size")
		}
		hdata := make([]byte, HeaderLen)
		if _, err = xz.ReadAt(hdata, headerPos); err != nil {
			return nil, 0, err
		}
		var h header
		if err = h.UnmarshalBinary(hdata); err != nil {
			return nil, 0, err
		}
		if h.flags != f.flags {
			return nil, 0, corruptf("xz: stream header and footer flags differ")
		}
		newHash, err := newHashFunc(h.flags)
		if err != nil {
			return nil, 0, err
		}

		descs := make([]blockDesc, len(records))
		off := headerPos + HeaderLen
		for i, rec := range records {
			descs[i] = blockDesc{
				offset:           off,
				unpaddedSize:     rec.unpaddedSize,
				uncompressedSize: rec.uncompressedSize,
				newHash:          newHash,
			}
			off += descs[i].paddedSize()
		}
		streams = append(streams, descs)
		// The walk is backwards, so pos must strictly decrease for the loop to
		// terminate. That follows from the bounds above, but state it here so
		// termination is checkable locally and survives future edits.
		if headerPos >= pos {
			return nil, 0, corruptf("xz: stream does not precede its index")
		}
		pos = headerPos
	}
	if len(streams) == 0 {
		return nil, 0, corruptf("xz: no streams found")
	}
	// streams were found back to front
	for _, stream := range slices.Backward(streams) {
		for _, d := range stream {
			// Each size is already bounded against its own block, but the
			// total is what Size reports, and callers size buffers from it.
			// Wrapping it would hand them a small or negative number for an
			// enormous stream.
			if d.uncompressedSize > math.MaxInt64-total {
				return nil, 0, corruptf(
					"xz: total uncompressed size overflows int64")
			}
			total += d.uncompressedSize
			blocks = append(blocks, d)
		}
	}
	return blocks, total, nil
}

// errReaderClosed is returned by Read after Close has been called. It matches
// ErrClosed so callers can recognise it without comparing message text.
var errReaderClosed = &kindError{
	msg:  "xz: parallel reader is closed",
	kind: ErrClosed,
}

// start launches the dispatcher and the decode workers.
func (r *ParallelReader) start() {
	r.started = true
	// Workers and DictCap are promoted fields, so they are writable between
	// construction and the first read. Re-apply the floor here: zero workers
	// would leave every read waiting for a block that nothing is decoding.
	if r.Workers < 1 {
		r.Workers = runtime.GOMAXPROCS(0)
	}
	r.dec.start(r.Workers, r.DictCap)
}

// start launches the dispatcher and the decode workers. The queue
// capacity bounds the number of blocks in flight (decoding or decoded
// but not yet consumed) and thereby the memory use.
func (d *parallelDecoder) start(workers, dictCap int) {
	d.dictCap = dictCap
	d.queue = make(chan *blockWork, workers+2)
	d.jobs = make(chan *blockWork)
	d.bufPool = make(chan []byte, cap(d.queue)+1)
	for i := 0; i < workers; i++ {
		go d.worker()
	}
	go d.dispatch()
}

// dispatch feeds the blocks to the workers in file order. Admission to
// the ordered queue (bounded capacity) throttles how many blocks are in
// flight.
func (d *parallelDecoder) dispatch() {
	// Both channels close on every exit path, including the cancelled one:
	// jobs so the workers finish, and queue so a consumer waiting for the
	// next block is released rather than left waiting on work that will
	// never be dispatched.
	defer close(d.queue)
	defer close(d.jobs)
	for i := range d.blocks {
		w := &blockWork{
			d:      d.blocks[i],
			result: make(chan blockResult, 1),
		}
		select {
		case d.queue <- w:
		case <-d.done:
			return
		}
		select {
		case d.jobs <- w:
		case <-d.done:
			return
		}
	}
}

// worker decodes blocks until the job channel is closed.
func (d *parallelDecoder) worker() {
	for w := range d.jobs {
		w.result <- d.decodeOne(&w.d)
	}
}

// decodeOne decodes a single block and reports the outcome. A panic raised
// while decoding attacker-controlled data is converted into an error: the
// worker runs on a goroutine the caller does not own, so an escaping panic
// would abort the process with no way for the caller to recover.
func (d *parallelDecoder) decodeOne(bd *blockDesc) (res blockResult) {
	var buf []byte
	defer func() {
		if v := recover(); v != nil {
			d.putBuf(buf)
			res = blockResult{err: fmt.Errorf(
				"xz: panic while decoding block at offset %d: %v",
				bd.offset, v)}
		}
	}()
	buf = d.getBuf(int(bd.uncompressedSize))
	data, err := d.decodeBlock(bd, buf)
	if err != nil {
		d.putBuf(buf)
		return blockResult{err: err}
	}
	return blockResult{data: data}
}

// getBuf returns a decode buffer of length n, reusing a pooled buffer
// if one of sufficient capacity is available.
func (d *parallelDecoder) getBuf(n int) []byte {
	select {
	case b := <-d.bufPool:
		if cap(b) >= n {
			return b[:n]
		}
	default:
	}
	return make([]byte, n)
}

// putBuf returns a buffer to the pool, dropping it if the pool is full.
func (d *parallelDecoder) putBuf(b []byte) {
	if b == nil {
		return
	}
	select {
	case d.bufPool <- b:
	default:
	}
}

// blockReadBufSize is the size of the buffered reader each worker
// places over its section of the file to batch the small reads of the
// block and chunk headers.
const blockReadBufSize = 256 << 10

// decodeBlock decodes a single block into buf, which must have the
// uncompressed size of the block recorded in the index. It verifies the
// block check and that header, compressed size and uncompressed size
// agree with the index record.
func (d *parallelDecoder) decodeBlock(bd *blockDesc, buf []byte) ([]byte, error) {
	sr := io.NewSectionReader(d.xz, bd.offset, bd.paddedSize())
	xr := bufio.NewReaderSize(sr, blockReadBufSize)

	h, hlen, err := readBlockHeader(xr)
	if err != nil {
		return nil, err
	}
	c := ReaderConfig{DictCap: d.dictCap}
	br, err := c.newBlockReader(xr, h, hlen, bd.newHash())
	if err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(br, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	// The block must end exactly here; the final Read triggers the
	// padding and check verification in the block reader.
	var tmp [1]byte
	n, err := br.Read(tmp[:])
	if n != 0 || err == nil {
		return nil, corruptf("xz: block longer than index record")
	}
	if !errors.Is(err, io.EOF) {
		return nil, err
	}
	if br.record() != (record{bd.unpaddedSize, bd.uncompressedSize}) {
		return nil, corruptf("xz: block sizes do not match index record")
	}
	return buf, nil
}

// nextBlock retires the current buffer and blocks until the next
// decoded block is available. It returns io.EOF after the last block.
// It gives up if the reader is cancelled: a cancelled dispatcher can leave a
// block queued that no worker will ever report on, so both receives have to
// watch done rather than only the queue.
func (r *ParallelReader) nextBlock() error {
	r.dec.putBuf(r.cur)
	r.cur = nil
	r.curPos = 0
	var w *blockWork
	select {
	case queued, ok := <-r.dec.queue:
		if !ok {
			return io.EOF
		}
		w = queued
	case <-r.dec.done:
		return errReaderClosed
	}
	select {
	case res := <-w.result:
		if res.err != nil {
			return res.err
		}
		r.cur = res.data
		return nil
	case <-r.dec.done:
		return errReaderClosed
	}
}

// Read reads the uncompressed data stream. The blocks are decoded
// concurrently but delivered in order.
func (r *ParallelReader) Read(p []byte) (n int, err error) {
	if err = r.getErr(); err != nil {
		return 0, err
	}
	if !r.started {
		r.start()
	}
	for n < len(p) {
		if r.curPos == len(r.cur) {
			if err = r.nextBlock(); err != nil {
				if !errors.Is(err, io.EOF) {
					r.dec.stop()
				}
				return n, r.setErr(err)
			}
			continue
		}
		k := copy(p[n:], r.cur[r.curPos:])
		n += k
		r.curPos += k
	}
	return n, nil
}

// WriteTo writes the whole remaining uncompressed data stream to w. It
// avoids the intermediate copy of the Read interface by handing the
// decoded block buffers directly to the writer.
func (r *ParallelReader) WriteTo(w io.Writer) (n int64, err error) {
	// An exhausted reader has nothing left to write, which is success, not
	// failure: io.Copy does not report io.EOF for an ordinary reader, and a
	// caller checking err would otherwise see a fully drained stream as an
	// error.
	if err = r.getErr(); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		return 0, err
	}
	if !r.started {
		r.start()
	}
	for {
		if r.curPos == len(r.cur) {
			if err = r.nextBlock(); err != nil {
				if errors.Is(err, io.EOF) {
					// Record EOF so later calls stay consistent, but
					// report success: WriterTo stops at EOF, it does
					// not fail at it.
					_ = r.setErr(err)
					return n, nil
				}
				r.dec.stop()
				return n, r.setErr(err)
			}
			continue
		}
		k, err := w.Write(r.cur[r.curPos:])
		n += int64(k)
		r.curPos += k
		if err != nil {
			r.dec.stop()
			return n, r.setErr(err)
		}
		// A writer that accepts nothing and reports no error is broken.
		// io.Copy turns that into ErrShortWrite rather than spinning, and so
		// must this loop.
		if k == 0 {
			r.dec.stop()
			return n, r.setErr(io.ErrShortWrite)
		}
	}
}

// stop cancels the dispatcher and releases any waiting read. The workers
// finish the block in hand and exit once the dispatcher closes the job
// channel.
func (d *parallelDecoder) stop() {
	d.closeOnce.Do(func() { close(d.done) })
}

// Close stops the background workers. It must be called when the reader is
// abandoned before io.EOF was reached; it is a no-op otherwise. The error is
// always nil.
//
// Unlike Read and WriteTo, Close may be called from another goroutine, and
// doing so cancels a read that is waiting on a block. A reader that already
// reached io.EOF keeps reporting io.EOF rather than the close.
func (r *ParallelReader) Close() error {
	r.dec.stop()
	// Only takes effect if the reader was not already finished or failed.
	_ = r.setErr(errReaderClosed)
	return nil
}

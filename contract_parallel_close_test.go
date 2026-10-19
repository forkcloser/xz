package xz_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/forkcloser/xz"
)

// gatedReaderAt serves the first open bytes of data and its last tail
// bytes (the index and footer a parallel reader starts from) at once, and
// blocks any read into the middle until released.
type gatedReaderAt struct {
	data    []byte
	open    int64
	tail    int64
	release chan struct{}
	once    sync.Once
}

func (g *gatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if end > g.open && off < int64(len(g.data))-g.tail {
		<-g.release
	}

	return bytes.NewReader(g.data).ReadAt(p, off)
}

func (g *gatedReaderAt) Release() { g.once.Do(func() { close(g.release) }) }

func anyCheck() byte {
	for _, c := range checks() {
		return c
	}

	return 0
}

// TestContractParallelReaderCloseFreesAWaitingRead: Close from another
// goroutine, "cancels a read that is waiting on a block": a Read blocked
// on a source that does not answer returns within a bound, with an error
// that is not io.EOF and not nil, and later reads keep an error; the
// source answering afterwards changes nothing. A reader that reached
// io.EOF before Close "keeps reporting io.EOF rather than the close".
func TestContractParallelReaderCloseFreesAWaitingRead(t *testing.T) {
	t.Parallel()

	// Incompressible data, so the blocks are real bytes to wait for: the
	// constructor reads the last 32 KiB and the index, which the tail serves;
	// the blocks in the middle never arrive.
	data := make([]byte, 1<<20)

	rng := rand.New(rand.NewPCG(7, 11))
	for i := range data {
		data[i] = byte(rng.IntN(256))
	}

	file := refStream(refOptions{check: anyCheck(), sizes: true, dictCode: 35}, refBlocks(t, data, 64<<10))
	gate := &gatedReaderAt{data: file, open: int64(len(file) / 2), tail: 96 << 10, release: make(chan struct{})}

	r, err := xz.ParallelReaderConfig{Workers: 2}.NewParallelReader(gate, int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		n   int
		err error
	}

	results := make(chan outcome, 1)

	go func() {
		buf := make([]byte, len(data)+1)

		var total int

		for {
			n, rerr := r.Read(buf[total:])
			total += n

			if rerr != nil || total >= len(buf) {
				results <- outcome{total, rerr}

				return
			}
		}
	}()

	// Give the read time to block on the gate, then close from here.
	time.Sleep(200 * time.Millisecond)

	closeDone := make(chan error, 1)

	go func() { closeDone <- r.Close() }()

	select {
	case got := <-results:
		if got.err == nil || errors.Is(got.err, io.EOF) {
			t.Errorf("the waiting Read returned %d bytes, %v; want an error that is not io.EOF", got.n, got.err)
		}

		if got.n >= len(data) {
			t.Errorf("the waiting Read returned the whole %d bytes through a source that never served them", got.n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not free the waiting Read within 10 s")
	}

	gate.Release()

	select {
	case cerr := <-closeDone:
		if cerr != nil {
			t.Errorf("Close: %v, want nil", cerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return within 10 s")
	}

	if n, rerr := r.Read(make([]byte, 16)); rerr == nil || errors.Is(rerr, io.EOF) {
		t.Errorf("a Read after Close: %d bytes, %v; want an error that is not io.EOF", n, rerr)
	}

	// The whole file through a source that answers: io.EOF, and io.EOF
	// still after Close.
	whole, err := xz.ParallelReaderConfig{Workers: 2}.NewParallelReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}

	got, err := io.ReadAll(whole)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the whole file: %d bytes, %v", len(got), err)
	}

	if n, err := whole.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("after the end: %d, %v; want 0, io.EOF", n, err)
	}

	if err := whole.Close(); err != nil {
		t.Errorf("Close after io.EOF: %v", err)
	}

	if n, err := whole.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("after Close, a reader that reached the end: %d, %v; want 0, io.EOF", n, err)
	}
}

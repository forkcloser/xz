package lzma

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"testing"
)

// plainReader is an io.Reader that is deliberately not an io.ByteReader, and
// counts the bytes pulled from it.
type plainReader struct {
	r io.Reader
	n int
}

func (p *plainReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += n
	return n, err
}

func lzmaEnwik(tb testing.TB, size int) (compressed, plain []byte) {
	tb.Helper()
	plain, err := os.ReadFile("../testdata/enwik7")
	if err != nil {
		tb.Skip("testdata/enwik7 not available")
	}
	if len(plain) > size {
		plain = plain[:size]
	}
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes(), plain
}

// TestReaderInputConsumption pins the documented input contract: an
// io.ByteReader input is consumed exactly to the end of the stream, a plain
// io.Reader is buffered and may be read past it. Both decode identically.
func TestReaderInputConsumption(t *testing.T) {
	comp, plain := lzmaEnwik(t, 200000)
	trailer := []byte("TRAILER-AFTER-THE-STREAM")
	withTrailer := append(append([]byte{}, comp...), trailer...)

	t.Run("byteReaderIsExact", func(t *testing.T) {
		src := bytes.NewReader(withTrailer) // an io.ByteReader
		r, err := NewReader(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatal("round-trip mismatch")
		}
		rest, _ := io.ReadAll(src)
		if !bytes.Equal(rest, trailer) {
			t.Fatalf("ByteReader input over-read: %d trailer bytes left, want %d", len(rest), len(trailer))
		}
	})

	t.Run("plainReaderIsBuffered", func(t *testing.T) {
		src := &plainReader{r: bytes.NewReader(withTrailer)}
		r, err := NewReader(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatal("round-trip mismatch")
		}
		// It must have pulled data in buffered runs, not one byte per Read:
		// the whole 35 KB stream plus trailer in a handful of Reads.
		if src.n < len(comp) {
			t.Fatalf("consumed %d < stream %d", src.n, len(comp))
		}
	})

	t.Run("plainReaderReadCount", func(t *testing.T) {
		// The point of buffering: the number of Read calls on the source
		// must be far below one per compressed byte.
		calls := 0
		r, err := NewReader(readCounter{bytes.NewReader(comp), &calls})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
		if calls > len(comp)/512 {
			t.Fatalf("%d Read calls for a %d byte stream; want buffered reads", calls, len(comp))
		}
	})
}

// readCounter counts Read calls and is not an io.ByteReader.
type readCounter struct {
	r     io.Reader
	calls *int
}

func (c readCounter) Read(b []byte) (int, error) {
	*c.calls++
	return c.r.Read(b)
}

func BenchmarkReaderPlainFile(b *testing.B) {
	comp, plain := lzmaEnwik(b, 4<<20)
	f, err := os.CreateTemp(b.TempDir(), "x.lzma")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := f.Write(comp); err != nil {
		b.Fatal(err)
	}
	_ = f.Close()
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fh, err := os.Open(f.Name())
		if err != nil {
			b.Fatal(err)
		}
		r, err := NewReader(fh) // *os.File: not a ByteReader
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
		_ = fh.Close()
	}
}

func BenchmarkReaderBufioFile(b *testing.B) {
	comp, plain := lzmaEnwik(b, 4<<20)
	f, err := os.CreateTemp(b.TempDir(), "x.lzma")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := f.Write(comp); err != nil {
		b.Fatal(err)
	}
	_ = f.Close()
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fh, err := os.Open(f.Name())
		if err != nil {
			b.Fatal(err)
		}
		r, err := NewReader(bufio.NewReader(fh))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
		_ = fh.Close()
	}
}

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import (
	"bytes"
	"io"
	"math/rand"
	"strings"
	"testing"

	"github.com/forkcloser/xz/internal/randtxt"
)

func TestWriter2(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	// check that double Flush doesn't write another chunk
	if err = w.Flush(); err != nil {
		t.Fatalf("w.Flush() error %s", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	p := buf.Bytes()
	want := []byte{1, 0, 0, 'a', 0}
	if !bytes.Equal(p, want) {
		t.Fatalf("bytes written %#v; want %#v", p, want)
	}
}

func TestCycle1(t *testing.T) {
	var buf bytes.Buffer
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	r, err := Reader2Config{DictCap: 4096}.NewReader2(&buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	p := make([]byte, 3)
	n, err = r.Read(p)
	t.Logf("n %d error %v", n, err)
}

func TestCycle2(t *testing.T) {
	buf := new(bytes.Buffer)
	w, err := Writer2Config{DictCap: 4096}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	// const txtlen = 1024
	const txtlen = 2100000
	_, _ = io.CopyN(buf, randtxt.NewReader(rand.NewSource(42)), txtlen)
	txt := buf.String()
	buf.Reset()
	n, err := io.Copy(w, strings.NewReader(txt))
	if err != nil {
		t.Fatalf("Compressing copy error %s", err)
	}
	if n != txtlen {
		t.Fatalf("Compressing data length %d; want %d", n, txtlen)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close error %s", err)
	}
	t.Logf("buf.Len() %d", buf.Len())
	r, err := Reader2Config{DictCap: 4096}.NewReader2(buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	out := new(bytes.Buffer)
	n, err = io.Copy(out, r)
	if err != nil {
		t.Fatalf("Decompressing copy error %s after %d bytes", err, n)
	}
	if n != txtlen {
		t.Fatalf("Decompression data length %d; want %d", n, txtlen)
	}
	if txt != out.String() {
		t.Fatal("decompressed data differs from original")
	}
}

// TestWriter2SmallDictIncompressible verifies that a writer with a small
// dictionary can encode incompressible data. Such data fills the 64 KiB
// compressed-chunk limit before the chunk is cut, so the uncompressed form of
// the chunk would be smaller — but a dictionary below that size can no longer
// replay the chunk's input, and choosing the uncompressed form made Write and
// Close fail with ErrNoSpace.
func TestWriter2SmallDictIncompressible(t *testing.T) {
	rnd := rand.New(rand.NewSource(13))
	for _, dictCap := range []int{MinDictCap, 8192, 16384, 1 << 16} {
		for _, size := range []int{5000, 10000, 100000} {
			data := make([]byte, size)
			rnd.Read(data)
			var buf bytes.Buffer
			w, err := Writer2Config{DictCap: dictCap}.NewWriter2(&buf)
			if err != nil {
				t.Fatalf("NewWriter2 error %s", err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatalf("dictCap=%d size=%d: Write error %s",
					dictCap, size, err)
			}
			if err = w.Close(); err != nil {
				t.Fatalf("dictCap=%d size=%d: Close error %s",
					dictCap, size, err)
			}
			r, err := NewReader2(&buf)
			if err != nil {
				t.Fatalf("NewReader2 error %s", err)
			}
			out := new(bytes.Buffer)
			if _, err = io.Copy(out, r); err != nil {
				t.Fatalf("dictCap=%d size=%d: decompress error %s",
					dictCap, size, err)
			}
			if !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("dictCap=%d size=%d: decompressed data differs "+
					"from original", dictCap, size)
			}
		}
	}
}

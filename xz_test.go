// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/forkcloser/xz"
)

func TestPanic(t *testing.T) {
	t.Parallel()

	data := []byte{253, 55, 122, 88, 90, 0, 0, 0, 255, 18, 217, 65, 0, 189, 191, 239, 189, 191, 239, 48}
	t.Logf("%q", string(data))
	t.Logf("0x%02x", data)

	r, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Logf("xz.NewReader error %s", err)
		return
	}

	_, err = io.ReadAll(r)
	if err != nil {
		t.Logf("io.ReadAll(r) error %s", err)
		return
	}
}

// stutterReader returns (0, nil) before every real read, which io.Reader
// permits.
type stutterReader struct {
	r    io.Reader
	skip bool
}

func (s *stutterReader) Read(p []byte) (int, error) {
	s.skip = !s.skip
	if s.skip {
		return 0, nil
	}

	return s.r.Read(p)
}

func TestReaderZeroNilReads(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("hello world "), 1000)

	var buf bytes.Buffer

	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = w.Write(payload); err != nil {
		t.Fatal(err)
	}

	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := xz.NewReader(&stutterReader{r: bytes.NewReader(buf.Bytes())})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatal("decoded data differs from the payload")
	}
}

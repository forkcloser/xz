// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bytereader_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/forkcloser/xz/v2/internal/bytereader"
)

// emptyReader returns (0, nil) forever.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

// plainReader is an io.Reader that is not an io.ByteReader.
type plainReader struct{ r io.Reader }

func (p plainReader) Read(b []byte) (int, error) { return p.r.Read(b) }

func TestNoProgress(t *testing.T) {
	t.Parallel()

	_, err := bytereader.New(emptyReader{}).ReadByte()
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("ReadByte on a reader that never progresses: %v, want io.ErrNoProgress", err)
	}
}

func TestReadsOneByteAtATime(t *testing.T) {
	t.Parallel()

	src := bytes.NewReader([]byte("abc"))

	br := bytereader.New(plainReader{src})
	if b, err := br.ReadByte(); err != nil || b != 'a' {
		t.Fatalf("%q, %v", b, err)
	}

	if src.Len() != 2 {
		t.Fatalf("%d bytes left after one ReadByte, want 2", src.Len())
	}

	// An io.ByteReader is used as it is.
	sr := strings.NewReader("x")
	if got := bytereader.New(sr); got != io.ByteReader(sr) {
		t.Fatal("a ByteReader was wrapped")
	}
}

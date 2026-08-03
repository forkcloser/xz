// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"testing"
)

func TestNoneHash(t *testing.T) {
	h := newNoneHash()

	p := []byte("foo")
	q := h.Sum(p)

	if !bytes.Equal(q, p) {
		t.Fatalf("h.Sum: got %q; want %q", q, p)
	}

	// The block reader skips the hash entirely when Size is zero, so the rest
	// of the interface is never exercised in normal use. Check it anyway: the
	// day something starts calling it, it has to behave.
	n, err := h.Write([]byte("payload"))
	if err != nil || n != 7 {
		t.Errorf("h.Write: got (%d, %v); want (7, nil)", n, err)
	}
	h.Reset()
	if got := h.Size(); got != 0 {
		t.Errorf("h.Size: got %d; want 0", got)
	}
	if got := h.BlockSize(); got != 0 {
		t.Errorf("h.BlockSize: got %d; want 0", got)
	}
	if got := h.Sum(nil); len(got) != 0 {
		t.Errorf("h.Sum(nil): got %d bytes; want none", len(got))
	}
}

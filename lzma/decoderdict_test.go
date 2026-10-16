// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma //nolint:testpackage // white-box: tests the unexported decoder dictionary

import (
	"errors"
	"testing"
)

func TestNewDecoderDict(t *testing.T) {
	t.Parallel()

	if _, err := newDecoderDict(0); err == nil {
		t.Fatal("no error for zero dictionary capacity")
	}

	if _, err := newDecoderDict(8); err != nil {
		t.Fatalf("error %s", err)
	}
}

// TestDecoderDictWriteMatchBounds: a match distance reaching exactly one
// byte before the dictionary's start is refused as corrupt, as the last
// byte written is accepted.
func TestDecoderDictWriteMatchBounds(t *testing.T) {
	t.Parallel()

	d, err := newDecoderDict(4096)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = d.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}

	if err = d.writeMatch(3, 2); err != nil {
		t.Fatalf("a distance to the first byte written: %v", err)
	}

	if err = d.writeMatch(int64(d.dictLen())+1, 1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a distance one past the dictionary: %v; want ErrCorrupt", err)
	}
}

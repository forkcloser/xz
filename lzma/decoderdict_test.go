// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma //nolint:testpackage // white-box: tests the unexported decoder dictionary

import (
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

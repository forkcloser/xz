// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dictsize_test

import (
	"errors"
	"testing"

	"github.com/forkcloser/xz/v2/internal/dictsize"
)

// TestCodes: the 41 dictionary codes decode to 2^n and 2^n+2^(n-1) up to
// 2^32-1, encode back to themselves, a value between two codes encodes to
// the next code up, and code 41 is refused.
func TestCodes(t *testing.T) {
	t.Parallel()

	for code := range byte(41) {
		n, err := dictsize.Decode(code)
		if err != nil {
			t.Fatalf("code %d: %v", code, err)
		}

		var want int64

		switch {
		case code == 40:
			want = 1<<32 - 1
		case code%2 == 0:
			want = 1 << (code/2 + 12)
		default:
			want = 3 << (code/2 + 11)
		}

		if n != want {
			t.Errorf("code %d: %d, want %d", code, n, want)
		}

		if back := dictsize.Encode(n); back != code {
			t.Errorf("value %d encodes to code %d; want %d", n, back, code)
		}

		if code > 0 && code < 40 {
			if up := dictsize.Encode(n - 1); up != code {
				t.Errorf("value %d encodes to code %d; want %d (the next code up)", n-1, up, code)
			}
		}
	}

	if _, err := dictsize.Decode(41); !errors.Is(err, dictsize.ErrCode) {
		t.Errorf("code 41: %v, want ErrCode", err)
	}

	if got := dictsize.Encode(1 << 40); got != dictsize.MaxCode {
		t.Errorf("a size past the maximum encodes to %d, want %d", got, dictsize.MaxCode)
	}
}

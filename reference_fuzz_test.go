// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"testing"

	"github.com/forkcloser/xz"
)

// FuzzReference holds both readers to the reference container on arbitrary
// bytes: where the reference parses, the same bytes; where it refuses, a
// refusal matching a sentinel. The seeds are reference files of every
// check type, with and without sizes, one and several blocks and streams.
func FuzzReference(f *testing.F) {
	data := cData(700)

	for _, check := range []byte{xz.None, xz.CRC32, xz.CRC64, xz.SHA256} {
		for _, sizes := range []bool{false, true} {
			o := refOptions{check: check, sizes: sizes, dictCode: 20}
			f.Add(refStream(o, refBlocks(f, data, 0)))
			f.Add(refStream(o, refBlocks(f, data, 300)))
			f.Add(
				refFile(
					4,
					refStream(o, refBlocks(f, data[:200], 0)),
					refStream(o, nil),
					refStream(o, refBlocks(f, data[200:], 0)),
				),
			)
		}
	}

	f.Fuzz(func(t *testing.T, file []byte) {
		if len(file) > 1<<16 {
			return
		}

		ref, err := refParse(file)
		sequential, parallel := both(file)
		classify(t, "sequential", verdict{ref, err}, sequential)
		classify(t, "parallel", verdict{ref, err}, parallel)
	})
}

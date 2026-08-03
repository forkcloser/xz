// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "math/bits"

// nlz32 computes the number of leading zeros for an unsigned 32-bit integer.
func nlz32(x uint32) int { return bits.LeadingZeros32(x) }

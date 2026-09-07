// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package upstreamdiff is a differential test of this fork against
// github.com/ulikunitz/xz, the project it was forked from.
//
// It is its own module so the library itself never depends on upstream: the
// root go.mod stays free of it, and `go test ./...` from the root does not
// descend here. Run it with `just test-upstreamdiff`, or
//
//	cd internal/upstreamdiff && go test ./...
//
// The contract it enforces is one-directional where the two disagree on
// purpose. Anything either side compresses, the other must decompress to the
// same bytes; anything upstream rejects, this fork must reject too; anything
// this fork accepts, upstream must decode to the same bytes. This fork
// rejecting input upstream accepts is allowed — that is what the hardening
// is for — and is reported, not failed.
package upstreamdiff

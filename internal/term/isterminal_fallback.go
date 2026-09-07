// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin && !dragonfly && !freebsd && (!linux || appengine) && !netbsd && !openbsd && !illumos && !windows

package term

// IsTerminal returns false: this platform has no terminal detection, so gxz
// never refuses to write compressed data to standard output on it. Ported
// from upstream v0.5.16, with illumos excluded because this fork supports it
// natively (see ioctl_illumos.go).
func IsTerminal(fd uintptr) bool {
	return false
}

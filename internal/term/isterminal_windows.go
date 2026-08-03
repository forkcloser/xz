// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package term provides the IsTerminal function.
package term

import "golang.org/x/sys/windows"

// IsTerminal returns true if the given file descriptor is a terminal.
func IsTerminal(fd uintptr) bool {
	var st uint32
	return windows.GetConsoleMode(windows.Handle(fd), &st) == nil
}

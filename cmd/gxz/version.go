// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "runtime/debug"

// version reports the module version gxz was built from: the tag when
// installed with `go install github.com/forkcloser/xz/cmd/gxz@vX.Y.Z`, a
// pseudo-version for a commit, and "(devel)" for a build from a working
// tree. Upstream generated a constant with `xb version-file`; that constant
// went stale the moment this fork stopped running the generator, and lied.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(unknown)"
	}
	return info.Main.Version
}

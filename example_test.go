// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/forkcloser/xz"
)

// These examples are the package's front page on pkg.go.dev, so they are
// written the way calling code should be written: nothing is abandoned
// half-closed, the errors that matter are all checked, and a failure is
// reported through slog and returns, so the deferred calls still run.

func ExampleReader() {
	f, err := os.Open("testdata/fox.xz")
	if err != nil {
		slog.Error("os.Open", "path", "testdata/fox.xz", "err", err)

		return
	}
	defer func() {
		if err = f.Close(); err != nil {
			slog.Error("f.Close()", "err", err)
		}
	}()

	r, err := xz.NewReader(bufio.NewReader(f))
	if err != nil {
		slog.Error("xz.NewReader(f)", "err", err)
		return
	}

	if _, err = io.Copy(os.Stdout, r); err != nil {
		slog.Error("io.Copy", "err", err)
		return
	}
	// Output:
	// The quick brown fox jumps over the lazy dog.
}

func ExampleWriter() {
	// A temporary path keeps the example from dropping a file into whatever
	// directory it is run from — which, when it runs as a test, is the
	// package source directory.
	name := filepath.Join(os.TempDir(), "example.xz")

	f, err := os.Create(name)
	if err != nil {
		slog.Error("os.Create", "path", name, "err", err)

		return
	}
	defer func() { _ = os.Remove(name) }()
	defer func() {
		if err = f.Close(); err != nil {
			slog.Error("f.Close()", "err", err)
		}
	}()

	w, err := xz.NewWriter(f)
	if err != nil {
		slog.Error("xz.NewWriter(f)", "err", err)
		return
	}

	if _, err = fmt.Fprintln(w, "The brown fox jumps over the lazy dog."); err != nil {
		slog.Error("fmt.Fprintln", "err", err)
		return
	}
	// Close finishes the compressed stream. Skipping it, or ignoring what it
	// returns, is how a truncated archive gets written without anyone
	// noticing.
	if err = w.Close(); err != nil {
		slog.Error("w.Close()", "err", err)
		return
	}
	// Output:
}

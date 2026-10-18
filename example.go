// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build ignore

package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"

	"github.com/forkcloser/xz"
)

func main() {
	const text = "The quick brown fox jumps over the lazy dog.\n"
	var buf bytes.Buffer
	// compress text
	w, err := xz.NewWriter(&buf)
	if err != nil {
		slog.Error("xz.NewWriter", "err", err)
		os.Exit(1)
	}
	if _, err := io.WriteString(w, text); err != nil {
		slog.Error("writeString", "err", err)
		os.Exit(1)
	}
	if err := w.Close(); err != nil {
		slog.Error("w.Close", "err", err)
		os.Exit(1)
	}
	// decompress buffer and write output to stdout
	r, err := xz.NewReader(&buf)
	if err != nil {
		slog.Error("newReader", "err", err)
		os.Exit(1)
	}
	if _, err = io.Copy(os.Stdout, r); err != nil {
		slog.Error("io.Copy", "err", err)
		os.Exit(1)
	}
}

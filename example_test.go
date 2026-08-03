// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/forkcloser/xz"
)

// These examples are the package's front page on pkg.go.dev, so they are
// written the way calling code should be written: nothing is abandoned
// half-closed, and the errors that matter are all checked. In particular
// log.Fatal is only reached before anything needs cleaning up, because it
// exits without running deferred calls.

func ExampleReader() {
	f, err := os.Open("fox.xz")
	if err != nil {
		log.Fatalf("os.Open(%q) error %s", "fox.xz", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("f.Close() error %s", err)
		}
	}()
	r, err := xz.NewReader(bufio.NewReader(f))
	if err != nil {
		log.Printf("xz.NewReader(f) error %s", err)
		return
	}
	if _, err = io.Copy(os.Stdout, r); err != nil {
		log.Printf("io.Copy error %s", err)
		return
	}
	// Output:
	// The quick brown fox jumps over the lazy dog.
}

func ExampleWriter() {
	f, err := os.Create("example.xz")
	if err != nil {
		log.Fatalf("os.Create(%q) error %s", "example.xz", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("f.Close() error %s", err)
		}
	}()
	w, err := xz.NewWriter(f)
	if err != nil {
		log.Printf("xz.NewWriter(f) error %s", err)
		return
	}
	if _, err = fmt.Fprintln(w, "The brown fox jumps over the lazy dog."); err != nil {
		log.Printf("fmt.Fprintln error %s", err)
		return
	}
	// Close finishes the compressed stream. Skipping it, or ignoring what it
	// returns, is how a truncated archive gets written without anyone
	// noticing.
	if err = w.Close(); err != nil {
		log.Printf("w.Close() error %s", err)
		return
	}
	// Output:
}

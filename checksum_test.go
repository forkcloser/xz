// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"testing"
)

// The format defines four check types and the package implements all four, but
// the tests only ever exercised the CRC-64 default and a committed
// None-checked file. newCRC32 had no coverage at all, which means the CRC-32
// path — the one the reference tool uses by default — was never run.

var checkTypes = []struct {
	name  string
	flags byte
	size  int
}{
	{"None", None, 0},
	{"CRC32", CRC32, 4},
	{"CRC64", CRC64, 8},
	{"SHA256", SHA256, 32},
}

// writerConfigFor builds a config that actually selects the given check.
// None cannot be requested through CheckSum — see
// TestNoneCheckSumFieldIsNotSelectable — so it goes through NoCheckSum.
func writerConfigFor(flags byte) WriterConfig {
	if flags == None {
		return WriterConfig{NoCheckSum: true}
	}
	return WriterConfig{CheckSum: flags}
}

// TestNoneCheckSumFieldIsNotSelectable pins a trap in the writer API. None is
// 0x0 and WriterConfig.fill treats a zero CheckSum as "not set", so
// WriterConfig{CheckSum: None} silently produces a CRC-64 stream instead of an
// unchecked one, with no error. Selecting None requires the separate
// NoCheckSum field.
func TestNoneCheckSumFieldIsNotSelectable(t *testing.T) {
	var buf bytes.Buffer
	w, err := WriterConfig{CheckSum: None}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.Bytes()[7]; got != CRC64 {
		t.Fatalf("CheckSum: None produced check %#x; documenting it as %#x",
			got, CRC64)
	}

	buf.Reset()
	w, err = WriterConfig{NoCheckSum: true}.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.Bytes()[7]; got != None {
		t.Fatalf("NoCheckSum produced check %#x; want %#x", got, None)
	}
}

func TestCheckTypesRoundTrip(t *testing.T) {
	data := parallelTestData(1 << 16)
	for _, ct := range checkTypes {
		t.Run(ct.name, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := writerConfigFor(ct.flags).NewWriter(&buf)
			if err != nil {
				t.Fatalf("NewWriter: %s", err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatalf("Write: %s", err)
			}
			if err = w.Close(); err != nil {
				t.Fatalf("Close: %s", err)
			}
			file := buf.Bytes()

			// The check size shows up in the stream: a bigger check makes a
			// bigger file. This catches a check being silently skipped.
			if file[7] != ct.flags {
				t.Errorf("stream header flags are %#x; want %#x",
					file[7], ct.flags)
			}

			got, err := io.ReadAll(mustReader(t, file))
			if err != nil {
				t.Fatalf("sequential read: %s", err)
			}
			if !bytes.Equal(got, data) {
				t.Error("sequential decode differs from the original")
			}

			pr, err := NewParallelReader(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				t.Fatalf("NewParallelReader: %s", err)
			}
			defer func() { _ = pr.Close() }()
			got, err = io.ReadAll(pr)
			if err != nil {
				t.Fatalf("parallel read: %s", err)
			}
			if !bytes.Equal(got, data) {
				t.Error("parallel decode differs from the original")
			}
		})
	}
}

// TestCheckTypesDetectCorruption is the part that matters: a check that is
// computed but never compared would pass the round-trip test above. Flipping a
// bit in the compressed data must be reported by every check type that can
// report it.
func TestCheckTypesDetectCorruption(t *testing.T) {
	data := parallelTestData(1 << 15)
	for _, ct := range checkTypes {
		if ct.flags == None {
			continue // nothing to detect with
		}
		t.Run(ct.name, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := writerConfigFor(ct.flags).NewWriter(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			good := buf.Bytes()

			// Corrupt the stored check itself, which sits just before the
			// index. Damaging the compressed data would usually be caught by
			// the LZMA decoder before the check is ever compared; damaging
			// the check can only be caught by comparing it.
			bad := append([]byte{}, good...)
			checkPos := len(bad) - footerLen - 12 - ct.size
			if checkPos <= 0 || checkPos >= len(bad) {
				t.Skipf("cannot locate the check in a %d byte file", len(bad))
			}
			bad[checkPos] ^= 0x01

			r, err := NewReader(bytes.NewReader(bad))
			if err != nil {
				return // rejected already, which is also detection
			}
			if _, err = io.ReadAll(r); err == nil {
				t.Error("a corrupted check was accepted")
			}
		})
	}
}

func mustReader(t *testing.T, file []byte) *Reader {
	t.Helper()
	r, err := NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("NewReader: %s", err)
	}
	return r
}

// TestCheckTypesAgainstXZ decodes our output with the reference tool. A check
// this package computes consistently with itself but differently from
// everybody else would pass every test above and still produce files no other
// implementation accepts.
func TestCheckTypesAgainstXZ(t *testing.T) {
	xzBin, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("xz not installed")
	}
	data := parallelTestData(1 << 16)
	for _, ct := range checkTypes {
		t.Run(ct.name, func(t *testing.T) {
			var buf bytes.Buffer
			w, err := writerConfigFor(ct.flags).NewWriter(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(xzBin, "-dc")
			cmd.Stdin = bytes.NewReader(buf.Bytes())
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("xz -dc rejected our %s output: %s (%s)",
					ct.name, err, stderr.String())
			}
			if !bytes.Equal(out.Bytes(), data) {
				t.Errorf("xz -dc decoded our %s output to different bytes",
					ct.name)
			}
		})
	}
}

// TestReadXZProducedFiles is the other direction: files the reference tool
// produced, including the multi-block layout that only its threaded mode
// emits, have to decode here.
func TestReadXZProducedFiles(t *testing.T) {
	xzBin, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("xz not installed")
	}
	data := parallelTestData(1 << 18)
	src := t.TempDir() + "/data"
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"-0", "-c"},
		{"-9", "-c"},
		{"--check=crc32", "-c"},
		{"--check=sha256", "-c"},
		{"--check=none", "-c"},
		{"-T2", "--block-size=16384", "-c"},
	} {
		t.Run(args[0], func(t *testing.T) {
			cmd := exec.Command(xzBin, append(args, src)...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("xz %v: %s", args, err)
			}
			file := out.Bytes()

			r, err := NewReader(bytes.NewReader(file))
			if err != nil {
				t.Fatalf("NewReader: %s", err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("sequential read: %s", err)
			}
			if !bytes.Equal(got, data) {
				t.Error("sequential decode differs from the original")
			}

			pr, err := NewParallelReader(bytes.NewReader(file), int64(len(file)))
			if err != nil {
				t.Fatalf("NewParallelReader: %s", err)
			}
			defer func() { _ = pr.Close() }()
			got, err = io.ReadAll(pr)
			if err != nil {
				t.Fatalf("parallel read: %s", err)
			}
			if !bytes.Equal(got, data) {
				t.Error("parallel decode differs from the original")
			}
		})
	}
}

// TestStringersDoNotPanic covers the String methods. They are only reached
// through debug logging, which is exactly why they are worth a test: a
// formatting bug there would surface for the first time in whatever
// environment had debug logging turned on.
func TestStringersDoNotPanic(t *testing.T) {
	for _, flags := range []byte{None, CRC32, CRC64, SHA256, 0x7} {
		if s := flagString(flags); s == "" {
			t.Errorf("flagString(%#x) is empty", flags)
		}
		if s := (header{flags: flags}).String(); s == "" {
			t.Errorf("header{%#x}.String() is empty", flags)
		}
		if s := (footer{indexSize: 8, flags: flags}).String(); s == "" {
			t.Errorf("footer{%#x}.String() is empty", flags)
		}
	}
	if got := flagString(0x7); got != "invalid" {
		t.Errorf("flagString of an unknown check is %q; want %q", got, "invalid")
	}

	f := lzmaFilter{dictCap: 1 << 20}
	if s := f.String(); s == "" {
		t.Error("lzmaFilter.String() is empty")
	}
	for _, h := range []blockHeader{
		{compressedSize: -1, uncompressedSize: -1},
		{compressedSize: 10, uncompressedSize: 20, filters: []filter{&f}},
	} {
		_ = h.String() // must not panic; content is for humans only
	}
}

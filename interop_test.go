// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/forkcloser/xz/v2"
	"github.com/forkcloser/xz/v2/lzma"
)

// The interop lane: XZ Utils, the reference implementation, as the oracle.
// The package's writers must produce what xz accepts and decodes to the
// input; the package's readers must decode what xz writes, with every
// preset, check, format and layout xz can be asked for; and on a corrupted
// stream the two must agree on accept or reject. The binary is xz-utils on
// PATH, which `just test` puts there from pins.yaml and then requires
// (XZ_UTILS_REQUIRED=1), so a verify leg that did not run the lane cannot
// look green; a lane that runs the Go recipes alone, without the recipe,
// skips it and says so.

// xzUtils returns the path of the XZ Utils binary, after checking it is
// one: `xz` on the PATH may be another program.
func xzUtils(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("xz-utils")
	if err != nil {
		if os.Getenv("XZ_UTILS_REQUIRED") == "1" {
			t.Fatalf("xz-utils is not on PATH (`just test` fetches it from pins.yaml): %v", err)
		}

		t.Skipf("xz-utils is not on PATH (`just test` fetches it from pins.yaml and requires it): %v", err)
	}

	out, err := exec.CommandContext(t.Context(), path, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", path, err)
	}

	if !strings.HasPrefix(string(out), "xz (XZ Utils) ") {
		t.Fatalf("%s is not XZ Utils: %q", path, bytes.SplitN(out, []byte("\n"), 2)[0])
	}

	return path
}

// runXZ runs xz-utils with stdin and returns its stdout; a non-zero exit is
// the error, with stderr in its message. The environment's own options are
// cleared: they would change what xz does.
func runXZ(t *testing.T, bin string, stdin []byte, args ...string) ([]byte, error) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), bin, args...)

	cmd.Env = append(os.Environ(), "XZ_DEFAULTS=", "XZ_OPT=")
	cmd.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("xz %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return stdout.Bytes(), nil
}

// interopInput is one input of the corpus.
type interopInput struct {
	name string
	data []byte
}

// interopCorpus is the inputs every interop case runs on: the edge sizes,
// text with long matches, incompressible bytes, a long run and a short
// period.
func interopCorpus(t *testing.T) []interopInput {
	t.Helper()

	enwik, err := os.ReadFile("testdata/enwik7")
	if err != nil {
		t.Fatal(err)
	}

	random := make([]byte, 64<<10)
	rng := rand.New(rand.NewPCG(7, 11))

	for i := range random {
		random[i] = byte(rng.Uint32())
	}

	pattern := make([]byte, 256<<10)
	for i := range pattern {
		pattern[i] = byte(i % 257)
	}

	return []interopInput{
		{"empty", nil},
		{"one-byte", []byte{'x'}},
		{"enwik", enwik[:1<<20]},
		{"random", random},
		{"zeros", make([]byte, 1<<20)},
		{"pattern", pattern},
	}
}

// writeThrough compresses data through a writer constructor and returns the
// stream.
func writeThrough(t *testing.T, data []byte, open func(io.Writer) (io.WriteCloser, error)) []byte {
	t.Helper()

	var buf bytes.Buffer

	w, err := open(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// TestInteropWriterToXZUtils holds the package's writers to xz: every
// stream they write, with every configuration a consumer can choose, must
// pass `xz -t` and decode under `xz -dc` to the input.
func TestInteropWriterToXZUtils(t *testing.T) {
	t.Parallel()

	bin := xzUtils(t)

	xzWriter := func(opts ...xz.Option) func(io.Writer) (io.WriteCloser, error) {
		return func(w io.Writer) (io.WriteCloser, error) { return xz.NewWriter(w, opts...) }
	}

	cases := []struct {
		name string
		open func(io.Writer) (io.WriteCloser, error)
		// The options xz needs to read the stream.
		read []string
	}{
		{"default", xzWriter(), nil},
		{"no-check", xzWriter(xz.WithCheck(xz.CheckNone)), nil},
		{"crc32", xzWriter(xz.WithCheck(xz.CheckCRC32)), nil},
		{"sha256", xzWriter(xz.WithCheck(xz.CheckSHA256)), nil},
		{"blocks-64k", xzWriter(xz.WithBlockSize(64 << 10)), nil},
		{"min-dict", xzWriter(xz.WithDictSize(lzma.MinDictSize)), nil},
		{"lc0-lp2-pb0", xzWriter(xz.WithProperties(0, 2, 0)), nil},
		// The write side of every filter: what the writer applies, xz must
		// undo, with and without a start offset, and in a chain.
		{"x86", xzWriter(xz.WithFilters(xz.Filter{ID: xz.X86})), nil},
		{"x86-start", xzWriter(xz.WithFilters(xz.Filter{ID: xz.X86, StartOffset: 4096})), nil},
		{"powerpc", xzWriter(xz.WithFilters(xz.Filter{ID: xz.PowerPC})), nil},
		{"ia64", xzWriter(xz.WithFilters(xz.Filter{ID: xz.IA64, StartOffset: 16})), nil},
		{"arm", xzWriter(xz.WithFilters(xz.Filter{ID: xz.ARM})), nil},
		{"armthumb", xzWriter(xz.WithFilters(xz.Filter{ID: xz.ARMThumb, StartOffset: 2})), nil},
		{"sparc", xzWriter(xz.WithFilters(xz.Filter{ID: xz.SPARC})), nil},
		{"arm64", xzWriter(xz.WithFilters(xz.Filter{ID: xz.ARM64, StartOffset: 8192})), nil},
		{"riscv", xzWriter(xz.WithFilters(xz.Filter{ID: xz.RISCV})), nil},
		{"delta", xzWriter(xz.WithFilters(xz.Filter{ID: xz.Delta, Distance: 4})), nil},
		{
			"delta-x86-blocks",
			xzWriter(
				xz.WithFilters(xz.Filter{ID: xz.Delta, Distance: 2}, xz.Filter{ID: xz.X86}),
				xz.WithBlockSize(64<<10),
			),
			nil,
		},
		{"lzma", func(w io.Writer) (io.WriteCloser, error) { return lzma.NewWriter(w) }, []string{"--format=lzma"}},
		{
			"lzma2-raw",
			func(w io.Writer) (io.WriteCloser, error) { return lzma.NewWriter2(w) },
			[]string{"--format=raw", "--lzma2=dict=8MiB"},
		},
	}

	for _, tc := range cases {
		for _, in := range interopCorpus(t) {
			t.Run(tc.name+"/"+in.name, func(t *testing.T) {
				t.Parallel()

				stream := writeThrough(t, in.data, tc.open)

				if _, err := runXZ(t, bin, stream, append([]string{"-t"}, tc.read...)...); err != nil {
					t.Fatalf("xz refuses the stream: %v", err)
				}

				out, err := runXZ(t, bin, stream, append([]string{"-dc"}, tc.read...)...)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(out, in.data) {
					t.Fatalf("xz decodes %d bytes, want %d", len(out), len(in.data))
				}
			})
		}
	}
}

// readAll decodes a stream through a reader constructor.
func readAll(open func(io.Reader) (io.Reader, error), stream []byte) ([]byte, error) {
	r, err := open(bytes.NewReader(stream))
	if err != nil {
		return nil, err
	}

	return io.ReadAll(r)
}

// readParallel decodes a stream through the Reader with workers.
func readParallel(stream []byte) ([]byte, error) {
	r, err := xz.NewReader(bytes.NewReader(stream), xz.WithWorkers(4))
	if err != nil {
		return nil, err
	}

	out, err := io.ReadAll(r)
	if cerr := r.Close(); err == nil {
		err = cerr
	}

	return out, err
}

// TestInteropXZUtilsToReaders holds the package's readers to xz: whatever
// xz writes, with every preset, check, block layout, filter and format, the
// readers must decode to the input.
func TestInteropXZUtilsToReaders(t *testing.T) {
	t.Parallel()

	bin := xzUtils(t)

	xzReader := func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }

	cases := []struct {
		name string
		args []string
		// How the package reads it; nil for the xz readers, Reader and
		// ParallelReader both.
		open func(io.Reader) (io.Reader, error)
	}{
		{"preset-0", []string{"-0"}, nil},
		{"preset-6", []string{"-6"}, nil},
		{"preset-9e", []string{"-9e"}, nil},
		{"check-none", []string{"-C", "none"}, nil},
		{"check-crc32", []string{"-C", "crc32"}, nil},
		{"check-sha256", []string{"-C", "sha256"}, nil},
		{"blocks-64k", []string{"-6", "--block-size=65536"}, nil},
		{"threads-blocks-64k", []string{"-6", "-T2", "--block-size=65536"}, nil},
		{"lc0-lp2-pb0", []string{"--lzma2=preset=6,lc=0,lp=2,pb=0"}, nil},
		{"x86", []string{"--x86", "--lzma2=preset=6"}, nil},
		{"x86-start", []string{"--x86=start=4096", "--lzma2=preset=6"}, nil},
		{"powerpc", []string{"--powerpc", "--lzma2=preset=6"}, nil},
		{"ia64", []string{"--ia64", "--lzma2=preset=6"}, nil},
		{"arm", []string{"--arm", "--lzma2=preset=6"}, nil},
		{"armthumb", []string{"--armthumb", "--lzma2=preset=6"}, nil},
		{"sparc", []string{"--sparc", "--lzma2=preset=6"}, nil},
		{"arm64", []string{"--arm64", "--lzma2=preset=6"}, nil},
		{"riscv", []string{"--riscv", "--lzma2=preset=6"}, nil},
		{"delta", []string{"--delta=dist=4", "--lzma2=preset=6"}, nil},
		{
			"delta-x86-threads",
			[]string{"--delta=dist=2", "--x86", "--lzma2=preset=6", "-T2", "--block-size=65536"},
			nil,
		},
		{"lzma", []string{"--format=lzma", "-6"}, func(r io.Reader) (io.Reader, error) { return lzma.NewReader(r) }},
		{
			"lzma2-raw",
			[]string{"--format=raw", "--lzma2=preset=6"},
			func(r io.Reader) (io.Reader, error) { return lzma.NewReader2(r, 8<<20) },
		},
	}

	for _, tc := range cases {
		for _, in := range interopCorpus(t) {
			t.Run(tc.name+"/"+in.name, func(t *testing.T) {
				t.Parallel()

				stream, err := runXZ(t, bin, in.data, append([]string{"-c"}, tc.args...)...)
				if err != nil {
					t.Fatal(err)
				}

				open := tc.open
				if open == nil {
					open = xzReader
				}

				out, err := readAll(open, stream)
				if err != nil {
					t.Fatalf("Reader: %v", err)
				}

				if !bytes.Equal(out, in.data) {
					t.Fatalf("Reader decodes %d bytes, want %d", len(out), len(in.data))
				}

				if tc.open != nil {
					return
				}

				out, err = readParallel(stream)
				if err != nil {
					t.Fatalf("ParallelReader: %v", err)
				}

				if !bytes.Equal(out, in.data) {
					t.Fatalf("ParallelReader decodes %d bytes, want %d", len(out), len(in.data))
				}
			})
		}
	}
}

// TestInteropConcatenated holds the readers to xz on a file of several
// streams, with and without stream padding between them: both decode the
// concatenation, and SingleStream stops after the first and reports the rest
// as xz --single-stream does.
func TestInteropConcatenated(t *testing.T) {
	t.Parallel()

	bin := xzUtils(t)
	corpus := interopCorpus(t)
	first, second := corpus[2].data[:300<<10], corpus[5].data

	a, err := runXZ(t, bin, first, "-c", "-6")
	if err != nil {
		t.Fatal(err)
	}

	b, err := runXZ(t, bin, second, "-c", "-0", "-C", "crc32")
	if err != nil {
		t.Fatal(err)
	}

	want := slices.Concat(first, second)

	for _, tc := range []struct {
		name   string
		stream []byte
	}{
		{"adjacent", slices.Concat(a, b)},
		{"padded", slices.Concat(a, make([]byte, 8), b)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// xz itself must read it as one file; a padding xz refuses
			// would make the case meaningless.
			out, err := runXZ(t, bin, tc.stream, "-dc")
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(out, want) {
				t.Fatalf("xz decodes %d bytes, want %d", len(out), len(want))
			}

			out, err = readAll(func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }, tc.stream)
			if err != nil {
				t.Fatalf("Reader: %v", err)
			}

			if !bytes.Equal(out, want) {
				t.Fatalf("Reader decodes %d bytes, want %d", len(out), len(want))
			}

			out, err = readParallel(tc.stream)
			if err != nil {
				t.Fatalf("ParallelReader: %v", err)
			}

			if !bytes.Equal(out, want) {
				t.Fatalf("ParallelReader decodes %d bytes, want %d", len(out), len(want))
			}

			// xz --single-stream decodes the first stream and ignores the
			// rest; the package's SingleStream decodes the first and reports
			// the rest as corruption. Both agree on the first stream's data.
			out, err = runXZ(t, bin, tc.stream, "-dc", "--single-stream")
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(out, first) {
				t.Fatalf("xz --single-stream decodes %d bytes, want %d", len(out), len(first))
			}

			out, err = readAll(
				func(r io.Reader) (io.Reader, error) { return xz.NewReader(r, xz.WithSingleStream()) },
				tc.stream,
			)
			if !errors.Is(err, xz.ErrCorrupt) {
				t.Fatalf("SingleStream: %v, want ErrCorrupt after the first stream", err)
			}

			if !bytes.Equal(out, first) {
				t.Fatalf("SingleStream decodes %d bytes before the error, want %d", len(out), len(first))
			}
		})
	}
}

// TestInteropCorruptionVerdicts flips one byte of a stream at positions
// spread over its whole length, header to footer, and requires xz and the
// package's readers to agree on every one: accepted by both, or refused by
// both. A flip both accept is one that lands in data the check does not
// cover or that happens to keep the stream valid; what is tested is that the
// package never accepts what xz refuses, nor refuses what xz accepts.
func TestInteropCorruptionVerdicts(t *testing.T) {
	t.Parallel()

	bin := xzUtils(t)
	corpus := interopCorpus(t)

	stream, err := runXZ(t, bin, corpus[2].data[:256<<10], "-c", "-6", "--block-size=65536")
	if err != nil {
		t.Fatal(err)
	}

	// Every byte of the header and footer, the first block header, and a
	// spread over the rest.
	positions := map[int]struct{}{}
	for i := range 24 {
		positions[i] = struct{}{}
	}

	for i := len(stream) - 48; i < len(stream); i++ {
		positions[i] = struct{}{}
	}

	for i := 24; i < len(stream)-48; i += (len(stream) - 72) / 40 {
		positions[i] = struct{}{}
	}

	for pos := range positions {
		t.Run(fmt.Sprintf("flip-%d-of-%d", pos, len(stream)), func(t *testing.T) {
			t.Parallel()

			flipped := append([]byte{}, stream...)
			flipped[pos] ^= 0x55

			_, xzErr := runXZ(t, bin, flipped, "-t")
			xzOK := xzErr == nil

			out, err := readAll(func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }, flipped)
			readerOK := err == nil && bytes.Equal(out, corpus[2].data[:256<<10])

			if readerOK != xzOK {
				t.Fatalf("Reader accepts: %v (%v); xz accepts: %v (%v)", readerOK, err, xzOK, xzErr)
			}

			out, err = readParallel(flipped)
			parallelOK := err == nil && bytes.Equal(out, corpus[2].data[:256<<10])

			if parallelOK != xzOK {
				t.Fatalf("ParallelReader accepts: %v (%v); xz accepts: %v (%v)", parallelOK, err, xzOK, xzErr)
			}
		})
	}
}

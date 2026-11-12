// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xz"
)

// The BCJ and delta filters, held to XZ Utils: testdata/filters holds, for
// each filter, start offset and distance below, what `xz` makes of one
// fixed input, and both readers must decode each file back to that input.
// The input is regenerated here from its seed, so only the compressed
// files are committed; XZ_UPDATE_FILTER_GOLDEN=1 rewrites them with the
// xz-utils binary on PATH.

// filterCase is one golden file: its name and the xz options that made it.
type filterCase struct {
	name string
	args []string
}

// filterCases are the golden files.
func filterCases() []filterCase {
	return []filterCase{
		{"x86", []string{"--x86"}},
		{"x86-start", []string{"--x86=start=4096"}},
		{"powerpc", []string{"--powerpc"}},
		{"powerpc-start", []string{"--powerpc=start=4096"}},
		{"ia64", []string{"--ia64"}},
		{"ia64-start", []string{"--ia64=start=4096"}},
		{"arm", []string{"--arm"}},
		{"arm-start", []string{"--arm=start=4096"}},
		{"armthumb", []string{"--armthumb"}},
		{"armthumb-start", []string{"--armthumb=start=4098"}},
		{"sparc", []string{"--sparc"}},
		{"sparc-start", []string{"--sparc=start=4096"}},
		{"arm64", []string{"--arm64"}},
		{"arm64-start", []string{"--arm64=start=8192"}},
		{"riscv", []string{"--riscv"}},
		{"riscv-start", []string{"--riscv=start=4098"}},
		{"delta-1", []string{"--delta=dist=1"}},
		{"delta-4", []string{"--delta=dist=4"}},
		{"delta-256", []string{"--delta=dist=256"}},
		{"delta-x86", []string{"--delta=dist=2", "--x86"}},
		{"x86-delta-arm", []string{"--x86", "--delta=dist=3", "--arm"}},
		{"blocks-arm64", []string{"--arm64", "--block-size=4096"}},
	}
}

// filterInput is the input every golden file compresses: bytes drawn from
// the opcodes the filters look for, so their instruction patterns are dense
// and their operands vary, then crafted IA-64 and PowerPC branches, whose
// patterns random bytes rarely form, then a run of uniform random bytes.
func filterInput() []byte {
	rng := rand.New(rand.NewPCG(0x6263, 0x6A))

	opcodes := []byte{
		0x00, 0xFF, 0xE8, 0xE9, 0xEB, 0xF0, 0xF8, 0x48, 0x40, 0x7F,
		0xC0, 0x94, 0x90, 0x17, 0xEF, 0x05,
	}

	data := make([]byte, 16<<10, 24<<10)
	for i := range data {
		data[i] = opcodes[rng.IntN(len(opcodes))]
	}

	data = append(data, craftedBranches(rng)...)

	for range 4 << 10 {
		data = append(data, byte(rng.Uint32()))
	}

	return data
}

// craftedBranches returns instructions the sparser patterns of random bytes
// rarely form: IA-64 bundles whose template admits a branch and whose slots
// hold a br.call, and PowerPC bl instructions, each with random operands.
func craftedBranches(rng *rand.Rand) []byte {
	templates := []byte{16, 17, 18, 19, 22, 23, 24, 25, 28, 29}

	var out []byte

	for range 128 {
		// A bundle is 5 template bits then three 41-bit slots, least
		// significant bit first.
		var bits [3]uint64
		for s := range bits {
			v := rng.Uint64() & (1<<41 - 1)
			v &^= 0xF << 37
			v |= 0x5 << 37
			v &^= 0x7 << 9
			bits[s] = v
		}

		var bundle [16]byte

		bundle[0] = templates[rng.IntN(len(templates))] | byte(rng.Uint32()&0xE0)

		pos := uint(5)

		for _, v := range bits {
			for b := range uint(41) {
				if v>>b&1 != 0 {
					bundle[(pos+b)>>3] |= 1 << ((pos + b) & 7)
				}
			}

			pos += 41
		}

		out = append(out, bundle[:]...)
	}

	for range 256 {
		r := rng.Uint32()
		out = append(out, 0x48|byte(r&3), byte(r>>8), byte(r>>16), byte(r>>24)&^3|1)
	}

	return out
}

func filterGoldenPath(name string) string {
	return filepath.Join("testdata", "filters", name+".xz")
}

// TestGenerateFilterGolden rewrites the golden files with the XZ Utils
// binary on PATH. It runs only with XZ_UPDATE_FILTER_GOLDEN=1.
func TestGenerateFilterGolden(t *testing.T) {
	t.Parallel()

	if os.Getenv("XZ_UPDATE_FILTER_GOLDEN") != "1" {
		t.Skip("XZ_UPDATE_FILTER_GOLDEN is not 1")
	}

	bin, err := exec.LookPath("xz-utils")
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), bin, "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), "xz (XZ Utils) ") {
		t.Fatalf("%s is not XZ Utils: %q, %v", bin, out, err)
	}

	input := filterInput()

	for _, tc := range filterCases() {
		// The chain is given in order, LZMA2 last.
		args := append(append([]string{"-c", "-T1"}, tc.args...), "--lzma2=preset=6")
		cmd := exec.CommandContext(t.Context(), bin, args...)

		cmd.Env = append(os.Environ(), "XZ_DEFAULTS=", "XZ_OPT=")
		cmd.Stdin = bytes.NewReader(input)

		var stderr bytes.Buffer

		cmd.Stderr = &stderr

		data, err := cmd.Output()
		if err != nil {
			t.Fatalf("xz %s: %v: %s", strings.Join(args, " "), err, stderr.String())
		}

		if err := os.WriteFile(filterGoldenPath(tc.name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFiltersAgainstXZUtils(t *testing.T) {
	t.Parallel()

	input := filterInput()

	for _, tc := range filterCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(filterGoldenPath(tc.name))
			if err != nil {
				t.Fatal(err)
			}

			r, err := xz.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("Reader: %v", err)
			}

			if !bytes.Equal(out, input) {
				t.Fatalf(
					"Reader decodes %d bytes, differing from the input (first difference at %d)",
					len(out),
					firstDiff(out, input),
				)
			}

			pr, err := xz.NewParallelReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("NewParallelReader: %v", err)
			}

			out, err = io.ReadAll(pr)
			if cerr := pr.Close(); err == nil {
				err = cerr
			}

			if err != nil {
				t.Fatalf("ParallelReader: %v", err)
			}

			if !bytes.Equal(out, input) {
				t.Fatalf(
					"ParallelReader decodes %d bytes, differing from the input (first difference at %d)",
					len(out),
					firstDiff(out, input),
				)
			}
		})
	}
}

// firstDiff is the index of the first byte where a and b differ, or the
// shorter length.
func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}

	return n
}

// TestFiltersSmallReads decodes a golden file through reads of every small
// size: the BCJ reader's tail handling must not depend on how much the
// consumer asks for or how much the LZMA2 reader returns.
func TestFiltersSmallReads(t *testing.T) {
	t.Parallel()

	input := filterInput()

	for _, name := range []string{"x86", "riscv", "ia64", "x86-delta-arm"} {
		data, err := os.ReadFile(filterGoldenPath(name))
		if err != nil {
			t.Fatal(err)
		}

		for _, size := range []int{1, 3, 7, 64, 4097} {
			t.Run(fmt.Sprintf("%s/%d", name, size), func(t *testing.T) {
				t.Parallel()

				r, err := xz.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}

				var out []byte

				buf := make([]byte, size)

				for {
					n, err := r.Read(buf)
					out = append(out, buf[:n]...)

					if errors.Is(err, io.EOF) {
						break
					}

					if err != nil {
						t.Fatal(err)
					}
				}

				if !bytes.Equal(out, input) {
					t.Fatalf(
						"reads of %d bytes decode %d bytes, differing from the input at %d",
						size,
						len(out),
						firstDiff(out, input),
					)
				}
			})
		}
	}
}

// TestFiltersRefused covers the block headers a reader must refuse: a BCJ
// start offset its alignment does not divide, properties of the wrong
// size, and chains in the wrong order.
func TestFiltersRefused(t *testing.T) {
	t.Parallel()

	good, err := os.ReadFile(filterGoldenPath("arm-start"))
	if err != nil {
		t.Fatal(err)
	}

	// The block header starts after the 12-byte stream header: size byte,
	// flags, then the filter flags: ARM (0x07), 4 bytes of properties, the
	// start offset 4096 little endian.
	const hdr = 12

	off := bytes.Index(good[hdr:hdr+32], []byte{0x07, 0x04, 0x00, 0x10, 0x00, 0x00})
	if off < 0 {
		t.Fatal("the ARM filter flags are not where the header puts them")
	}

	off += hdr

	for _, tc := range []struct {
		name string
		// The patch replaces bytes at the given distance from the ARM
		// field: 0 is the ARM field itself, 6 the three-byte LZMA2 field
		// after it.
		at    int
		patch []byte
		kind  error
	}{
		{"misaligned start offset", 0, []byte{0x07, 0x04, 0x01, 0x10, 0x00, 0x00}, xz.ErrUnsupported},
		{"properties size 2", 0, []byte{0x07, 0x02, 0x00, 0x10, 0x00, 0x00}, xz.ErrCorrupt},
		// Delta in LZMA2's place: the chain ARM then delta, whose last
		// filter is not LZMA2.
		{"delta last", 6, []byte{0x03, 0x01, 0x00}, xz.ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := bytes.Clone(good)
			copy(data[off+tc.at:], tc.patch)
			// The header's CRC32 no longer matches; a reader must report
			// the field, not the checksum, so the checksum is recomputed
			// by the helper the format tests share.
			fixBlockHeaderCRC(t, data[hdr:])

			_, err := xz.NewReader(bytes.NewReader(data))
			if err == nil {
				r, _ := xz.NewReader(bytes.NewReader(data))
				_, err = io.ReadAll(r)
			}

			if !errors.Is(err, tc.kind) {
				t.Fatalf("got %v, want %v", err, tc.kind)
			}
		})
	}
}

// fixBlockHeaderCRC recomputes the CRC32 of the block header at the start
// of data, whose size byte gives its length.
func fixBlockHeaderCRC(t *testing.T, data []byte) {
	t.Helper()

	n := (int(data[0]) + 1) * 4
	if n > len(data) {
		t.Fatalf("block header of %d bytes in %d", n, len(data))
	}

	binary.LittleEndian.PutUint32(data[n-4:], crc32.ChecksumIEEE(data[:n-4]))
}

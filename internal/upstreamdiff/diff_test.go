// Copyright 2026 Forkcloser. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package upstreamdiff

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"testing"

	fork "github.com/forkcloser/xz"
	forklzma "github.com/forkcloser/xz/lzma"
	upstream "github.com/ulikunitz/xz"
	upstreamlzma "github.com/ulikunitz/xz/lzma"
)

// corpora returns the inputs both sides are driven with: the shapes a
// compressor cares about, plus the first part of the benchmark corpus when
// the checkout has it.
func corpora(tb testing.TB) map[string][]byte {
	tb.Helper()
	rng := rand.New(rand.NewSource(1))
	random := make([]byte, 1<<18)
	rng.Read(random)

	var text bytes.Buffer
	words := []string{"the ", "quick ", "brown ", "fox ", "jumps ", "over ",
		"lazy ", "dog ", "0000000000000000", "\n"}
	for text.Len() < 1<<18 {
		text.WriteString(words[rng.Intn(len(words))])
	}

	c := map[string][]byte{
		"empty":      {},
		"one byte":   {'x'},
		"text":       text.Bytes(),
		"random":     random,
		"zeros":      make([]byte, 1<<20),
		"repetitive": bytes.Repeat([]byte("abcabcabd"), 1<<15),
	}
	if enwik, err := os.ReadFile("../../testdata/enwik7"); err == nil {
		c["enwik7 prefix"] = enwik[:min(len(enwik), 2<<20)]
	}
	return c
}

func forkCompress(tb testing.TB, cfg fork.WriterConfig, data []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		tb.Fatalf("fork NewWriter: %v", err)
	}
	if _, err = w.Write(data); err != nil {
		tb.Fatalf("fork Write: %v", err)
	}
	if err = w.Close(); err != nil {
		tb.Fatalf("fork Close: %v", err)
	}
	return buf.Bytes()
}

func upstreamCompress(tb testing.TB, cfg upstream.WriterConfig, data []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := cfg.NewWriter(&buf)
	if err != nil {
		tb.Fatalf("upstream NewWriter: %v", err)
	}
	if _, err = w.Write(data); err != nil {
		tb.Fatalf("upstream Write: %v", err)
	}
	if err = w.Close(); err != nil {
		tb.Fatalf("upstream Close: %v", err)
	}
	return buf.Bytes()
}

func forkDecompress(file []byte) ([]byte, error) {
	r, err := fork.NewReader(bytes.NewReader(file))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func forkDecompressParallel(file []byte) ([]byte, error) {
	r, err := fork.NewParallelReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func upstreamDecompress(file []byte) ([]byte, error) {
	r, err := upstream.NewReader(bytes.NewReader(file))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// TestXZInterchange compresses every corpus with every configuration on each
// side and decompresses it on the other. A file this fork writes that
// upstream (or the reference xz, which upstream was tested against) cannot
// read would be a format bug; a file upstream writes that this fork cannot
// read would be a regression in the reader.
func TestXZInterchange(t *testing.T) {
	forkConfigs := map[string]fork.WriterConfig{
		"default":    {},
		"crc32":      {CheckSum: fork.CRC32},
		"sha256":     {CheckSum: fork.SHA256},
		"none":       {NoCheckSum: true},
		"blocks 64K": {BlockSize: 64 << 10},
		"blocks 4K":  {BlockSize: 4 << 10, CheckSum: fork.CRC32},
		"small dict": {DictCap: forklzma.MinDictCap},
	}
	upstreamConfigs := map[string]upstream.WriterConfig{
		"default":    {},
		"crc32":      {CheckSum: upstream.CRC32},
		"sha256":     {CheckSum: upstream.SHA256},
		"none":       {NoCheckSum: true},
		"blocks 64K": {BlockSize: 64 << 10},
		"blocks 4K":  {BlockSize: 4 << 10, CheckSum: upstream.CRC32},
	}
	for name, data := range corpora(t) {
		for cname, cfg := range forkConfigs {
			t.Run(name+"/fork writes/"+cname, func(t *testing.T) {
				file := forkCompress(t, cfg, data)
				got, err := upstreamDecompress(file)
				if err != nil {
					t.Fatalf("upstream cannot read what the fork wrote: %v", err)
				}
				if !bytes.Equal(got, data) {
					t.Fatal("upstream decoded different bytes from what the fork wrote")
				}
			})
		}
		for cname, cfg := range upstreamConfigs {
			t.Run(name+"/upstream writes/"+cname, func(t *testing.T) {
				file := upstreamCompress(t, cfg, data)
				got, err := forkDecompress(file)
				if err != nil {
					t.Fatalf("the fork cannot read what upstream wrote: %v", err)
				}
				if !bytes.Equal(got, data) {
					t.Fatal("the fork decoded different bytes from what upstream wrote")
				}
				got, err = forkDecompressParallel(file)
				if err != nil {
					t.Fatalf("the parallel reader cannot read what upstream wrote: %v", err)
				}
				if !bytes.Equal(got, data) {
					t.Fatal("the parallel reader decoded different bytes from what upstream wrote")
				}
			})
		}
	}
}

// TestLZMAInterchange is the same exchange for the lzma package: the classic
// format and raw LZMA2 chunk sequences.
func TestLZMAInterchange(t *testing.T) {
	for name, data := range corpora(t) {
		t.Run(name+"/classic", func(t *testing.T) {
			var buf bytes.Buffer
			w, err := forklzma.NewWriter(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := upstreamlzma.NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("upstream lzma.NewReader on the fork's stream: %v", err)
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("upstream read of the fork's classic stream: err=%v, %d bytes", err, len(got))
			}

			buf.Reset()
			uw, err := upstreamlzma.NewWriter(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = uw.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = uw.Close(); err != nil {
				t.Fatal(err)
			}
			fr, err := forklzma.NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("fork lzma.NewReader on upstream's stream: %v", err)
			}
			got, err = io.ReadAll(fr)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("fork read of upstream's classic stream: err=%v, %d bytes", err, len(got))
			}
		})
		t.Run(name+"/lzma2", func(t *testing.T) {
			var buf bytes.Buffer
			w, err := forklzma.NewWriter2(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			r, err := upstreamlzma.NewReader2(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("upstream lzma.NewReader2 on the fork's stream: %v", err)
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("upstream read of the fork's LZMA2 stream: err=%v, %d bytes", err, len(got))
			}

			buf.Reset()
			uw, err := upstreamlzma.NewWriter2(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = uw.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = uw.Close(); err != nil {
				t.Fatal(err)
			}
			fr, err := forklzma.NewReader2(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("fork lzma.NewReader2 on upstream's stream: %v", err)
			}
			got, err = io.ReadAll(fr)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("fork read of upstream's LZMA2 stream: err=%v, %d bytes", err, len(got))
			}
		})
	}
}

// TestCorruptionVerdictsAgree flips every byte of a small file and compares
// verdicts. Upstream rejecting an input this fork accepts would mean the
// hardening loosened something; both accepting must mean the same bytes. The
// other direction — this fork rejecting what upstream accepts — is the
// point of the fork (upstream decodes a truncated file as a complete one,
// for instance) and is counted, not failed.
func TestCorruptionVerdictsAgree(t *testing.T) {
	data := corpora(t)["text"][:4096]
	for cname, file := range map[string][]byte{
		"single block": forkCompress(t, fork.WriterConfig{}, data),
		"multi block":  forkCompress(t, fork.WriterConfig{BlockSize: 1024}, data),
	} {
		var stricter int
		for i := range file {
			for _, mask := range []byte{0x40, 0xff} {
				bad := append([]byte{}, file...)
				bad[i] ^= mask
				forkOut, forkErr := forkDecompress(bad)
				upOut, upErr := upstreamDecompress(bad)
				switch {
				case forkErr == nil && upErr != nil:
					t.Errorf("%s: byte %d ^ %#x: the fork accepted an input upstream rejects (%v)",
						cname, i, mask, upErr)
				case forkErr == nil && upErr == nil && !bytes.Equal(forkOut, upOut):
					t.Errorf("%s: byte %d ^ %#x: both accepted, different output", cname, i, mask)
				case forkErr != nil && upErr == nil:
					stricter++
				}
			}
		}
		t.Logf("%s: the fork rejected %d corruptions upstream accepted", cname, stricter)
	}

	// Truncation: every prefix. Upstream accepts a file cut exactly at the
	// end of its last block; the fork must never accept a prefix.
	file := forkCompress(t, fork.WriterConfig{BlockSize: 1024}, data)
	for n := range file {
		if out, err := forkDecompress(file[:n]); err == nil && bytes.Equal(out, data) {
			t.Errorf("the fork accepted a %d byte prefix of a %d byte file as complete", n, len(file))
		}
		if _, err := upstreamDecompress(file[:n]); err != nil {
			if _, ferr := forkDecompress(file[:n]); ferr == nil {
				t.Errorf("prefix %d: upstream rejects (%v), the fork accepts", n, err)
			}
		}
	}
}

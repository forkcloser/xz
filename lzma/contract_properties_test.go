package lzma_test

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"slices"
	"testing"

	"github.com/forkcloser/xz/lzma"
)

// tailReader is a reader over a chunk sequence followed by a suffix that
// is not the sequence's: what Reader2 leaves of it says where it stopped.
func tailReader(stream []byte, suffix string) *bytes.Reader {
	return bytes.NewReader(slices.Concat(stream, []byte(suffix)))
}

// TestContractReader2StopsAtTheSequencesEnd: Reader2 reads the chunk
// sequence and nothing past its end chunk: a suffix after the sequence is
// left unread in the source, byte for byte, and EOS reports the end.
func TestContractReader2StopsAtTheSequencesEnd(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for cname, c := range writer2Configs() {
			stream := lzma2(t, c, data)
			src := tailReader(stream, "SUFFIX after the sequence")

			r, err := lzma.Reader2Config{DictCap: 8 << 20}.NewReader2(src)
			if err != nil {
				t.Fatalf("%s, %s: NewReader2: %v", name, cname, err)
			}

			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s, %s: %d bytes, %v", name, cname, len(got), err)
			}

			if !r.EOS() {
				t.Errorf("%s, %s: EOS false after the end chunk", name, cname)
			}

			rest, _ := io.ReadAll(src)
			if string(rest) != "SUFFIX after the sequence" {
				t.Errorf(
					"%s, %s: the source has %q left, want the suffix whole: the reader read past the sequence",
					name,
					cname,
					rest,
				)
			}
		}
	}
}

// TestContractReader2NeverReturnsNothingWithoutAnError: with a buffer of
// any size above zero, Read returns bytes or an error, never (0, nil),
// and the bytes concatenated are the input.
func TestContractReader2NeverReturnsNothingWithoutAnError(t *testing.T) {
	t.Parallel()

	for name, data := range corpus(t) {
		for _, size := range []int{1, 3, 7, 64, 4096} {
			stream := lzma2(t, lzma.Writer2Config{}, data)

			r, err := lzma.Reader2Config{DictCap: 8 << 20}.NewReader2(bytes.NewReader(stream))
			if err != nil {
				t.Fatal(err)
			}

			var got []byte

			buf := make([]byte, size)

			for {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)

				if n == 0 && err == nil {
					t.Fatalf("%s, buffer %d: Read returned 0, nil after %d bytes", name, size, len(got))
				}

				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatalf("%s, buffer %d: %v", name, size, err)
				}
			}

			if !bytes.Equal(got, data) {
				t.Errorf("%s, buffer %d: %d bytes, want %d", name, size, len(got), len(data))
			}
		}
	}
}

// TestContractReader2AllocatesTheDictionaryLazily: a reader made for a
// dictionary of a gigabyte costs nothing like a gigabyte until it decodes:
// NewReader2 and the first chunk of a small stream together grow the heap
// by less than a dictionary would.
//
//nolint:paralleltest // measures the heap; another test's allocations in the window would be counted
func TestContractReader2AllocatesTheDictionaryLazily(t *testing.T) {
	stream := lzma2(t, lzma.Writer2Config{}, []byte("a small stream of a few bytes"))

	var before, after runtime.MemStats

	// The baseline is the heap after a collection, so garbage from the
	// stream's construction is not counted against the reader.
	//revive:disable-next-line:call-to-gc
	runtime.GC()
	runtime.ReadMemStats(&before)

	r, err := lzma.Reader2Config{DictCap: 1 << 30}.NewReader2(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}

	runtime.ReadMemStats(&after)

	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 64<<20 {
		t.Errorf("NewReader2 with a 1 GiB dictionary grew the heap by %d bytes", grown)
	}

	got, err := io.ReadAll(r)
	if err != nil || string(got) != "a small stream of a few bytes" {
		t.Fatalf("read: %q, %v", got, err)
	}

	runtime.ReadMemStats(&after)

	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 64<<20 {
		t.Errorf("decoding a 29-byte stream under a 1 GiB dictionary grew the heap by %d bytes", grown)
	}
}

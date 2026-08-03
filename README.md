# Package xz

This Go language package supports the reading and writing of xz
compressed streams. It includes also a gxz command for compressing and
decompressing data. The package is completely written in Go and doesn't
have any dependency on any C code.

APIs are not considered stable. Compression speed and ratio do not match
the xz tool, whose algorithms have been tuned over a long time.
Decompression is a different story: see the numbers below, and
`ParallelReader` for block-parallel decoding of multi-block archives.

## About this fork

This here is a friendly fork of https://github.com/ulikunitz/xz.
Upstream seems inactive. However, if you have time and interest in doing
that, feel free to carry these changes over there.

The fork diverges from upstream in three areas:

**Performance.** Serial decoding is a bit over twice as fast as
upstream, mostly by buffering each LZMA2 chunk in memory so the range
decoder reads bytes by index instead of through a per-byte interface
call, and by keeping the hot bit-decoding loops free of calls and error
branches. Decoder state, probability models, dictionary and read
buffers are reused across chunks and across the blocks of a file, which
takes allocations down from about 1.2 million to 132 per 10 MB decode
and from 1.2 million to 306 per encode. `ParallelReader` decodes the
blocks of multi-block archives concurrently on top of that. The decoder
dictionary grows on demand instead of being allocated at its declared
size — a stream that produces little never pays for the 4 GiB its
header may declare, at the cost of roughly twice the final size in
allocations for streams that fill it.

**Robustness.** Every number in an xz index is attacker controlled, so
the parallel reader binds its memory use to what a block actually
decodes to rather than what the index declares, and rejects record
counts, sizes and overflows that upstream fed into allocations or loop
bounds. A `ParallelReader` that is dropped without `Close` winds down
its goroutines instead of leaking them. Decoding errors are classified:
everything that means "this input is not valid xz" matches
`ErrCorrupt`, unsupported-but-valid features match `ErrUnsupported`,
and I/O errors from the underlying reader pass through untouched, so
callers can tell a corrupt file from a failed transport. A truncated
file is reported as such instead of decoding as a shorter one, and a
writer flush failure surfaces instead of silently producing a short
stream, as upstream v0.5.16 does for small-dictionary configurations —
configurations this fork briefly rejected and now encodes correctly.

**Verification.** The decoder is differentially tested against
upstream and against the `xz` tool across encoder configurations,
payload shapes, multi-stream files and dictionary-growth boundaries,
and fuzzed both in-repo (serial and parallel readers must agree) and
against upstream. Malformed-input tests cover truncation and bit flips
at every offset and a corpus of hostile index constructions with an
allocation budget. `AUDIT.md` records a full audit of the tree,
including the findings that led to the fixes above and the negative
results that were measured and rejected.

### Benchmarks

Measured on `testdata/enwik7` (10 MB of Wikipedia text), Apple M5 Pro,
Go 1.26, 2026-08-02. Upstream is `github.com/ulikunitz/xz` v0.5.16 on
the same benchmark bodies. Reproduce with:

    go test -run '^$' -bench 'Reader|Writer' -benchmem -benchtime=5x -count=6 .

| Benchmark           | Upstream v0.5.16 | This fork | Change |
| ------------------- | ---------------- | --------- | ------ |
| Reader (decompress) | 48 MB/s          | 100 MB/s  | +110%  |
| Reader allocs/op    | 1,213,039        | 132       | −99.99% |
| Writer (compress)   | 14.8 MB/s        | 16 MB/s   | +8%    |
| Writer allocs/op    | 1,217,296        | 306       | −99.97% |

Multi-block files (the shape `xz -T` produces, and the one
`ParallelReader` exists for), same corpus:

| Benchmark                                | Throughput | Allocs/op |
| ---------------------------------------- | ---------- | --------- |
| Reader, 153 × 64 KiB blocks              | 73 MB/s    | 2,576     |
| ParallelReader, 10 × 1 MiB blocks, 18 workers | 700 MB/s | ~1,265 |
| ParallelReader, 153 × 64 KiB blocks, 18 workers | ~980 MB/s | — |

Compression ratio is identical to upstream in the default
configuration. Multi-block serial decoding is slower than single-block
per byte because each block restarts the dictionary; that cost is
intrinsic to the format, not to this implementation.


## Using the API

The following example program shows how to use the API.

```go
package main

import (
    "bytes"
    "io"
    "log"
    "os"

    "github.com/forkcloser/xz"
)

func main() {
    const text = "The quick brown fox jumps over the lazy dog.\n"
    var buf bytes.Buffer
    // compress text
    w, err := xz.NewWriter(&buf)
    if err != nil {
        log.Fatalf("xz.NewWriter error %s", err)
    }
    if _, err := io.WriteString(w, text); err != nil {
        log.Fatalf("WriteString error %s", err)
    }
    if err := w.Close(); err != nil {
        log.Fatalf("w.Close error %s", err)
    }
    // decompress buffer and write output to stdout
    r, err := xz.NewReader(&buf)
    if err != nil {
        log.Fatalf("NewReader error %s", err)
    }
    if _, err = io.Copy(os.Stdout, r); err != nil {
        log.Fatalf("io.Copy error %s", err)
    }
}
```

## Documentation

You can find the full documentation at [pkg.go.dev](https://pkg.go.dev/github.com/forkcloser/xz).

## Using the gxz compression tool

The package includes a gxz command line utility for compression and
decompression.

Use following command for installation:

    $ go install github.com/forkcloser/xz/cmd/gxz@latest

To test it call the following command.

    $ gxz bigfile

After some time a much smaller file bigfile.xz will replace bigfile.
To decompress it use the following command.

    $ gxz -d bigfile.xz

## Security & Vulnerabilities

The security policy is documented in [SECURITY.md](SECURITY.md). 

The software is not affected by the supply chain attack on the original xz
implementation, [CVE-2024-3094](https://nvd.nist.gov/vuln/detail/CVE-2024-3094).
This implementation doesn't share any files with the original xz implementation
and no patches or pull requests are accepted without a review.

All security advisories for this project are published under
[github.com/forkcloser/xz/security/advisories](https://github.com/forkcloser/xz/security/advisories?state=published).

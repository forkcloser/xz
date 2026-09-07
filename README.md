# Package xz

This Go language package supports the reading and writing of xz
compressed streams. It includes also a gxz command for compressing and
decompressing data. The package is completely written in Go and doesn't
have any dependency on any C code.

Compression speed and ratio do not match the xz tool, whose algorithms
have been tuned over a long time. Decompression is a different story: see
the numbers below, and `ParallelReader` for block-parallel decoding of
multi-block archives.

## Stability

From v1.0.0 the exported API of `xz` and `lzma` follows the Go 1
compatibility promise within the v1 line: no exported name changes
meaning or disappears, and code that compiles against v1.0.0 keeps
compiling. Additive changes (new functions, new fields with zero-value
defaults) can land in minor releases. `internal/` is not covered, and
neither is the exact text of error messages — match errors with
`errors.Is` against `ErrCorrupt`, `ErrUnsupported`, `ErrClosed`,
`io.ErrUnexpectedEOF` and the `lzma` package's `ErrCorrupt` and
`ErrUnsupported`, not by string. The minimum Go version is the one in
`go.mod`; CI runs the current release.

## About this fork

This here is a friendly fork of https://github.com/ulikunitz/xz, taken at
upstream v0.5.15; v0.5.16's `IsTerminal` fallback for platforms without
terminal detection is ported. [`CHANGELOG.md`](./CHANGELOG.md) lists what a
user migrating from upstream will notice.

Upstream is not dormant — but its `master` is. Development moved to the `v2`
branch, which is a *different module*: `github.com/ulikunitz/xz/v2`, in a
`v2/` subdirectory, built on the separate `github.com/ulikunitz/lz` match
finder, replacing the v0.5 API with configuration structs, presets, and
JSON-marshallable reader and writer configs. It carries tags `v2.0.0-dev.1`
through `v2.0.0-dev.4` (October–December 2025) and has run 48 commits past
the last of them; `v0.6.0-last-dev` marks where the v0.x line stopped.
Because v2 takes the `/v2` import path, it does not supersede
`github.com/ulikunitz/xz` for anyone importing that today — and that is the
line this fork continues. `master` still takes occasional maintenance: its
last commit is 2026-07-20 and the `v2` branch was last pushed 2026-08-02,
both checked 2026-09-07. If you would rather carry these changes upstream
than depend on a fork, they apply to `master`; please do.

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
`ErrCorrupt` — container and LZMA2 payload alike, the `lzma` package
having its own `ErrCorrupt` that the xz reader maps — unsupported-but-valid
features match `ErrUnsupported`, and I/O errors from the underlying reader
pass through untouched, so callers can tell a corrupt file from a failed
transport. A truncated file is reported as such instead of decoding as a
shorter one, and a writer flush failure surfaces instead of silently
producing a short stream, as upstream v0.5.16 does for small-dictionary
configurations — configurations this fork briefly rejected and now
encodes correctly. After a write error the writers report that error
from then on rather than continue on unknown state (upstream's LZMA2
writer panics on the next `Write`).

**Verification.** In-tree, the decoder is checked against the `xz`
tool across check types and encoder configurations, and unit tests
cover payload shapes, multi-stream files and dictionary-growth
boundaries. Fuzz targets require the serial and parallel readers to
agree and round trips through the writer to be exact. Malformed-input
tests cover truncation and byte flips at every offset — every failure
must match `ErrCorrupt` or `io.ErrUnexpectedEOF` — and a corpus of
hostile index constructions with an allocation budget. A differential
test against upstream (`internal/upstreamdiff`, its own module so the
library does not depend on upstream; `just test-upstreamdiff`) exchanges
streams in both directions across configurations and requires the two to
agree on every corruption verdict except where this fork is deliberately
stricter. [`PERF.md`](./PERF.md) keeps the performance measurements and
the negative results that were measured and rejected.

### Benchmarks

Measured on `testdata/enwik7` (10 MB of Wikipedia text), Apple M5 Pro,
Go 1.26, 2026-09-06. Upstream is `github.com/ulikunitz/xz` v0.5.16 on
the same benchmark bodies. Reproduce with:

    go test -run '^$' -bench 'Reader|Writer' -benchmem -benchtime=5x -count=6 .

| Benchmark           | Upstream v0.5.16 | This fork | Change |
| ------------------- | ---------------- | --------- | ------ |
| Reader (decompress) | 48.6 MB/s        | ~102 MB/s | 2.1×   |
| Reader allocs/op    | 1,213,036        | 132       | −99.99% |
| Writer (compress)   | 14.1 MB/s        | ~15 MB/s  | +9%    |
| Writer allocs/op    | 1,217,288        | 306       | −99.97% |

[`PERF.md`](./PERF.md) has the methodology, what each change bought, the
negative results, and what is left.

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

`gxz -V` prints the module version it was built from. Binaries are not
published as release assets; `go install` is the supported way to get one.

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

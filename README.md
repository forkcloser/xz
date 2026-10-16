# Package xz

This Go language package supports the reading and writing of xz
compressed streams. The package is completely written in Go and doesn't
have any dependency on any C code. The command-line tool built on it is
[forkcloser/compress](https://github.com/forkcloser/compress)'s `xz`.

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
upstream v0.5.15. What a user migrating from upstream will notice, every fix
and feature this fork has that upstream does not, is the table in
[`UPSTREAM.md`](./UPSTREAM.md), beside the fork point and what was taken from
upstream since; the [releases](https://github.com/forkcloser/xz/releases)
carry the notes.

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

## Security & Vulnerabilities

The security policy is documented in [SECURITY.md](SECURITY.md).

The software is not affected by the supply chain attack on the original xz
implementation, [CVE-2024-3094](https://www.cve.org/CVERecord?id=CVE-2024-3094).
This implementation doesn't share any files with the original xz implementation
and no patches or pull requests are accepted without a review.

All security advisories for this project are published under
[github.com/forkcloser/xz/security/advisories](https://github.com/forkcloser/xz/security/advisories?state=published).

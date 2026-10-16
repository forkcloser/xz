# Upstream

Fork of <https://github.com/ulikunitz/xz> (`master`).

- Forked from: [`b371040`](https://github.com/ulikunitz/xz/commit/b3710402543262b807b5bb3370b310aae8efa2c0), committed 2025-08-31
- Reviewed through: [`6ead826`](https://github.com/ulikunitz/xz/commit/6ead826b4d3c7c9856f2daa905cf06403b9daddc), reviewed 2026-10-05

Every upstream commit after the fork point, through the reviewed one, is in
exactly one of the tables below. A port is a commit of this repository,
adapted to the fork; nothing is merged from upstream.

## Incorporated

| Upstream commit | Here | What |
| --- | --- | --- |
| [`82b346c`](https://github.com/ulikunitz/xz/commit/82b346cd8be59c38d9c898f43eb1cf9021e1a39f) | [`2514bf4`](https://github.com/forkcloser/xz/commit/2514bf4) | `internal/term` falls back to "not a terminal" on unsupported systems |
| [`591e09e`](https://github.com/ulikunitz/xz/commit/591e09e51d9cd7707ca2d3cd126ae7e6b68e5b99) | [`4c88e4f`](https://github.com/forkcloser/xz/commit/4c88e4f) | `lzma.ByteReader` retries `(0, nil)` reads |

## Not incorporated

| Upstream commit | Why |
| --- | --- |
| [`ba40d80`](https://github.com/ulikunitz/xz/commit/ba40d80159752f0a291aca9e79cea490daf24b5e) | upstream's v0.5.16 release bookkeeping (version strings, relnotes, TODO.md) |
| [`024f909`](https://github.com/ulikunitz/xz/commit/024f9092972afea64fa5c25157566881bac36938) | `+build` lines for the fallback; this fork uses `go:build` only, on its own narrower platform set |
| [`f5969eb`](https://github.com/ulikunitz/xz/commit/f5969eb5ae921ec9343a77d1ea9cc9812302b0af) | merge commit (of `591e09e`) |
| [`6ead826`](https://github.com/ulikunitz/xz/commit/6ead826b4d3c7c9856f2daa905cf06403b9daddc) | upstream's v0.5.17 release bookkeeping |

## Changes upstream does not have

| Change | Here |
| --- | --- |
| `ParallelReader`: block-concurrent decoding over an `io.ReaderAt`, with `Size`, `io.WriterTo`, cancellation through `Close` and goroutine cleanup when abandoned | [`2a68166`](https://github.com/forkcloser/xz/commit/2a68166), [`e8ca46a`](https://github.com/forkcloser/xz/commit/e8ca46a), [`e1d38d2`](https://github.com/forkcloser/xz/commit/e1d38d2), https://github.com/forkcloser/xz/pull/127 |
| Decoding about twice as fast as upstream, allocating per stream rather than per operation (`PERF.md`) | [`e3a30e8`](https://github.com/forkcloser/xz/commit/e3a30e8), [`9940ee5`](https://github.com/forkcloser/xz/commit/9940ee5), https://github.com/forkcloser/xz/pull/125 |
| Error classification: `xz.ErrCorrupt`, `ErrUnsupported`, `ErrClosed` and `lzma.ErrCorrupt`, `ErrUnsupported`; every invalid input matches the corrupt sentinel, I/O errors pass through, a stream cut short is `io.ErrUnexpectedEOF` where upstream decoded a file cut after its last block as complete | https://github.com/forkcloser/xz/pull/8 |
| Hostile index contents are bounded before any allocation or loop bound; decode buffers grow with the output produced; the decoder dictionary grows on demand | [`e999b1a`](https://github.com/forkcloser/xz/commit/e999b1a), [`b31d853`](https://github.com/forkcloser/xz/commit/b31d853), [`ae1481b`](https://github.com/forkcloser/xz/commit/ae1481b), [`50b4f38`](https://github.com/forkcloser/xz/commit/50b4f38), [`9fbd72d`](https://github.com/forkcloser/xz/commit/9fbd72d) |
| `Writer`, `lzma.Writer` and `lzma.Writer2` keep their first error and return it from every later call (upstream's `Writer2` panicked on the `Write` after a failed flush); `Writer2.Close` reports a failed last flush; LZMA2 with a dictionary smaller than a chunk falls back to a compressed chunk | https://github.com/forkcloser/xz/pull/8, [`2a92eca`](https://github.com/forkcloser/xz/commit/2a92eca) |
| `WriterConfig.NoCheckSum`; `lzma.Reader2.Reset` and `DictCap`; `lzma.NewReader` buffers a plain `io.Reader` | https://github.com/forkcloser/xz/releases/tag/v1.0.0 |
| Removed: the `xio` package, `lzma.BinaryTree` (it emitted match distances the decoder rejects), `cmd/xb`, and later the `gxz` command, which [compress](https://github.com/forkcloser/compress)'s `xz` replaces | [`c7fe3db`](https://github.com/forkcloser/xz/commit/c7fe3db), https://github.com/forkcloser/xz/pull/8, https://github.com/forkcloser/xz/pull/119 |
| Terminal detection on linux, darwin, FreeBSD and Windows only | https://github.com/forkcloser/xz/pull/23 |
| The LZMA and LZMA2 streams' edges hold the package's contract: seven fixes at chunk ends, cut streams, `Reset`, zero uncompressed size, `Close` twice | https://github.com/forkcloser/xz/pull/120 |
| The LZMA2 decoder refuses what liblzma refuses | https://github.com/forkcloser/xz/pull/121 |
| A complete file that is not a valid `.xz` file is corrupt, in both readers | https://github.com/forkcloser/xz/pull/123 |
| A block header is read into a buffer its reader owns | https://github.com/forkcloser/xz/pull/126 |

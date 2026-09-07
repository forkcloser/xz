# Changelog

All notable changes to this fork are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
semantic versioning against the stability contract in `README.md`.

Upstream history before the fork is in `TODO.md` (the *Log* section) and in
upstream's own `doc/relnotes`. The entries below are what a user of
`github.com/ulikunitz/xz` v0.5.16 will notice when switching the import
path.

## [Unreleased]

### Added

- `ParallelReader`: block-concurrent decoding of multi-block files over an
  `io.ReaderAt`, with `Size()`, `io.WriterTo`, cancellation through `Close`
  from another goroutine, and goroutine cleanup when a reader is abandoned.
- Error classification. `xz.ErrCorrupt`, `xz.ErrUnsupported` and
  `xz.ErrClosed`, and in the `lzma` package `lzma.ErrCorrupt` and
  `lzma.ErrUnsupported`: every "this is not valid input" error matches the
  corrupt sentinel, container and LZMA2 payload alike, so `errors.Is`
  replaces matching on message text. I/O errors from the underlying reader
  pass through unchanged; a stream cut short is `io.ErrUnexpectedEOF`.
- `WriterConfig.NoCheckSum` to write a stream with no integrity check
  (`CheckSum` cannot select `None`, whose value is zero).
- `lzma.Reader2.Reset` and `lzma.Reader2.DictCap`, so one LZMA2 reader can
  decode a sequence of chunk sequences without reallocating.
- `lzma.NewReader` accepts a plain `io.Reader` efficiently by buffering it;
  an `io.ByteReader` is used as is so its position stays exact.
- A differential test against upstream (`internal/upstreamdiff`, its own
  module) and fuzz targets (`FuzzReader`, `FuzzParallelReader`,
  `FuzzReadersAgree`, `FuzzRoundTrip`, `FuzzSingleStreamReader`).
- `internal/term` builds on every platform; `IsTerminal` reports false where
  there is no terminal detection (ported from upstream v0.5.16).

### Changed

- Decoding is about twice as fast as upstream and allocates per stream
  rather than per operation (132 allocations for a 10 MB decode against
  1.2 million); see `PERF.md`.
- The decoder dictionary grows on demand up to the declared capacity
  instead of being allocated at the declared size up front.
- A truncated file is reported as `io.ErrUnexpectedEOF`. Upstream decoded a
  file cut exactly after its last block as a complete one.
- Hostile index contents are bounded: record counts, block sizes and
  uncompressed sizes that cannot fit the stream are rejected before they
  reach an allocation or a loop bound, and decode buffers grow with the
  output actually produced rather than with what the index declares.
- `Writer`, `lzma.Writer` and `lzma.Writer2` remember the first error from
  the underlying writer and return it from every later `Write`, `Flush` and
  `Close` instead of continuing on unknown state (upstream's `Writer2`
  panics with "maxUncompressed reached" on the `Write` after a failed
  flush).
- `lzma.Writer2.Close` reports a failure to flush the last chunk instead of
  returning nil over a short stream.
- LZMA2 writing with a dictionary smaller than a chunk falls back to a
  compressed chunk instead of failing (`lzma: can't write empty uncompressed
  chunk`, upstream) or silently truncating.
- `lzma.Reader2` reports a compressed chunk whose data ends before its
  declared size as corruption rather than as unexpected EOF: the chunk is
  read whole before decoding, so the input did not end.
- `ReaderConfig.SingleStream` passes an I/O error met after the stream
  through unchanged instead of reporting trailing data.
- The classic `lzma.Reader` rejects streams declaring more than a pebibyte
  with an error matching `lzma.ErrUnsupported`, and a header cut short with
  `io.ErrUnexpectedEOF`.
- `gxz -V` reports the module version from the build information; the
  generated version constant, which had stopped tracking anything, is gone.
- `gxz`'s usage text says what it does by default: xz, not `.lzma`.
- Copyright headers name this fork on the files it authored, and `LICENSE`
  carries the fork's line alongside upstream's; the licence terms are
  unchanged (BSD-3-Clause).

### Removed

- `lzma.BinaryTree`, the alternative match finder. It never worked: it
  emitted match distances the decoder does not have (its own reader
  rejected its output for zeros and for ordinary text), compressed text to
  half its size where `HashTable4` reaches a fraction of a percent, and was
  quadratic on repeated words. Upstream's roadmap still lists "fix binary
  tree matcher". `lzma.HashTable4` is the only `MatchAlgorithm` and its
  zero value; a working alternative can be added as a new value.
- `cmd/xb`, upstream's build helper (`version-file`, `cat`, `copyright`);
  nothing in this fork's build uses it.
- `lzma.LimitedByteWriter` and `lzma.ErrLimit`, an implementation detail of
  the LZMA2 chunk writer that never reached a caller; they are unexported.
- The pandoc `make-docs` scripts and `doc/md.css`; the Markdown is the
  documentation.
- Unreachable functions in `internal/gflag` and `internal/xlog`. Both are
  internal; nothing outside the module could use them.

### Fixed

- `ParallelReader` returned a truncated result with no error when a block
  header claimed more bytes than its block holds.
- `ParallelReader` matched neither `ErrCorrupt` nor anything else for an
  index indicator found where the index placed a block, for an index whose
  record count ran past its size, and for two size-bound failures in the
  index walk.
- A hostile index could drive `parseBlocks` into an infinite loop, a 40-byte
  file could reserve terabytes, and a worker goroutine could panic the
  process; all three are rejected with errors.
- `xz.ParallelReader` and `xz.Reader` produce the same bytes for every input
  both accept (fuzzed).

[Unreleased]: https://github.com/forkcloser/xz/compare/b371040...HEAD

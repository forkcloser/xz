# Performance

What this fork changed for speed, what it measured, and what is left. The
git history has the patches; this is the reference for the numbers, the
methodology, and the negative results that should not be retried blindly.

## Methodology

- Corpus: `testdata/enwik7` (10 MB of English Wikipedia text). Text is a
  worst case for match copying and a fair case for the range coder; the
  repetitive-data and multi-block results below use their own corpora, named
  where they appear.
- Benchmarks: `BenchmarkReader` (decompress) and `BenchmarkWriter` (compress)
  in the top-level package, driving the full xz stream end to end.
- Units: MB/s is decimal, as `go test -bench` reports it. Sizes are as
  reported (`B/op`).
- Machine: Apple M5 Pro (18 cores), darwin/arm64. Go 1.26.
- Every claimed delta is `benchstat` over `-benchtime=5x -count=6` or more;
  a "within noise" verdict means p > 0.05.

Reproduce:

```
go test -run '^$' -bench 'BenchmarkWriter$|BenchmarkReader$' \
    -benchmem -benchtime=5x -count=6 .
```

## Current state

Measured at `8cb36d8` (2026-09-06), n=6. Upstream is `github.com/ulikunitz/xz`
v0.5.16 on the same benchmark bodies.

| Benchmark           | Upstream          | This fork        | Change   |
| ------------------- | ----------------- | ---------------- | -------- |
| Reader (decompress) | 48.6 MB/s         | ~102 MB/s        | 2.1×     |
| Reader allocs/op    | 1,213,036         | 132              | −99.99%  |
| Writer (compress)   | 14.1 MB/s         | ~15 MB/s         | +9%      |
| Writer allocs/op    | 1,217,288         | 306              | −99.97%  |

The writer is noisy on this machine (13.9–18.6 MB/s across six runs under
moderate load); treat its throughput as a range and its allocation count as
the reliable figure. Compression ratio is 0.3357, identical to upstream, and
decoded output is byte-identical (sha256 on enwik7).

Beyond these two benchmarks:

- Repetitive data (RLE-heavy 10 MB corpus: zero runs, short patterns):
  decode 333 → 1,461 MB/s (4.4×), from the dictionary match-copy fast path.
- Multi-block files: `ParallelReader` decodes a 470-block, 1.65 GiB
  `llvm.tar.xz` in 7.4 s against 79.7 s serial (10.7× on 18 workers), output
  hash identical to `xz -dc`.
- `NewReader(*os.File)` on the classic LZMA1 format: 7 → 84 MB/s (11.6×),
  from buffering a plain reader that was previously pulled one byte per
  system call.

## What changed, and what each step bought

Reader throughput on enwik7 after each step, measured mid-series on the same
machine (the final row is within noise of the current figure above):

| Step | Change | Reader MB/s |
| ---- | ------ | ----------- |
| 0 | upstream baseline | 48.6 |
| 1 | `lzma.operation` as a value type instead of an interface: no per-operation boxing (`lzma/operation.go`, `decoder.go`, `encoder.go`, `hashtable.go`, `bintree.go`). Also the writer's allocation win. | 52.4 |
| 2 | Buffer each LZMA2 compressed chunk (≤ 64 KiB, size known) and serve the range decoder from a `bytes.Reader` instead of a one-byte `Read` per input byte (`lzma/reader2.go`). | 57.1 |
| 3 | Branchless bit decode: the `code < bound` comparison is unpredictable by construction, so the range/code/probability updates select by mask (`lzma/rangecodec.go`). | 66.8 |
| 4 | Hoist range/code into locals and inline the direct-bit decode in `directCodec.Decode`; direct bits have no probability model, so call overhead was the whole cost (`lzma/directcodec.go`). | 74.9 |
| 5 | Split the bit decode into an inlinable arithmetic half (`decodeBitArith`, by value, no I/O) and an out-of-line renormalization; the multi-bit codecs commit range/code once per symbol instead of once per bit (`rangecodec.go`, `treecodecs.go`, `literalcodec.go`). | 90.5 |
| 6 | Range decoder reads from a `[]byte` + index; input failures become a sticky error checked once per operation, so the per-bit loops carry no call and no error branch (`decoder.go`, `reader2.go`, the codecs). | 96.2 |
| 7 | Thread range/code through the whole operation: every codec `decode` takes and returns them, state committed once per `readOp`; dead error plumbing removed (`decoder.go`, `lengthcodec.go`, `distcodec.go`, `prob.go`, the codecs). | 103.2 |
| 8 | Direct dictionary match copy with pattern doubling for overlapping matches, wrap-around kept on the old segment loop (`lzma/decoderdict.go`). Flat on enwik7; the 4.4× repetitive-data result above. | 103.4 |
| 9 | Hand-inline the six per-operation `isMatch`/`isRep*` bits in `readOp`. The same on `lengthCodec`'s two choice bits measured nothing and was reverted. | 106.3 |
| 10 | `ParallelReader`: parse the index backwards, decode independent blocks concurrently, deliver in order, bounded to `Workers+2` blocks in flight (`parallelreader.go`, `format.go`). | — |
| 11 | Reuse coder state across LZMA2 property resets instead of rebuilding ~145 probability trees per resetting chunk (`lzma/state.go`, the codecs): 385 → 18.6 MiB allocated on a 20 000-chunk stream, 30% less time. Invisible on enwik7. | — |
| 12 | Wrap a plain `io.Reader` in `bufio` inside the LZMA1 `NewReader`; an input that is already an `io.ByteReader` is left alone so its position stays exact. | — |

Steps 1–9 are byte-for-byte output-preserving and were each verified by the
full suite and a sha256 round-trip; step 8 additionally by a differential
fuzz against the old algorithm (2000 trials × 200 random steps); step 11 by
`TestStateResetReusesWithoutDrift`, which fails if any reset line is removed.

## Negative results

Recorded so the ideas are not retried blindly, and because they located the
real cost.

- **A. Error-free, inlinable `DecodeBit`** (reverted). Dropped the `error`
  return through the decode chain so the compiler could inline the bit
  decode. No change: the inline cost only fell 132 → 129 against a budget of
  80, and the input read it cheapened was already under 1% of the profile.
- **B. Hoist range/code into registers** (reverted). Prototyped on the
  literal codec. No change (p=0.49) — because the literal codec is ~3% of
  decode. The idea was right and mis-aimed; step 5 is the same idea applied
  where the volume is.
- **C. Preload both tree children and select by mask** (reverted). Meant to
  take the probability load off the serial bit-to-bit chain. Clearly worse,
  99 → 113 ms: the extra loads, their bounds checks and the select cost more
  than the latency they hid.
- **D. Remove the per-byte modulo in the rolling hashes** (kept, no gain).
  `(r.i + 1) % cap(r.p)` is a hardware divide per input byte; an increment and
  compare is equivalent. p=0.74: the writer is stalled on memory, so the
  divide was already hidden. Kept as strictly less work.

The lesson from A and B: order matters. A's idea, infallible reads and no
per-bit error plumbing, is exactly steps 6 and 7 — and it measured zero when
tried first, because `DecodeBit` was still one non-inlinable lump. Once the
arithmetic was inline (step 5), the residual boundary overhead A targeted was
what remained. A correct idea can measure as nothing against the wrong
backdrop.

## Where the writer's time goes

The reader techniques do not transfer. A writer profile on enwik7 puts about
75% in the match finder (`hashTable.getMatches` 30%, `putDelta` 26%,
`putEntry` 9%, `NextOp` 9%) and only ~15% in the whole range encoder, so
hoisting and inlining have at most a 15% target. The cost is cache, not
structure: with the default 8 MiB dictionary the match finder works over two
tables of roughly 32 MB each and touches one of them at a hash-derived,
effectively random index for every input byte.

Throughput against dictionary size, same corpus, same code:

| DictCap         | Throughput | Ratio  |
| --------------- | ---------- | ------ |
| 64 KiB          | 24.8 MB/s  | 0.3557 |
| 256 KiB         | 25.5 MB/s  | 0.3444 |
| 1 MiB           | 23.0 MB/s  | 0.3383 |
| 4 MiB           | 18.2 MB/s  | 0.3361 |
| 8 MiB (default) | 17.0 MB/s  | 0.3357 |

The last 2.5% of ratio costs a third of the throughput. The levers are the
default dictionary size and the hash-table entry (an `int64` absolute position
where a narrower relative one would halve the footprint). Both change the
compressed output, so they are policy decisions, not optimizations.

## Remaining bottlenecks

Decode profile after step 9, share of decode time:

- **Range-coder arithmetic, ~75%.** All inline, call-free, branch-lean and
  register-resident. What remains is the serial bit-to-bit dependency and the
  per-bit probability read-modify-write; attempt C showed that hiding the
  model load costs more than it saves. In pure Go this is the floor. The one
  lever of any size left is hand-written assembly for the bit loops, which is
  a maintenance decision more than an engineering one.
- **Match copy, ~12%.** Almost entirely the inherent `memmove` of decoded
  bytes after step 8.
- **Dict-to-caller copy.** Every decoded byte is copied out of the circular
  dictionary to the caller, and touched again by the xz wrapper. Letting
  callers read from the window is an API change with a modest,
  bandwidth-bound upside.
- **Parallelism.** Chunks within one LZMA2 block share dictionary state, so
  single-block decode is inherently serial. `ParallelReader` covers
  multi-block files only.
- **Writer.** See above: the working set of the match finder, and pulling that
  lever changes the output.

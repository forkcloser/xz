# Performance

Tracking of performance work on this fork.

## Methodology

- Corpus: `testdata/enwik7` (10 MB of English Wikipedia text).
- Benchmarks: `BenchmarkWriter` (compress) and `BenchmarkReader` (decompress)
  in the top-level package, driving the full xz stream end to end.
- Each result is `benchstat` over `-benchtime=5x -count=6`.
- Machine: Apple M5 Pro (18 cores), macOS (darwin/arm64), Go 1.26.

Reproduce:

```
go test -run '^$' -bench 'BenchmarkWriter$|BenchmarkReader$' \
    -benchmem -benchtime=5x -count=6 .
```

## Baseline (original, unmodified)

Measured on the unmodified upstream code.

| Benchmark          | Throughput   | Time/op   | Bytes/op  | Allocs/op   |
| ------------------ | ------------ | --------- | --------- | ----------- |
| Reader (decompress) | 48.63 MiB/s | 196.1 ms  | 31.94 MiB | 1,213,036   |
| Writer (compress)   | 14.09 MiB/s | 678.3 ms  | 69.35 MiB | 1,217,288   |

Compression ratio (Writer): 0.3357.

The standout signal is ~1.2 million heap allocations to process 10 MB on
both paths — roughly one allocation per operation.

## Patch 1 — `operation` as a value type

**Change.** `lzma.operation` was an interface with two value implementations
(`lit`, `match`). Because the decode and encode hot loops returned the
interface, every decoded/encoded operation boxed a value onto the heap. The
interface is replaced with a single value struct plus `litOp`/`matchOp`
constructors, and the consumers (`decoder`, `encoder`, `hashTable`, `binTree`)
pass it by value. This removes essentially all per-operation allocation.

Files: `lzma/operation.go`, `lzma/decoder.go`, `lzma/encoder.go`,
`lzma/hashtable.go`, `lzma/bintree.go`.

Behavior is unchanged: all tests pass and compressed output is byte-identical
(compression ratio still 0.3357).

**Results.**

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 52.38 MiB/s (+7.7%)    | 182.1 ms (-7.1%)   | 13.44 MiB (-57.9%)  | 438 (-99.96%)            |
| Writer (compress)   | 17.60 MiB/s (+24.9%)   | 542.0 ms (-20.1%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

Geomean throughput: +16%. All deltas significant (p=0.002, n=6).

The allocation reduction (~99.9%) removes per-operation GC pressure, which
matters most under concurrency and in memory-constrained environments, and
also yields a real single-stream speedup — largest on the writer.

## Patch 2 — buffer each LZMA2 compressed chunk in memory

**Change.** In the LZMA2 reader the range decoder read its input through
`ByteReader(io.LimitReader(...))`. Because `io.LimitReader` is not an
`io.ByteReader`, it was wrapped in the streaming `breader`, which performs a
one-byte `Read` (plus a `memmove`) through an interface for *every* byte the
range decoder consumes.

Each LZMA2 compressed chunk has a known size (the 16-bit size field, at most
64 KiB), so `Reader2.startChunk` now reads the whole chunk into a reusable
buffer with a single `io.ReadFull` and serves the range decoder from a reused
`bytes.Reader`, whose `ReadByte` is a plain index increment — no per-byte
`Read`, no `memmove`. `io.ReadFull` advances the underlying reader by exactly
the chunk length (`header.compressed + 1`, since the size field stores
length − 1), so the next chunk header stays aligned. The range decoder itself
is untouched.

Files: `lzma/reader2.go`. This patch only touches the reader; the writer
rows below reflect Patch 1 and move only within run-to-run noise.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 57.10 MiB/s (+17.4%)   | 167.0 ms (-14.8%)  | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 16.98 MiB/s (+20.5%)   | 562.4 ms (-17.1%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

Reader delta significant (p=0.002, n=6).

## Patch 3 — branchless range-decoder bit decode

**Change.** After Patches 1 and 2, CPU profiles showed reader time dominated
by the range coder: `rangeDecoder.DecodeBit` alone was ~48%, with the bit-tree
codecs that call it next (the input read was down to ~0.8%). `DecodeBit`
selected the decoded bit with `if d.code < bound { ... } else { ... }`. That
comparison is data-dependent and essentially unpredictable — it is decoding
near-random compressed bits — so the branch mispredicts roughly half the time.

`DecodeBit` now derives a full-width mask from the comparison and selects the
range, code and probability updates with mask arithmetic instead of branching.
The bare `code >= bound` flag compiles to a conditional set (no branch); only
the *predictable* normalization branch (taken ~once per eight bits) remains.
The decoded output is identical.

Files: `lzma/rangecodec.go`. Reader-only.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 66.84 MiB/s (+37.4%)   | 142.7 ms (-27.2%)  | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 17.67 MiB/s (+25.4%)   | 540.0 ms (-20.4%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

Reader delta significant (p=0.001, n=6). Branchless `DecodeBit` alone added
~+19% reader throughput on top of Patches 1+2. The writer is untouched by this
patch; its row reflects Patch 1 and moves only within run-to-run noise.

## Patch 4 — hoist and inline the direct-bit decode

**Change.** With `DecodeBit` fixed, the profile promoted `DirectDecodeBit` to
the #2 reader cost (~12% flat), called in a tight loop by `directCodec.Decode`
to decode the high bits of large match distances. Direct bits are equiprobable
— *there is no probability model to touch* — so unlike the tree/literal codecs
(see Attempt B below) the per-bit cost is purely call overhead and reloading
range/code from the struct.

`directCodec.Decode` now hoists `range`/`code` into locals and inlines the
direct-bit decode (and its byte read) into the loop, writing the state back
once. `DirectDecodeBit` had no other callers and was removed.

Files: `lzma/directcodec.go`, `lzma/rangecodec.go`. Reader-only.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 74.90 MiB/s (+54.0%)   | 127.3 ms (-35.1%)  | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

Reader delta significant (p=0.001, n=6). This patch alone added ~+12% reader
throughput on top of Patch 3. Writer untouched.

## Patch 5 — inline the bit decode and hoist range/code across whole symbols

**Change.** This is the big one, and it contradicts what the earlier notes
(and Attempt B below) concluded. A line-level profile of `DecodeBit` after
Patch 4 showed that **~60% of its time was the function boundary, not the
range arithmetic**: 240 ms on the normalization check `if d.nrange >= top`
(where the just-computed `d.nrange`/`d.code` are committed to the struct and
reloaded), 90 ms on the `return`, 40 ms on the prologue. Because `DecodeBit`
is a separate method operating through `d *rangeDecoder`, the decoder state
`d.nrange`/`d.code` is stored to memory and reloaded on *every single bit*.

Attempt B ("hoist range/code into registers") had tried exactly this and found
nothing — but it was prototyped on the **literal codec**, which this profile
shows is only ~3% of decode. The volume is in `treeCodec` (via `distCodec`'s
position slots) and `lengthCodec`. Attempt B tested the right idea on the one
codec where it could not show.

The fix:

- `decodeBitArith` does the *arithmetic half* of a bit decode (bit select +
  probability update) taking and returning `range`/`code` **by value**, with
  no I/O and no struct access. It is small enough to inline (cost 72).
- `readNorm` does the once-per-eight-bits renormalization (the byte read),
  kept out of line so `decodeBitArith` stays inlinable.
- The multi-bit codecs (`treeCodec`, `treeReverseCodec`, `literalCodec`, and
  `DecodeBit` itself) now hoist `range`/`code` into locals, inline
  `decodeBitArith` per bit, and commit the state to the struct **once** per
  symbol instead of once per bit.

Files: `lzma/rangecodec.go`, `lzma/treecodecs.go`, `lzma/literalcodec.go`.
Reader-only; decoded output byte-identical; full suite passes.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 90.50 MiB/s (+86.1%)   | 105.4 ms (-46.3%)  | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

This patch alone added **+23% reader throughput** on top of Patch 4
(73.4 → 90.5 MiB/s, p=0.000, n=8). Writer untouched. The earlier claim that
the reader was "near the practical pure-Go floor" after Patch 4 was wrong: the
floor was a measurement of the function-call structure, not the algorithm.

## Patch 6 — read input from a byte slice; sticky read errors

**Change.** After Patch 5 every remaining per-bit decode loop still contained
a call and an error branch: the once-per-eight-bits renormalization went
through `readNorm` → `io.ByteReader.ReadByte` (interface dispatch into
`bytes.Reader`), and every renorm could fail, so every loop carried an
`if err != nil` path. A call anywhere in a loop body also forces the register
allocator to keep the hoisted `range`/`code`/tree state in callee-saved
registers or spill them.

The LZMA2 path has buffered the whole compressed chunk since Patch 2, so the
range decoder now reads its input directly from a `[]byte` + index. Input
failures are no longer reported per byte: the decoder records a **sticky
error** (`rangeDecoder.err`) and further reads return zero bytes; `decompress`
checks the sticky error once per decoded operation. Decoding a few garbage
zero bytes until that check is harmless — every symbol decode terminates after
a bounded number of bits — and truncated-input behavior is unchanged
(`io.ErrUnexpectedEOF`, verified by test).

With that, renormalization becomes ~3 inline instructions. The fast path is
hand-inlined at the decode sites because the cold-path call alone costs 57 of
the 80-point inline budget (measured: every `readByte` shape came out 82–87),
and the loops are free of calls and error branches in the common path. The
LZMA1 streaming path (`lzma.Reader`) keeps working through the same cold
branch (`readByteSlow` falls back to the `io.ByteReader`).

Files: `lzma/rangecodec.go`, `lzma/treecodecs.go`, `lzma/literalcodec.go`,
`lzma/directcodec.go`, `lzma/decoder.go`, `lzma/reader2.go`. Reader-only;
decoded output byte-identical; full suite passes.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 96.15 MiB/s (+97.7%)   | 99.2 ms (-49.4%)   | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

This patch alone added +5.7% reader throughput on top of Patch 5
(p=0.000, n=8). Writer untouched.

## Patch 7 — thread range/code through the whole operation

**Change.** The remaining structural overhead was the codec boundaries inside
`readOp`: the `isMatch`/`isRep*` bits went through the out-of-line `DecodeBit`
(struct load + commit per bit), and each codec `Decode` call
(`lengthCodec` → `treeCodec`, `distCodec` → `treeCodec`/`directCodec`/
`treeReverseCodec`) reloaded `range`/`code` from the struct on entry and
committed them on exit — 6–10 round-trips through memory per operation.

`readOp` now hoists `range`/`code` once and threads them **through every
decode call in registers**: the codec `Decode(d)` methods became unexported
`decode(d, rng, code) (v, nrng, ncode)` variants, the isolated single bits use
a threaded `rangeDecoder.decodeBit`, and the state is committed to the struct
once per operation. The per-call error returns are gone too (sticky errors
from Patch 6 made them dead weight), which removes the remaining error checks
from the op decode path. `DecodeBit` and `prob.Decode` had no callers left and
were removed.

Files: `lzma/decoder.go`, `lzma/rangecodec.go`, `lzma/treecodecs.go`,
`lzma/literalcodec.go`, `lzma/lengthcodec.go`, `lzma/distcodec.go`,
`lzma/directcodec.go`, `lzma/prob.go`. Reader-only; decoded output
byte-identical (sha256-verified on enwik7); full suite passes.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 103.21 MiB/s (+112.2%) | 92.4 ms (-52.9%)   | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

This patch alone added +7.3% reader throughput on top of Patch 6
(96.15 → 103.21 MiB/s, p=0.000, n=8). Reader throughput has now more than
doubled vs the original baseline. Writer untouched.

## Patch 8 — direct dictionary match copy with pattern doubling

**Change.** `decoderDict.writeMatch` funneled every copy segment through
`buffer.Write`, which rechecks `Available()`, redoes the wrap logic and calls
`addIndex` per segment — after `writeMatch` itself had already validated
space. Worse, for overlapping matches (`dist < length`, i.e. repeating
patterns) the segment loop advanced in *dist-sized chunks*: a run with
`dist=1` and `length=273` performed 273 one-byte `buffer.Write` calls.

`writeMatch` now takes a fast path whenever neither the source run nor the
destination run crosses the physical end of the circular buffer (the common
case; wrap happens at most once per trip through the dictionary): it copies
directly on the underlying array, and replicates overlapping patterns by
doubling (copy the `dist`-sized pattern once, then repeatedly copy the
already-written prefix, so the copied chunk doubles each step). The wrapping
slow path keeps the original segment loop.

Correctness was verified with a differential fuzz test (2000 trials × 200
random literal/match/drain steps against the old algorithm, comparing full
buffer state) plus sha256 round-trips of enwik7 and an RLE-heavy corpus.

Files: `lzma/decoderdict.go`. Reader-only.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 103.35 MiB/s (+112.5%) | 92.3 ms (-52.9%)   | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

On enwik7 (English text, few overlapping matches) this is a wash vs Patch 7
(p=0.878) — but that is the wrong corpus for this patch. On an RLE-heavy
10 MB corpus (zero runs, short repeating patterns, byte runs; compressed with
this package's writer) decode throughput went from **333 MB/s to 1,461 MB/s
(4.4×)** measured old-vs-new `writeMatch` with everything else identical.
Repetitive data — long runs, sparse binaries, zeroed pages — is a common
decode workload, so the patch stays despite the flat enwik7 row.

## Patch 9 — hand-inline the per-operation bits in readOp

**Change.** After Patch 7 the `isMatch`/`isRep*` bits still went through the
(register-threaded, but out-of-line) `rangeDecoder.decodeBit` call — the last
call remaining on the per-bit path. The six `readOp` call sites now hand-inline
the same sequence the codec loops use: inlined `decodeBitArith` plus the
hand-inlined renormalization read.

The same treatment on `lengthCodec`'s two choice bits measured no change
(p=1.000; they are only ~2 bits per match) and was reverted — `lengthCodec`
still uses `decodeBit`.

Files: `lzma/decoder.go`. Reader-only.

**Results** (cumulative, vs baseline).

| Benchmark           | Throughput             | Time/op            | Bytes/op            | Allocs/op                |
| ------------------- | ---------------------- | ------------------ | ------------------- | ------------------------ |
| Reader (decompress) | 106.32 MiB/s (+118.6%) | 89.7 ms (-54.3%)   | 13.56 MiB (-57.6%)  | 284 (-99.98%)            |
| Writer (compress)   | 18.27 MiB/s (+29.7%)   | 522.1 ms (-23.0%)  | 50.85 MiB (-26.7%)  | 4,622 (-99.6%)           |

This patch alone added +2.8% reader throughput on top of Patch 8 (p=0.003,
n=8). Writer untouched.

## Patch 10 — parallel block decoding (ParallelReader)

**Change.** Not a hot-loop optimization but a new decode path. Multi-block xz
files — as produced by `xz -T`/`--block-size`, or by this package's writer
with `WriterConfig.BlockSize` — consist of independent blocks (separate
dictionaries, separate range-coder state, per-block check), and the stream
index at the end of each stream records every block's file offset and sizes.
Single-stream decode cannot exploit that; a new `ParallelReader` does:

- `NewParallelReader(r io.ReaderAt, size int64)` parses the streams backwards
  (footer → index → stream header, all checksums verified) without touching
  block data, then decodes blocks concurrently on `Workers` goroutines
  (default `GOMAXPROCS`) and delivers them in order through `io.Reader` /
  `io.WriterTo`.
- Each block is verified as in the streaming reader: block check (CRC32/
  CRC64/SHA256), header sizes, and consistency with the index record.
- Memory is bounded: at most `Workers+2` blocks in flight, with decode
  buffers recycled through a pool. Multi-stream files and stream padding are
  supported; a single-block file just decodes on one worker.

Files: `parallelreader.go`, `parallelreader_test.go` (multi-block,
multi-stream, single-block, empty, truncated/corrupt, Close, WriteTo — also
run under `-race`), plus a relaxation in `format.go` (`readIndexBody` may
skip the record-count check when the index is parsed before the blocks).

**Results.** Real-world archive (`llvm.tar.xz`: 470 blocks × 24 MiB, 1.65 GiB
compressed, 11.0 GiB uncompressed, CRC64, created by multi-threaded xz),
decoded to sha256 on the 18-core M5 Pro; output hash identical to `xz -dc`:

| Decoder                    | Time     | Throughput   | Speedup |
| -------------------------- | -------- | ------------ | ------- |
| Reader (serial, Patch 9)   | 79.7 s   | 141 MiB/s    | 1.0×    |
| ParallelReader (18 workers)| 7.4 s    | 1,514 MiB/s  | 10.7×   |

(For reference, `xz -dc -T0` piped to `shasum -a 256` took 22.7 s on the same
file, though that pipeline was bottlenecked by shasum itself.)

The caveat from the bottlenecks section stands: this only helps files that
contain multiple blocks. Single-block files (e.g. anything compressed by
plain single-threaded `xz` or this package's writer without a `BlockSize`)
still decode serially.

## Patch 11 — reuse the coder state across LZMA2 resets

**Change.** An LZMA2 chunk can reset the coder state, and a chunk carrying new
properties made `Reader2.startChunk` build a whole new `state`. That state is
not small: `state.Reset` assigned a fresh struct value, which dropped every
codec's probability array, so each of the ~145 trees in the length, repeat
length and distance codecs plus the literal codec reallocated and refilled.
A stream of small resetting chunks therefore cost far more in allocator
traffic than in decoding.

`probTree.init`, `literalCodec.init` and the tree codecs now reuse their
backing arrays when they are big enough, `state.Reset` assigns its scalar
fields individually instead of overwriting the struct, and the reader resets
in place rather than calling `newState`.

Files: `lzma/state.go`, `lzma/treecodecs.go`, `lzma/lengthcodec.go`,
`lzma/distcodec.go`, `lzma/literalcodec.go`, `lzma/reader2.go`. Reader-only;
decoded output byte-identical.

Because `Reset` no longer overwrites the whole struct, a field added later
without a matching reset line would silently survive a reset.
`TestStateResetReusesWithoutDrift` dirties every probability in the state,
resets, and requires deep equality with a freshly built state; it was
confirmed to fail when a single reset line is removed.

**Results.** Measured on a stream of 20 000 property-resetting chunks
(480 KB in, 41 MB out), decoded to `io.Discard`:

| | Allocated | Time |
| --- | --- | --- |
| Before | 385.3 MiB | 95 ms |
| After | 18.6 MiB | 67 ms |

20.7x less allocation and ~30% less time. `enwik7` is unaffected — it
contains almost no property resets — and both standard benchmarks moved
within noise (Reader p=0.38, Writer p=0.07, n=8), with allocation counts and
the compression ratio unchanged.

## Where the writer's time actually goes

`PERF.md` has said since Patch 1 that the writer has "far more structural
headroom than the reader". A profile says otherwise: the cost is not
structure, it is cache.

Writer profile on `enwik7` (share of total):

- `hashTable.getMatches` 30% flat, `putDelta` 26%, `putEntry` 9%, `NextOp` 9%
  — about 75% in the match finder.
- The whole range encoder — `EncodeBit`, `shiftLow`, the tree encoders —
  is about 15%.

So the hoist/thread/inline techniques that doubled the reader have at most a
15% target here, and the real cost is memory. With an 8 MiB dictionary the
match finder works over two tables of roughly 32 MB each (`t` is
`8 << exponent` bytes, `data` is `4 * dictCap`), and it touches `t` at a hash
-derived, effectively random index for every input byte.

Throughput against dictionary size, same corpus, same code:

| DictCap | Throughput | Ratio |
| --- | --- | --- |
| 64 KiB | 24.8 MiB/s | 0.3557 |
| 256 KiB | 25.5 MiB/s | 0.3444 |
| 1 MiB | 23.0 MiB/s | 0.3383 |
| 4 MiB | 18.2 MiB/s | 0.3361 |
| 8 MiB (default) | 17.0 MiB/s | 0.3357 |

The last 2.5% of ratio costs a third of the throughput, and it is bought
entirely with cache misses. That points at three things, none of them a
hot-loop rewrite: the default dictionary size is a policy choice worth
revisiting, the hash table entry is an `int64` absolute position where a
narrower relative one would halve the footprint, and both changes alter the
compressed output, so they need to be made deliberately rather than as an
optimization.

## Attempt D — remove the per-byte modulo in the rolling hashes (kept, no gain)

`CyclicPoly.RollByte` and `RabinKarp.RollByte` advanced their ring index with
`r.i = (r.i + 1) % cap(r.p)`. The divisor is not a constant, so this is a real
hardware divide on every input byte. Replacing it with an increment and a
compare is exactly equivalent, since `r.i` is always below `cap` beforehand.

**Result.** No measurable change (p=0.74, n=10). The writer is stalled on
memory, so the divide latency was already hidden. The simplification was kept
because it is strictly less work, but it is not a speedup.

## Getting here: attempts that did NOT work

Before finding the branch, two other range-coder experiments were implemented,
verified against the full test suite, benchmarked, and reverted after showing
no gain. They are recorded so the ideas are not retried blindly — and because
they are what pointed at the real cause.

### Attempt A — error-free, inlinable range decoder (reverted)

**Idea.** `DecodeBit` returns `(uint32, error)` and is called once per bit;
the hypothesis was that per-bit **call + error-return overhead** dominated.
The change made reads infallible (fully buffer the input, detect truncation
once per operation via an `eof` flag) and dropped the `error` return through
the whole decode chain so the compiler could inline `DecodeBit`.

**Result.** No measurable change (≈0%, p≈0.3). The compiler still would not
inline `DecodeBit` (removing the error only lowered its inline cost 132 → 129;
budget is 80), and the input read it cheapened was already ~0.8%.

### Attempt B — hoist range/code into registers (reverted)

**Idea.** Each `DecodeBit` call reloads `d.nrange`/`d.code` from the struct
and stores them back. Hoisting them into locals and hand-inlining the bit
decode across a whole symbol should keep that state in registers. Prototyped
on the literal codec.

**Result.** No measurable change (167.0 → 166.5 ms, p=0.49).

### Attempt C — preload both tree children, select by mask (reverted)

**Idea.** After Patch 6 the branchless bit decode makes the next tree node
index `m` a *data* dependency, so the CPU cannot speculate the `probs[m]` load
and its L1 latency sits on the serial bit-to-bit chain. Preloading both
children `probs[2m]`/`probs[2m+1]` while the bit resolves and selecting the
next probability with mask arithmetic (`l ^ ((l^r) & mask)`) should take the
load latency off the chain.

**Result.** Clearly worse: 99 → 113 ms (-13%). The two extra loads, their
bounds checks, and the select arithmetic cost more than the hidden latency
(and peeling the bottom tree level added per-symbol overhead to trees that are
only 3–8 bits deep). Reverted.

### What the attempts ruled out — and what remained

Neither call/error overhead (A) nor per-bit state reload (B) mattered *for the
probability-modelled bits* B was prototyped on (the literal codec). That left
the two things B did not change: the unpredictable data-dependent branch, and
the per-bit probability-model memory access. Patch 3 attacked the branch and
won ~+19%, confirming the branch — not overhead — was the dominant cost there.

Patch 4 then showed B's technique was not wrong, only mis-aimed: hoisting and
inlining *does* pay off on the **direct-bit** path, which has no probability
model, so state-reload and call overhead are the whole cost. The lesson: the
model memory access is what makes hoisting pointless — remove the model
(direct bits) and hoisting wins.

Attempt A deserves the same postscript: Patches 6 and 7 are essentially
Attempt A's idea (infallible reads, no per-bit error plumbing, state threaded
through the decode chain) and they *did* pay — but only after Patch 5 had
already inlined the bit arithmetic. When A was tried, `DecodeBit` was one
non-inlinable lump, so removing the error return changed nothing; once the
arithmetic was inline, the residual call/error/boundary overhead A targeted
was exactly what was left. Order matters: a correct idea can measure as a
zero against the wrong backdrop.

## Remaining bottlenecks

Fresh profile of the decode subtree after Patch 9 (share of decode time):

- **Range-coder arithmetic, ~75%** — `treeCodec.decode` ~29% cum,
  `directCodec.decode` ~17%, `treeReverseCodec` ~9%, plus the inlined
  `decodeBitArith` share attributed to `readOp`/`lengthCodec`. All of it is
  now inline, call-free, branch-lean and register-resident; what remains is
  the serial bit-to-bit dependency (each bit's `bound` needs the previous
  bit's `range`/`code`/probability) and the per-bit probability-model
  read-modify-write. Attempt C showed even hiding the model load latency
  costs more than it saves. In pure Go this is the floor; the only lever left
  of any size is hand-written assembly for the bit loops, which is a
  maintenance decision more than an engineering one.
- **Match copy (`writeMatch` + `memmove` + `apply`) ~12%** — after Patch 8
  this is almost entirely the inherent `memmove` of decoded bytes plus the
  once-per-op distance/length validation. Nothing big left.
- **Dict → caller copy (`Dict.Read` inside `decoder.Read`)** — every decoded
  byte is copied out of the circular dictionary buffer to the caller; with the
  xz wrapper above it, bytes are touched twice after decode. Restructuring so
  callers read straight from the window would be an API-level change with
  modest, memory-bandwidth-bound upside.
- **Parallelism** — within one LZMA2 block, chunks share dictionary state and
  cannot be decoded concurrently; single-stream decode is inherently serial.
  Multi-block xz files can be decoded block-parallel — implemented as
  `ParallelReader` in Patch 10 (10.7× on a real 470-block archive). Helps
  only suitably created files.
- **Writer:** still dominated by the hash match-finder, but see "Where the
  writer's time actually goes" above — that cost is cache misses over two
  ~32 MB tables, not call structure. The range encoder that the Patch 5–9
  techniques would target is only about 15% of writer time, so those
  techniques are worth at most a few percent here. The larger lever is the
  size of the match-finder working set, and pulling it changes the compressed
  output.

Decode has moved from 48.63 to 106.3 MiB/s (2.19×) on enwik7 — and 4.4× on
repetitive data — with byte-identical output. The reader is now genuinely
dominated by serial range-coder arithmetic; the next significant wins live on
the writer.

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

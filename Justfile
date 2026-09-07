# This file is the project's own — add recipes below. Keep the import: it
# mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::go::default do::lint::go::bce do::lint::go::escape do::lint::go::deadcode do::lint::default
fix: do::fix::go::default do::fix::default
test: do::test::go::unit do::test::go::race test-upstreamdiff
bench: do::test::go::bench

# The differential test against upstream (ulikunitz/xz) lives in its own
# module so the library never depends on upstream: the root go.mod stays free
# of it and the shared `./...` recipes do not descend into it. Network on
# first run (the upstream module is fetched through the module proxy,
# checksum-verified like any other dependency).
[doc('Differential test against upstream ulikunitz/xz (internal/upstreamdiff, its own module)')]
test-upstreamdiff:
    #!/usr/bin/env bash
    set -euo pipefail
    cd internal/upstreamdiff
    go test ./...

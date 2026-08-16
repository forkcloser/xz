# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::go::default do::lint::default
fix: do::fix::go::default do::fix::default
test: do::test::go::unit do::test::go::race test-386
bench: do::test::go::bench

# 32-bit coverage: this codebase is int-width sensitive — dictionary and buffer
# arithmetic, int64 stream sizes narrowed to int, and index records whose
# declared sizes are bounded against the address space — and none of that is
# exercised by a 64-bit run. No development machine is 32-bit, but 386 binaries
# execute natively on amd64 hosts, so the amd64 CI legs run this for free;
# other hosts skip it loudly rather than silently reporting a pass they never
# obtained. The race detector does not support 386.
[doc('Run the tests as GOARCH=386 (executes natively on amd64 hosts; skipped elsewhere)')]
test-386:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "$(go env GOHOSTARCH)" != "amd64" ]; then
        echo "GOARCH=386 binaries need an amd64 host to execute; skipping"
        exit 0
    fi
    CGO_ENABLED=0 GOARCH=386 go test -count=1 -timeout "${TEST_GO_TIMEOUT:-10m}" ./...

# This file is the project's own — add recipes below. Keep the import: it
# mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# Re-exports main.just's PATH with .build/xz-utils/bin in front, where the
# xz-utils recipe puts the XZ Utils binary the interop tests run; the
# importing file's definition wins. Keep the rest identical to main.just's.
export PATH := if os() == 'windows' { justfile_directory() / '.build' / 'xz-utils' / 'bin' + ';' + hermetic_bin + ';' + env_var('PATH') } else { justfile_directory() / '.build' / 'xz-utils' / 'bin' + ':' + hermetic_bin + ':/usr/bin:/bin:/usr/sbin:/sbin' }

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::go::default do::lint::go::deadcode do::lint::default
fix: do::fix::go::default do::fix::default
# The suites with the XZ Utils binary on PATH and required: the interop tests
# fail without it here, and skip on a lane that runs the Go recipes alone.
test: xz-utils
    #!/usr/bin/env bash
    set -euo pipefail
    export XZ_UTILS_REQUIRED=1
    just do test go unit
    just do test go race
    just test-upstreamdiff

# The XZ Utils binary the interop tests hold the package to, from the archive
# pins.yaml pins for this host, verified against the pinned sha256 on every
# run and unpacked into .build/xz-utils/bin as xz-utils: `xz` on the hermetic
# PATH is another program. Network on first run.
[doc('Fetch the pinned XZ Utils binary for this host into .build/xz-utils/bin/xz-utils')]
xz-utils:
    #!/usr/bin/env bash
    set -euo pipefail
    pin="xz-utils-$(go env GOOS)-$(go env GOARCH)"
    url=$(limen pins get "${pin}" url)
    sha256=$(limen pins get "${pin}" sha256)
    x=""
    [ "$(go env GOOS)" = windows ] && x=".exe"
    mkdir -p .build/xz-utils/bin
    archive=".build/xz-utils/${url##*/}"
    if [ ! -f "${archive}" ] || ! echo "${sha256}  ${archive}" | sha256sum -c --status; then
        curl --proto '=https' --tlsv1.2 -fsSL --retry 5 --retry-delay 3 -o "${archive}" "${url}"
        echo "${sha256}  ${archive}" | sha256sum -c --status
    fi
    name=${archive##*/}
    name=${name%.tar.gz}
    rm -rf ".build/xz-utils/${name}"
    tar -xzf "${archive}" -C .build/xz-utils
    mv -f ".build/xz-utils/${name}/xz${x}" ".build/xz-utils/bin/xz-utils${x}"
    ".build/xz-utils/bin/xz-utils${x}" --version | head -n 1
# The security workflow runs `just security`.
security: do::security::default
bench: do::perf::go::bench

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

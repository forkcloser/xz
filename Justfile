# This file is the project's own — add recipes below. Keep the import: it
# mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::go::default do::lint::go::bce do::lint::go::escape do::lint::go::deadcode do::lint::default
fix: do::fix::go::default do::fix::default
test: do::test::go::unit do::test::go::race
bench: do::test::go::bench

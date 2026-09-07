# Releasing

This is a library; a release is a signed tag on `main`, and the Go module
proxy does the rest. There is no goreleaser configuration and no release
workflow: nothing is built or uploaded, and `gxz` is installed with
`go install github.com/forkcloser/xz/cmd/gxz@<tag>`, which stamps the tag
into `gxz -V` through the build info.

1. `CHANGELOG.md`: move the *Unreleased* entries under the new version with
   today's date. Every behaviour a user could notice belongs there.
2. `just lint && just test` on a clean tree, and CI green on `main` for the
   commit to be tagged (all five verify legs and the fuzz job).
3. Tag and push. The tag is signed — `CONTRIBUTING.md` requires it — and is
   pushed alone, never with `--tags`:

       git tag -s vX.Y.Z -m vX.Y.Z
       git push origin refs/tags/vX.Y.Z

4. Check what the proxy sees: `go list -m github.com/forkcloser/xz@vX.Y.Z`
   from outside the tree, then `go install github.com/forkcloser/xz/cmd/gxz@vX.Y.Z`
   and `gxz -V`, which must print `vX.Y.Z`.
5. Publish the GitHub release from the tag with the changelog entry as its
   body, so the tag has a human-readable page.

Versioning follows semantic versioning against the stability contract in the
README: within v1, only additive changes in minor releases, fixes in patch
releases. A breaking change means a `v2` module path.

#!/usr/bin/env bash
#
# Prints the product version, the single source of truth stamped into the daemon
# at build time. Both build paths (build-payload.sh for the image, deploy.sh for
# the fast loop) call this and pass the result to `go build -ldflags -X`.
#
# The number comes from git tags: a commit that IS a tag prints that tag
# (v0.1.0); a commit after one prints tag-commits-ghash (v0.1.0-3-gabc1234); an
# uncommitted tree gets a -dirty suffix. Outside a git checkout -- a source
# tarball, say -- it falls back to "dev". Tag releases with an ANNOTATED tag:
#
#     git tag -a v0.1.0 -m 'boa v0.1.0' && git push origin v0.1.0
#
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if git rev-parse --git-dir >/dev/null 2>&1; then
  git describe --tags --always --dirty 2>/dev/null || echo dev
else
  echo dev
fi

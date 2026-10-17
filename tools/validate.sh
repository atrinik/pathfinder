#!/usr/bin/env sh
# Everything CI checks. Needs the tools from install-ci-dependencies.sh.
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [ -n "${unformatted}" ]; then
  echo "gofmt needed:" >&2
  echo "${unformatted}" >&2
  exit 1
fi
go mod verify
go vet ./...
staticcheck ./...
if [ "${CGO_ENABLED:-1}" = 0 ]; then
  go test ./...
else
  go test -race ./...
fi

# The committed bindings must match the contract.
sh tools/generate.sh
git diff --exit-code -- gen/ || {
  echo "gen/ is stale: run tools/generate.sh and commit the result" >&2
  exit 1
}
govulncheck ./...
go build -trimpath -o /dev/null ./cmd/pathfinderd

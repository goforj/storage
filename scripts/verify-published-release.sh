#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  printf 'usage: %s vX.Y.Z\n' "$0" >&2
  exit 2
fi

root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export GOWORK=off GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
export GOPRIVATE= GONOPROXY= GONOSUMDB= GOFLAGS= GOMODCACHE="$tmp/modules"

cd "$tmp"
go mod init example.com/storage-release-check
{
  printf 'package consumer\n\nimport (\n'
  for manifest in "$root/go.mod" "$root/storagecore/go.mod" "$root/storagetest/go.mod" "$root"/driver/*/go.mod; do
    module="$(awk '$1 == "module" { print $2; exit }' "$manifest")"
    printf '\t_ "%s"\n' "$module"
    go mod edit -require="$module@$version"
  done
  printf ')\n'
} > consumer_test.go

expected="$(awk '$1 == "_" { gsub(/"/, "", $2); print $2 }' consumer_test.go | sort)"
status=0
while IFS= read -r module; do
  if ! go mod download "$module@$version"; then
    status=1
  fi
done <<< "$expected"
if [[ "$status" -ne 0 ]]; then
  exit "$status"
fi

go mod tidy
go test ./...

selected="$(go list -m -f '{{if and .Version (not .Replace)}}{{.Path}} {{.Version}}{{end}}' all)"
while IFS= read -r module; do
  if ! grep -Fxq "$module $version" <<< "$selected"; then
    printf 'expected %s %s in the consumer build\n' "$module" "$version" >&2
    exit 1
  fi
  printf 'Verified %s %s\n' "$module" "$version"
done <<< "$expected"

#!/usr/bin/env bash

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
allowlist="$root/scripts/govulncheck-allowlist.txt"

matches() {
	diff -u <(sort -u "$1") <(sort -u "$2") >/dev/null
}

rejects() {
	if matches "$1" "$2"; then
		return 1
	fi
}

actual="$(mktemp)"
stale="$(mktemp)"
orphan="$(mktemp)"
unexpected="$(mktemp)"
trap 'rm -f "$actual" "$stale" "$orphan" "$unexpected"' EXIT

awk 'NF { if (NF != 2) { exit 1 }; print $1 " " $2 }' "$allowlist" >"$actual"
cp "$actual" "$stale"
printf 'stale/module GO-stale\n' >>"$stale"
cp "$actual" "$orphan"
printf 'orphan/module GO-orphan\n' >>"$orphan"
cp "$actual" "$unexpected"
printf 'unexpected/module GO-unexpected\n' >>"$unexpected"

matches "$allowlist" "$actual"
rejects "$stale" "$actual"
rejects "$orphan" "$actual"
rejects "$allowlist" "$unexpected"

echo "govulncheck allowlist exact-set checks passed"

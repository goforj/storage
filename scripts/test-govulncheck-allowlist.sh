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

mock_scan_is_accepted() {
	local scan_status="$1"
	local scanner_output="$2"
	local parsed_findings
	parsed_findings="$(printf '%s\n' "$scanner_output" | awk '/^Vulnerability #[0-9]+: GO-/{print $3}')"

	[[ "$scan_status" == "0" && -z "$parsed_findings" ]] || \
		[[ "$scan_status" == "3" && -n "$parsed_findings" ]]
}

assert_mock_accepts() {
	mock_scan_is_accepted "$1" "$2"
}

assert_mock_rejects() {
	if mock_scan_is_accepted "$1" "$2"; then
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

assert_mock_accepts 0 ''
assert_mock_accepts 3 'Vulnerability #1: GO-2026-0001'
assert_mock_rejects 0 'Vulnerability #1: GO-2026-0001'
assert_mock_rejects 3 ''
assert_mock_rejects 2 'Vulnerability #1: GO-2026-0001'

echo "govulncheck allowlist exact-set checks passed"

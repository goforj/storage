#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

if ! command -v govulncheck >/dev/null 2>&1; then
	printf 'govulncheck must be installed before scanning\n' >&2
	exit 2
fi

allowlist="$root/scripts/govulncheck-allowlist.txt"
workspace="$(mktemp -d)"
actual_findings="$(mktemp)"
allowlisted_findings="$(mktemp)"
trap 'rm -rf "$workspace" "$actual_findings" "$allowlisted_findings"' EXIT

mapfile -t module_dirs < <(find "$root" -name go.mod -type f -not -path '*/.git/*' -not -path '*/vendor/*' -exec dirname {} \; | sort)
(cd "$workspace" && GOWORK=off go work init "${module_dirs[@]}")

status=0
while IFS= read -r modfile; do
	dir="${modfile#$root/}"
	if [[ "$dir" == "go.mod" ]]; then
		dir="."
	else
		dir="${dir%/go.mod}"
	fi

	packages="$(cd "$root/$dir" && GOWORK="$workspace/go.work" go list ./...)"
	if [[ -z "$packages" ]]; then
		printf 'govulncheck: %s has no Go packages, skipped\n' "$dir"
		continue
	fi

	printf 'govulncheck: %s\n' "$dir"
	output="$(mktemp)"
	if (cd "$root/$dir" && GOWORK="$workspace/go.work" govulncheck -test ./...) >"$output" 2>&1; then
		scan_status=0
	else
		scan_status=$?
	fi
	cat "$output"
	awk -v module="$dir" '/^Vulnerability #[0-9]+: GO-/{print module " " $3}' "$output" >>"$actual_findings"
	if [[ "$scan_status" -ne 0 && "$scan_status" -ne 3 ]]; then
		status=1
	fi
	rm -f "$output"
done < <(find "$root" -name go.mod -type f -not -path '*/.git/*' -not -path '*/vendor/*' | sort)

awk 'NF { if (NF != 2) { exit 1 }; print $1 " " $2 }' "$allowlist" | sort -u >"$allowlisted_findings" || status=1
if ! diff -u "$allowlisted_findings" <(sort -u "$actual_findings"); then
	status=1
fi

exit "$status"

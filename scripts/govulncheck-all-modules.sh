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
trap 'rm -rf "$workspace"' EXIT

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
		cat "$output"
		rm -f "$output"
		continue
	else
		scan_status=$?
	fi
	cat "$output"
	actual_ids="$(awk '/^Vulnerability #[0-9]+: GO-/{print $3}' "$output" | sort -u)"
	allowed_ids="$(awk -v module="$dir" '$1 == module {print $2}' "$allowlist" | sort -u)"
	if [[ "$scan_status" -ne 3 ]] || [[ -z "$actual_ids" ]] || ! diff -u <(printf '%s\n' "$allowed_ids") <(printf '%s\n' "$actual_ids"); then
		status=1
	fi
	rm -f "$output"
done < <(find "$root" -name go.mod -type f -not -path '*/.git/*' -not -path '*/vendor/*' | sort)

exit "$status"

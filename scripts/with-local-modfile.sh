#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
	printf 'usage: %s <module-dir> <command> [args...]\n' "$0" >&2
	exit 2
fi

root="$(git rev-parse --show-toplevel)"
module_dir="$1"
shift

if [[ ! -f "$root/$module_dir/go.mod" ]]; then
	printf 'missing module: %s\n' "$module_dir" >&2
	exit 2
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
modfile="$tmpdir/module.mod"
sumfile="$tmpdir/module.sum"

cp "$root/$module_dir/go.mod" "$modfile"
if [[ -f "$root/$module_dir/go.sum" ]]; then
	cp "$root/$module_dir/go.sum" "$sumfile"
fi

while IFS= read -r sibling_mod; do
	sibling_dir="${sibling_mod#$root/}"
	if [[ "$sibling_dir" == "go.mod" ]]; then
		sibling_dir="."
	else
		sibling_dir="${sibling_dir%/go.mod}"
	fi
	if [[ "$sibling_dir" == "$module_dir" ]]; then
		continue
	fi
	sibling_path="$(awk '$1 == "module" { print $2; exit }' "$sibling_mod")"
	go mod edit -modfile="$modfile" -replace="$sibling_path=$root/$sibling_dir"
done < <(find "$root" -name go.mod -type f -not -path '*/.git/*' -not -path '*/vendor/*' | sort)

(
	cd "$root/$module_dir"
	export GOWORK=off
	export GOFLAGS="${GOFLAGS:+$GOFLAGS }-modfile=$modfile"
	exec "$@"
)

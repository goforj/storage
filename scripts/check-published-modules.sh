#!/usr/bin/env bash
set -euo pipefail

published_modules=(
  "driver/dropboxstorage"
  "driver/ftpstorage"
  "driver/gcsstorage"
  "driver/localstorage"
  "driver/memorystorage"
  "driver/rclonestorage"
  "driver/redisstorage"
  "driver/s3storage"
  "driver/sftpstorage"
)

status=0

find_matches() {
  local pattern="$1"
  local file="$2"

  if command -v rg >/dev/null 2>&1; then
    rg -n "$pattern" "$file"
  else
    grep -nE "$pattern" "$file"
  fi
}

for dir in "${published_modules[@]}"; do
  modfile="$dir/go.mod"

  if [[ ! -f "$modfile" ]]; then
    echo "missing module file: $modfile" >&2
    status=1
    continue
  fi

  if find_matches 'github\.com/goforj/storage[^ ]* v0\.0\.0\b' "$modfile" >/dev/null; then
    echo "invalid sibling v0.0.0 requirement in $modfile" >&2
    find_matches 'github\.com/goforj/storage[^ ]* v0\.0\.0\b' "$modfile" >&2
    status=1
  fi

  if find_matches '^replace github\.com/goforj/storage' "$modfile" >/dev/null; then
    echo "invalid committed sibling replace in $modfile" >&2
    find_matches '^replace github\.com/goforj/storage' "$modfile" >&2
    status=1
  fi
done

exit "$status"

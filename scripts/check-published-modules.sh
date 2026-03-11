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

for dir in "${published_modules[@]}"; do
  modfile="$dir/go.mod"

  if [[ ! -f "$modfile" ]]; then
    echo "missing module file: $modfile" >&2
    status=1
    continue
  fi

  if rg -n 'github\.com/goforj/storage[^ ]* v0\.0\.0\b' "$modfile" >/dev/null; then
    echo "invalid sibling v0.0.0 requirement in $modfile" >&2
    rg -n 'github\.com/goforj/storage[^ ]* v0\.0\.0\b' "$modfile" >&2
    status=1
  fi

  if rg -n '^replace github\.com/goforj/storage' "$modfile" >/dev/null; then
    echo "invalid committed sibling replace in $modfile" >&2
    rg -n '^replace github\.com/goforj/storage' "$modfile" >&2
    status=1
  fi
done

exit "$status"

#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/release-all-modules.sh <version> [--commit] [--message <msg>] [--push] [--remote <name>] [--dry-run] [--allow-dirty] [--skip-existing] [--exclude <module-dir>]

Examples:
  scripts/release-all-modules.sh v0.2.2 --dry-run
  scripts/release-all-modules.sh v0.2.2 --commit
  scripts/release-all-modules.sh v0.2.2 --commit --push
  scripts/release-all-modules.sh v0.2.2 --commit --exclude docs --exclude examples

Behavior:
  - Rewrites intra-repo module requirements to the target version
  - Runs go mod tidy in any module whose sibling requirements changed
  - Verifies published driver manifests
  - Optionally creates a release commit
  - Tags the resulting commit using scripts/tag-all-modules.sh

Notes:
  - Tagging only happens after the rewritten module files are committed.
  - Without --commit, the script stops after rewriting/checking so you can review.
USAGE
}

version=""
commit_release=0
commit_message=""
push=0
remote="origin"
dry_run=0
allow_dirty=0
skip_existing=0
excludes=()

normalize_module_dir() {
  local dir="$1"
  dir="${dir#./}"
  dir="${dir%/}"
  if [[ -z "$dir" ]]; then
    dir="."
  fi
  printf '%s\n' "$dir"
}

module_is_excluded() {
  local dir="$1"
  local ex
  for ex in "${excludes[@]-}"; do
    if [[ "$dir" == "$ex" ]] || [[ "$dir" == "$ex/"* ]]; then
      return 0
    fi
  done
  return 1
}

module_requires() {
  local modfile="$1"
  awk '
    $1 == "require" && $2 == "(" { in_require = 1; next }
    in_require && $1 == ")" { in_require = 0; next }
    $1 == "require" && $2 != "(" { print $2; next }
    in_require && $1 !~ /^\/\// { print $1 }
  ' "$modfile"
}

if [[ $# -lt 1 ]]; then
  usage
  exit 1
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --commit)
      commit_release=1
      shift
      ;;
    --message)
      commit_message="${2:-}"
      if [[ -z "$commit_message" ]]; then
        echo "error: --message requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --push)
      push=1
      shift
      ;;
    --remote)
      remote="${2:-}"
      if [[ -z "$remote" ]]; then
        echo "error: --remote requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    --allow-dirty)
      allow_dirty=1
      shift
      ;;
    --skip-existing)
      skip_existing=1
      shift
      ;;
    --exclude)
      mod="${2:-}"
      if [[ -z "$mod" ]]; then
        echo "error: --exclude requires a module directory value" >&2
        exit 1
      fi
      excludes+=("$(normalize_module_dir "$mod")")
      shift 2
      ;;
    v*)
      if [[ -n "$version" ]]; then
        echo "error: multiple versions provided" >&2
        exit 1
      fi
      version="$1"
      shift
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$version" ]]; then
  echo "error: version is required (example: v0.1.3)" >&2
  exit 1
fi

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  echo "error: version must look like vX.Y.Z (optionally with -prerelease and/or +build suffix)" >&2
  exit 1
fi

root="$(git rev-parse --show-toplevel)"
cd "$root"

initial_status="$(git status --porcelain)"
initial_dirty=0
if [[ -n "$initial_status" ]]; then
  initial_dirty=1
fi

if [[ "$allow_dirty" -eq 0 ]] && [[ "$initial_dirty" -eq 1 ]]; then
  echo "error: working tree is dirty. commit/stash or pass --allow-dirty" >&2
  exit 1
fi

declare -a module_dirs=()
declare -a module_paths=()

module_path_for_dir() {
  local wanted_dir="$1"
  local i
  for ((i = 0; i < ${#module_dirs[@]}; i++)); do
    if [[ "${module_dirs[$i]}" == "$wanted_dir" ]]; then
      printf '%s\n' "${module_paths[$i]}"
      return 0
    fi
  done
  return 1
}

module_dir_for_path() {
  local wanted_path="$1"
  local i
  for ((i = 0; i < ${#module_paths[@]}; i++)); do
    if [[ "${module_paths[$i]}" == "$wanted_path" ]]; then
      printf '%s\n' "${module_dirs[$i]}"
      return 0
    fi
  done
  return 1
}

while IFS= read -r dir; do
  dir="$(normalize_module_dir "$dir")"
  if module_is_excluded "$dir"; then
    continue
  fi

  modfile="$dir/go.mod"
  module_path="$(awk '$1 == "module" { print $2; exit }' "$modfile")"
  if [[ -z "$module_path" ]]; then
    echo "error: failed to read module path from $modfile" >&2
    exit 1
  fi

  module_dirs+=("$dir")
  module_paths+=("$module_path")
done <<EOF_MODULES
$(find . -name go.mod -type f \
  -not -path './.git/*' \
  -not -path './*/.git/*' \
  -not -path './*/vendor/*' \
  -exec dirname {} \; | sed 's#^\./##' | sort)
EOF_MODULES

if [[ ${#module_dirs[@]} -eq 0 ]]; then
  echo "error: no modules discovered" >&2
  exit 1
fi

GOCACHE_DIR="${GOCACHE:-/tmp/storage-gocache}"
GOMODCACHE_DIR="${GOMODCACHE:-/tmp/storage-gomodcache}"
mkdir -p "$GOCACHE_DIR" "$GOMODCACHE_DIR"

declare -a changed_modules=()
declare -a planned_updates=()
declare -a release_files=()

for dir in "${module_dirs[@]}"; do
  modfile="$dir/go.mod"
  current_module="$(module_path_for_dir "$dir")"
  updated=0

  while IFS= read -r req; do
    [[ -n "$req" ]] || continue
    sibling_dir="$(module_dir_for_path "$req" || true)"
    if [[ -z "$sibling_dir" ]] || [[ "$req" == "$current_module" ]]; then
      continue
    fi

    planned_updates+=("$dir: $req@$version")
    updated=1

    if [[ "$dry_run" -eq 0 ]]; then
      (
        cd "$dir"
        GOWORK=off go mod edit -require="$req@$version"
      )
    fi
  done < <(module_requires "$modfile")

  if [[ "$updated" -eq 1 ]]; then
    changed_modules+=("$dir")
    release_files+=("$dir/go.mod")
    if [[ "$dry_run" -eq 0 ]]; then
      (
        cd "$dir"
        GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go mod tidy
      )
    fi
    if [[ -f "$dir/go.sum" ]]; then
      release_files+=("$dir/go.sum")
    fi
  fi
done

echo "repo: $root"
echo "version: $version"
if [[ ${#excludes[@]} -gt 0 ]]; then
  echo "excluded modules: ${excludes[*]}"
fi
if [[ ${#planned_updates[@]} -gt 0 ]]; then
  echo "planned dependency updates (${#planned_updates[@]}):"
  for update in "${planned_updates[@]}"; do
    echo "  - $update"
  done
else
  echo "planned dependency updates: none"
fi

if [[ "$dry_run" -eq 1 ]]; then
  echo "dry-run: no files changed"
else
  scripts/check-published-modules.sh
fi

if [[ "$dry_run" -eq 1 ]]; then
  tag_args=("$version" "--dry-run")
  if [[ "$allow_dirty" -eq 1 ]]; then
    tag_args+=("--allow-dirty")
  fi
  if [[ "$skip_existing" -eq 1 ]]; then
    tag_args+=("--skip-existing")
  fi
  if [[ ${#excludes[@]} -gt 0 ]]; then
    for ex in "${excludes[@]}"; do
      tag_args+=("--exclude" "$ex")
    done
  fi
  bash scripts/tag-all-modules.sh "${tag_args[@]}"
  exit 0
fi

if [[ ${#changed_modules[@]} -eq 0 ]]; then
  echo "no module changes produced"
else
  if [[ "$commit_release" -eq 1 ]]; then
    if [[ "$initial_dirty" -eq 1 ]]; then
      echo "error: refusing to auto-commit release changes on top of an already-dirty tree" >&2
      exit 1
    fi
    if [[ -z "$commit_message" ]]; then
      commit_message="chore: release $version"
    fi
    git add -- "${release_files[@]}"
    git commit -m "$commit_message"
  else
    echo "release files updated but not committed"
    echo "review and commit the changes, then rerun or use --commit"
    exit 0
  fi
fi

tag_args=("$version")
if [[ "$push" -eq 1 ]]; then
  tag_args+=("--push")
  tag_args+=("--remote" "$remote")
fi
if [[ "$allow_dirty" -eq 1 ]]; then
  tag_args+=("--allow-dirty")
fi
if [[ "$skip_existing" -eq 1 ]]; then
  tag_args+=("--skip-existing")
fi
if [[ ${#excludes[@]} -gt 0 ]]; then
  for ex in "${excludes[@]}"; do
    tag_args+=("--exclude" "$ex")
  done
fi

bash scripts/tag-all-modules.sh "${tag_args[@]}"

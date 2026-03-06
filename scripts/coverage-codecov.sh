#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

OUTPUT_FILE="${COVERAGE_OUTPUT:-coverage.txt}"
TMP_ROOT="${COVERAGE_TMP_DIR:-/tmp/storage-coverage}"
GOCACHE_DIR="${GOCACHE_DIR:-/tmp/storage-gocache}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-/tmp/storage-gomodcache}"
INTEGRATION_TAGS="${INTEGRATION_TAGS:-integration}"
INTEGRATION_MODULE_DIR="${INTEGRATION_MODULE_DIR:-integration}"
INTEGRATION_MODULE_PKGS="${INTEGRATION_MODULE_PKGS:-./all}"
INTEGRATION_MODULE_COVERPKG="${INTEGRATION_MODULE_COVERPKG:-github.com/goforj/storage,github.com/goforj/storage/storagetest,github.com/goforj/storage/integration/...,github.com/goforj/storage/driver/localstorage,github.com/goforj/storage/driver/s3storage,github.com/goforj/storage/driver/gcsstorage,github.com/goforj/storage/driver/sftpstorage,github.com/goforj/storage/driver/ftpstorage,github.com/goforj/storage/driver/rclonestorage}"

ROOT_COVER_DIR="$TMP_ROOT/root"
STORAGETEST_COVER_DIR="$TMP_ROOT/storagetest"
INTEGRATION_COVER_DIR="$TMP_ROOT/integration"
MERGED_DIR="$TMP_ROOT/merged"

DRIVER_MODULES=(
  driver/localstorage
  driver/s3storage
  driver/gcsstorage
  driver/sftpstorage
  driver/ftpstorage
  driver/dropboxstorage
  driver/rclonestorage
)

rm -rf "$TMP_ROOT"
mkdir -p "$ROOT_COVER_DIR" "$STORAGETEST_COVER_DIR" "$INTEGRATION_COVER_DIR" "$MERGED_DIR" "$GOCACHE_DIR" "$GOMODCACHE_DIR"

echo "==> Root module coverage"
GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
go test -cover ./... -args -test.gocoverdir="$ROOT_COVER_DIR"

echo "==> storagetest module coverage"
(
  cd storagetest
  GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
  go test -cover ./... -args -test.gocoverdir="$STORAGETEST_COVER_DIR"
)

merge_inputs=("$ROOT_COVER_DIR" "$STORAGETEST_COVER_DIR")

for module_dir in "${DRIVER_MODULES[@]}"; do
  module_name="$(basename "$module_dir")"
  cover_dir="$TMP_ROOT/${module_name}"
  mkdir -p "$cover_dir"
  echo "==> ${module_name} module coverage"
  (
    cd "$module_dir"
    GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
    go test -cover ./... -args -test.gocoverdir="$cover_dir"
  )
  merge_inputs+=("$cover_dir")
done

if [[ -d "$INTEGRATION_MODULE_DIR" ]]; then
  echo "==> Integration module coverage ($INTEGRATION_MODULE_DIR)"
  (
    cd "$INTEGRATION_MODULE_DIR"
    GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
    go test -cover -tags="$INTEGRATION_TAGS" -coverpkg="$INTEGRATION_MODULE_COVERPKG" $INTEGRATION_MODULE_PKGS \
      -args -test.gocoverdir="$INTEGRATION_COVER_DIR"
  )
  merge_inputs+=("$INTEGRATION_COVER_DIR")
fi

echo "==> Merge coverage"
merge_csv="$(IFS=,; echo "${merge_inputs[*]}")"
go tool covdata merge -i="$merge_csv" -o="$MERGED_DIR"

mkdir -p "$(dirname "$OUTPUT_FILE")"
go tool covdata textfmt -i="$MERGED_DIR" -o="$OUTPUT_FILE"

echo "==> Combined coverage written to $OUTPUT_FILE"
go tool covdata percent -i="$MERGED_DIR"

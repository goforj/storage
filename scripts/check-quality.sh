#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

published_modules=(
  "."
  "storagecore"
  "storagetest"
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

echo "==> Verify published module manifests"
scripts/check-published-modules.sh

for module in "${published_modules[@]}"; do
  echo "==> Quality: $module"
  (
    cd "$module"
    go mod tidy -diff
    go vet ./...
    go test -race -count=1 ./...
  )
done

for module in "${published_modules[@]}"; do
  echo "==> Published dependency compatibility: $module"
  (
    cd "$module"
    GOWORK=off go test -run '^$' -count=1 ./...
  )
done

echo "==> Quality: examples"
(
  cd examples
  go mod tidy -diff
  GOWORK=off go vet ./...
  GOWORK=off go test -count=1 ./...
)

echo "==> Quality: integration compile"
(
  cd integration
  go mod tidy -diff
  go vet -tags=integration ./...
  go test -tags=integration -run '^$' -count=1 ./...
)

echo "==> Quality: benchmark compile"
(
  cd docs/bench
  go mod tidy -diff
  go vet -tags=bench ./...
  go vet -tags=benchrender ./...
  go test -race -tags=bench -run '^$' -count=1 ./...
  go test -race -tags=benchrender -run '^$' -count=1 ./...
)

#!/usr/bin/env sh

set -eu

if ! command -v wgo >/dev/null 2>&1; then
  echo "Installing wgo..."
  go install github.com/bokwoon95/wgo@latest
fi

echo "Watching Go source for API reference and example updates..."
exec wgo -verbose -file=.go -xfile '_test\.go$' -xdir examples \
  go run ./docs/examplegen/main.go :: \
  go run ./docs/readme/main.go

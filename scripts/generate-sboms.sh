#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_directory="${1:?usage: generate-sboms.sh OUTPUT_DIRECTORY}"
workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT

mkdir -p "$output_directory"
mapfile -t module_directories < <(find "$repository_root" -name go.mod -not -path '*/vendor/*' -printf '%h\n' | sort)

if [[ ${#module_directories[@]} -eq 0 ]]; then
  echo "No Go modules found." >&2
  exit 1
fi

(
  cd "$workspace"
  GOWORK=off go work init "${module_directories[@]}"
)

for index in "${!module_directories[@]}"; do
  module_directory="${module_directories[$index]}"
  module_name="${module_directory#"$repository_root"/}"
  [[ "$module_name" == "$module_directory" ]] && module_name="root"
  output_file="$output_directory/${index}-${module_name//\//_}.cdx.json"

  echo "Generating SBOM for $module_name"
  (
    cd "$repository_root"
    GOWORK="$workspace/go.work" cyclonedx-gomod mod -json -test -output "$output_file" "$module_directory"
  )
done

generated_count="$(find "$output_directory" -maxdepth 1 -name '*.cdx.json' -type f -size +0c | wc -l | tr -d ' ')"
expected_count="${#module_directories[@]}"
if [[ "$generated_count" != "$expected_count" ]]; then
  echo "Generated $generated_count SBOMs for $expected_count modules." >&2
  exit 1
fi

echo "Generated and verified $generated_count SBOMs."

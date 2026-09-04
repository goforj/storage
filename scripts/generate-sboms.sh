#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_directory="${1:?usage: generate-sboms.sh OUTPUT_DIRECTORY}"
mkdir -p "$output_directory"
mapfile -t module_directories < <(find "$repository_root" -name go.mod -not -path '*/vendor/*' -printf '%h\n' | sort)

if [[ ${#module_directories[@]} -eq 0 ]]; then
  echo "No Go modules found." >&2
  exit 1
fi

for index in "${!module_directories[@]}"; do
  module_directory="${module_directories[$index]}"
  module_name="${module_directory#"$repository_root"/}"
  [[ "$module_name" == "$module_directory" ]] && module_name="root"
  output_file="$output_directory/${index}-${module_name//\//_}.cdx.json"
  module_path="$(cd "$module_directory" && GOWORK=off go list -m -f '{{.Path}}')"
  module_argument="$module_name"
  [[ "$module_name" == "root" ]] && module_argument="."

  echo "Generating SBOM for $module_name"
  if ! (
    cd "$module_directory"
    GOWORK=off cyclonedx-gomod mod -json -type library -test -output "$output_file" .
  ); then
    rm -f "$output_file"
    "$repository_root/scripts/with-local-modfile.sh" "$module_argument" cyclonedx-gomod mod -json -type library -test -output "$output_file" .
  fi

  jq -e --arg module_path "$module_path" '
    .bomFormat == "CycloneDX" and
    .metadata.component.type == "library" and
    .metadata.component.name == $module_path and
    (.metadata.component.purl | startswith("pkg:golang/" + $module_path + "?type=module")) and
    ([.. | objects | .name? | strings | select(. == "..")] | length == 0) and
    ([.. | objects | .purl? | strings | select(startswith("pkg:golang/.."))] | length == 0)
  ' "$output_file" >/dev/null
done

generated_count="$(find "$output_directory" -maxdepth 1 -name '*.cdx.json' -type f -size +0c | wc -l | tr -d ' ')"
expected_count="${#module_directories[@]}"
if [[ "$generated_count" != "$expected_count" ]]; then
  echo "Generated $generated_count SBOMs for $expected_count modules." >&2
  exit 1
fi

echo "Generated and verified $generated_count SBOMs."

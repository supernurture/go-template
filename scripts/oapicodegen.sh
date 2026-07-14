#!/bin/bash

set -euo pipefail

CONFIG_PATH="api/server/config.yaml"
SPECS_PATH="api/server/specs"

OUTPUT_DIR="internal/api/server/oapicodegen"
mkdir -p "$OUTPUT_DIR"

for file in "$SPECS_PATH"/*.yaml; do
  [ -e "$file" ] || continue

  name=$(basename "$file" .yaml)

  # a subdirectory per specification, package named after it, to avoid clashes
  mkdir -p "$OUTPUT_DIR/$name"

  echo "→ generating $name"

  go tool oapi-codegen \
    -config "$CONFIG_PATH" \
    -package "$name" \
    -o "$OUTPUT_DIR/$name/${name}_gen.go" \
    "$file"
done
#!/bin/bash

set -e

CONFIG_PATH="api/server/config.yaml"
SPECIFICATIONS_PATH="api/server/specifications"

OUTPUT_DIR="internal/api/server/oapicodegen"
mkdir -p "$OUTPUT_DIR"

for file in $(ls "$SPECIFICATIONS_PATH"/*.yaml | sort); do
  [ -e "$file" ] || continue

  name=$(basename "$file" .yaml)

  echo "→ generating $name"

  go tool oapi-codegen \
    -config "$CONFIG_PATH" \
    -o "$OUTPUT_DIR/${name}_gen.go" \
    "$file"
done
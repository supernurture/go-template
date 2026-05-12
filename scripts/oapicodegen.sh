#!/bin/bash

set -e

CONFIG="api/server/config.yaml"
SPECIFICATIONS="api/server/specifications"
OUTPUT_DIR="internal/api/server/oapicodegen"

mkdir -p "$OUTPUT_DIR"

for file in $(ls "$SPECIFICATIONS"/*.yaml | sort); do
  [ -e "$file" ] || continue

  name=$(basename "$file" .yaml)

  echo "→ generating $name"

  go tool oapi-codegen \
    -config "$CONFIG" \
    -o "$OUTPUT_DIR/$name.oapicodegen.go" \
    "$file"
done
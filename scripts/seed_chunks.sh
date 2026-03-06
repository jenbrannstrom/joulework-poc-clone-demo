#!/usr/bin/env bash
set -euo pipefail

CHUNK_DIR="${1:-./data/chunks}"
COUNT="${2:-20}"

mkdir -p "$CHUNK_DIR"

for i in $(seq -w 1 "$COUNT"); do
  printf "chunk-%s %s\n" "$i" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$CHUNK_DIR/chunk_$i.txt"
done

echo "seeded $COUNT chunks in $CHUNK_DIR"

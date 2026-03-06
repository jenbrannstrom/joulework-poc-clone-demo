#!/usr/bin/env bash
set -euo pipefail

CHUNK_DIR="${1:-./data/chunks}"
TASK_COUNT="${2:-40}"
TERMS_PER_TASK="${3:-200000}"

mkdir -p "$CHUNK_DIR"

start_term=0
for i in $(seq -w 1 "$TASK_COUNT"); do
  cat >"$CHUNK_DIR/pi_${i}.json" <<EOF
{"taskType":"pi_leibniz","startTerm":$start_term,"termCount":$TERMS_PER_TASK}
EOF
  start_term=$((start_term + TERMS_PER_TASK))
done

total_terms=$((TASK_COUNT * TERMS_PER_TASK))
echo "seeded $TASK_COUNT pi chunks in $CHUNK_DIR (total terms: $total_terms)"

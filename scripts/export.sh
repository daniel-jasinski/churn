#!/usr/bin/env bash
# export.sh — export a workspace event log as canonical JSONL.
#
# Usage: scripts/export.sh <data-dir> [out.jsonl]
# Default out: churn-export-YYYYMMDD-HHMMSS.jsonl (in the repo root).
# Safe while a server is running (read-only WAL snapshot). The JSONL is the
# grep/diff/git/interchange form of the log — import-log restores it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ $# -lt 1 ]; then
  echo "usage: scripts/export.sh <data-dir> [out.jsonl]" >&2
  exit 1
fi
data="$1"
out="${2:-churn-export-$(date +%Y%m%d-%H%M%S).jsonl}"

if [ ! -x ./churn.exe ]; then
  echo "==> no ./churn.exe yet: building"
  go build -o churn.exe ./cmd/churn
fi

echo "==> exporting $data -> $out"
./churn.exe export-log --data "$data" --out "$out"
echo "==> $(wc -l < "$out") events written"

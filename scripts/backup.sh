#!/usr/bin/env bash
# backup.sh — consistent online snapshot of a workspace database.
#
# Usage: scripts/backup.sh <data-dir> [dest.db]
# Default dest: churn-backup-YYYYMMDD-HHMMSS.db (in the current repo root).
# Safe while a server is running (SQLite online-backup API).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ $# -lt 1 ]; then
  echo "usage: scripts/backup.sh <data-dir> [dest.db]" >&2
  exit 1
fi
data="$1"
dest="${2:-churn-backup-$(date +%Y%m%d-%H%M%S).db}"

if [ ! -x ./churn.exe ]; then
  echo "==> no ./churn.exe yet: building"
  go build -o churn.exe ./cmd/churn
fi

echo "==> backing up $data -> $dest"
./churn.exe backup --data "$data" "$dest"

#!/usr/bin/env bash
# dev.sh — build and run a local dev server.
#
# Usage: scripts/dev.sh [extra serve flags…]
# Builds ./churn.exe, seeds ./workspace with the demo data IF the directory
# does not exist yet, then serves it on 127.0.0.1:8080. Extra arguments are
# passed through to `churn serve` (e.g. --verbose, --actor you).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "==> building ./churn.exe"
go build -o churn.exe ./cmd/churn

if [ ! -d workspace ]; then
  echo "==> no ./workspace yet: seeding the demo workspace"
  ./churn.exe seed-demo --data workspace
else
  echo "==> reusing existing ./workspace"
fi

# --no-open: the dev workflow manages its own browser tab; drop --no-open to
# have serve launch one. Pinned to :8080 for a stable dev URL.
echo "==> serving http://127.0.0.1:8080 (Ctrl-C to stop)"
exec ./churn.exe serve --data workspace --listen 127.0.0.1:8080 --no-open "$@"

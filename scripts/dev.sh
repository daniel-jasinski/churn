#!/usr/bin/env bash
# dev.sh — build and run a local dev server.
#
# Usage: scripts/dev.sh [extra serve flags…]
# Builds ./churn.exe, seeds the data directory with the demo data IF it does
# not exist yet, then serves it. Extra arguments are passed through to
# `churn serve` (e.g. --verbose, --actor you).
#
# Two knobs, both defaulted so the plain invocation is unchanged (:8080 on
# ./workspace):
#   PORT            listen port
#   CHURN_DEV_DATA  data directory
# They exist because a workspace is held under an exclusive OS lock
# (internal/store/lock.go), so a second dev server needs its own data
# directory as well as its own port — otherwise it fails on the lock rather
# than on the port, which is a much less obvious error.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

port="${PORT:-8080}"
data="${CHURN_DEV_DATA:-workspace}"

echo "==> building ./churn.exe"
go build -o churn.exe ./cmd/churn

if [ ! -d "$data" ]; then
  echo "==> no ./$data yet: seeding the demo workspace"
  ./churn.exe seed-demo --data "$data"
else
  echo "==> reusing existing ./$data"
fi

# --no-open: the dev workflow manages its own browser tab; drop --no-open to
# have serve launch one.
echo "==> serving http://127.0.0.1:$port (Ctrl-C to stop)"
exec ./churn.exe serve --data "$data" --listen "127.0.0.1:$port" --no-open "$@"

#!/usr/bin/env bash
# build.sh — release build of the churn binary.
#
# Usage: scripts/build.sh
# Produces ./churn.exe (trimpath, symbols stripped). The committed web/dist
# is embedded, so no Node toolchain is needed.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "==> building ./churn.exe (release: -trimpath -ldflags '-s -w')"
go build -trimpath -ldflags "-s -w" -o churn.exe ./cmd/churn
echo "==> done: ./churn.exe"

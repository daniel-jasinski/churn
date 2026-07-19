#!/usr/bin/env bash
# web-build.sh — rebuild the frontend bundle (web/dist).
#
# Usage: scripts/web-build.sh
# Runs `npm ci` if web/node_modules is missing (restores the pinned
# toolchain from package-lock.json), then the production build. Commit the
# resulting web/dist together with the src change — the freshness test in
# internal/server enforces it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../web"

if [ ! -d node_modules ]; then
  echo "==> web/node_modules missing: npm ci"
  npm ci
fi

echo "==> npm run build (esbuild → web/dist)"
npm run build
echo "==> done — remember to commit web/dist with your web/src change"

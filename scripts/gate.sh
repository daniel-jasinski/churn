#!/usr/bin/env bash
# gate.sh — the full quality gate (green before every commit).
#
# Usage: scripts/gate.sh
#   SKIP_RACE=1 scripts/gate.sh   # quick run: plain `go test` without -race
#
# Steps: gofmt -l (fails if any file needs formatting), go build, go vet,
# tests (-race needs cgo: mingw-w64 gcc is put on PATH), and golangci-lint
# when installed (warns and skips otherwise).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "==> gofmt"
unformatted="$(gofmt -l cmd internal web/embed.go)"
if [ -n "$unformatted" ]; then
  echo "$unformatted"
  echo "gate: gofmt found unformatted files (run: gofmt -w <file>)" >&2
  exit 1
fi

echo "==> go build ./..."
go build ./...

echo "==> go vet ./..."
go vet ./...

if [ "${SKIP_RACE:-0}" = "1" ]; then
  echo "==> go test ./...  (SKIP_RACE=1: no race detector)"
  go test ./...
else
  echo "==> go test -race ./...  (CGO via /c/dev/mingw64/bin)"
  PATH="/c/dev/mingw64/bin:$PATH" CGO_ENABLED=1 go test -race ./...
fi

if command -v golangci-lint >/dev/null 2>&1; then
  echo "==> golangci-lint run ./..."
  golangci-lint run ./...
else
  echo "==> golangci-lint not installed — SKIPPED (install it for the full gate)" >&2
fi

echo "==> gate green"

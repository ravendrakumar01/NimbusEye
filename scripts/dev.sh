#!/usr/bin/env bash
# Starts the API in mock mode and the Vite dev server, and stops both on Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "go not found on PATH" >&2; exit 1; }
[ -d web/node_modules ] || (cd web && npm install)

cleanup() { jobs -p | xargs -r kill 2>/dev/null || true; }
trap cleanup EXIT INT TERM

go run ./cmd/api --mock "$@" &
(cd web && npm run dev) &
wait

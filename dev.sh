#!/usr/bin/env bash
# Dev mode: backend (:8787) + traffic proxy (:8788) alongside the frontend
# next dev server (:5173). The frontend proxies /api to the backend; Ctrl-C
# stops both.
#
# For the single binary (embedded frontend), see the "Build from source"
# section of the README — that path does not use this script.
set -euo pipefail
cd "$(dirname "$0")"

# On exit, kill every child in this process group (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (plain go run, frontend not embedded); the number of concurrent
# worker agents is configured in System Settings.
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (Next dev server; /api is proxied to :8787).
( cd web && npm run dev ) &

echo "[dev] backend :8787 / proxy :8788 / frontend http://localhost:5173  (Ctrl-C to stop)"
wait

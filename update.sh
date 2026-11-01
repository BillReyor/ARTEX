#!/usr/bin/env bash
# ARTEX update script: (1) Docker update (rebuild the image)  (2) local build update
# Mirrors install.sh: install does the first-time setup, update upgrades to a
# newer version. DB migrations run automatically — artex re-runs schema.sql
# idempotently on every start (ADD COLUMN / CREATE INDEX IF NOT EXISTS), so a
# restart migrates. Data (the pgdata volume, ./data, ./skills) is untouched.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# -- optional: sync the repo to the latest code (compose / scripts / source) --
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "not a git working copy, skipping git pull"; return; }
  [ "$(ask 'Pull the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or a diverged branch) — resolve manually and retry; keeping the current code for now"
  fi
}

# -- (1) Docker update ---------------------------------
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose not found; deploy with ./install.sh first"
  [ -f .env ] || die ".env not found; run ./install.sh for the first-time setup"

  # Rebuild only artex from source: postgres is the fixed 16-alpine and does not
  # need to move. artex declares depends_on postgres, so a named `up` starts pg
  # if it is down and leaves a running one as-is.
  info "rebuilding the artex image from source..."
  docker compose build artex
  info "starting (artex migrates the schema on restart)..."
  docker compose up -d artex
  ok "update complete -> http://localhost:8787"
  info "view logs: docker compose logs -f artex"
  info "clean up old images (optional): docker image prune -f"
}

# -- (2) local build update ----------------------------
update_local(){
  command -v go >/dev/null 2>&1 || die "Go not found (>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found — for a first-time setup use ./install.sh instead"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "rebuilding the frontend static export..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "recompiling the single binary with embedded frontend..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm not found: building the backend WITHOUT an embedded frontend (run 'npm run dev' separately)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "build complete -> ./artex"
  warn "restart the running artex process to apply it (it migrates the schema on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (rebuild the image)"
echo "  2) Local update (go rebuild)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "invalid choice" ;;
esac

#!/usr/bin/env bash
# ARTEX installer: (1) all-Docker  (2) local build & run
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# -- docker detection / auto-install -------------------
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "docker and docker compose detected"; return
  fi
  warn "docker / docker compose not found"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask 'Install Docker automatically? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker installed (log out and back in to use it without sudo)"
      else
        die "please install docker and retry"
      fi ;;
    Darwin) die "on macOS install Docker Desktop: https://www.docker.com/products/docker-desktop/" ;;
    *)      die "please install docker and retry" ;;
  esac
}

# -- (1) all-Docker ------------------------------------
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres password (Enter to generate a random one)' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY (optional, can be set later in the UI)' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok ".env created (POSTGRES_PASSWORD set)"
  else
    info "using the existing .env"
  fi
  # No published image: build the artex image from source (first build takes
  # several minutes: Next.js export -> Go compile -> Playwright/Chromium).
  info "building the image and starting..."
  docker compose up -d --build
  ok "started -> http://localhost:8787"
  info "view logs: docker compose logs -f artex"
}

# -- (2) local build & run -----------------------------
install_local(){
  echo "Database option:"
  echo "  1) Connect to an existing PostgreSQL"
  echo "  2) Start a PostgreSQL with Docker (requires docker)"
  case "$(ask 'Choose' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres password (Enter for random)' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Database host' 127.0.0.1)"
      DB_PORT="$(ask 'Port' 5432)"
      DB_USER="$(ask 'User' artex)"
      DB_PASS="$(ask 'Password' '')"
      DB_NAME="$(ask 'Database name' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # generate config.json
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "config.json created"

  # Go check
  command -v go >/dev/null 2>&1 || die "Go not found; install Go (>=1.26): https://go.dev/dl/"
  ok "Go: $(go version)"

  # embedding the frontend needs node to produce the static export
  if command -v npm >/dev/null 2>&1; then
    info "building the frontend static export..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "compiling the single binary with embedded frontend..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm not found: building the backend WITHOUT an embedded frontend (run 'npm run dev' separately)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "build complete -> ./artex"

  info "starting... (Ctrl-C to stop)"
  ./artex
}

echo "=============================="
echo "  ARTEX install"
echo "  1) All-Docker install"
echo "  2) Local run (go build)"
echo "=============================="
case "$(ask 'Choose' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "invalid choice" ;;
esac

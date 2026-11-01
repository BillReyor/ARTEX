# syntax=docker/dockerfile:1
#
# Run the image (do not compile in the image): Only install commonly used tools and put in the **precompiled Linux single binary**.
# Binaries are cross-compiled by CI's binaries job (pure Go, no QEMU) and placed according to the target architecture
# dist/<TARGETARCH>/artex of the build context. In this way, when building multiple architectures, arm64 only needs to simulate the apt layer.
# No more simulating Next/Go compilation, much faster.
#
# When manually building an image locally, prepare the binary yourself first:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.15-slim-bookworm
ARG TARGETARCH
# Commonly used tools: ripgrep/curl/vim, add a batch of recon spare parts (add or delete as needed).
# Node installs 22.x from NodeSource: the apt nodejs that comes with bookworm is 18, and Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Playwright MCP and CLI (global) are pre-installed, and no npx network download is required when running.
# @playwright/mcp: browser MCP direct `npx @playwright/mcp` (installed globally, no need for -y/@latest).
# @playwright/cli: Provide playwright-cli. After installation, use --help to verify that it is executable.
# Then install playwright (which provides browser management). After installation, use --with-deps to preset chromium and its system dependencies.
# In this way, the MCP/CLI in the container is available for the first time, and the browser is no longer downloaded from the Internet.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Precompiled corresponding architecture binary (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Guardian startup script: After the process exits, press the exit code to decide whether to restart it. One-click page update relies on it to complete the change.
# It is also responsible for forwarding SIGTERM to artex - docker stop only sends the signal to PID 1,
# If it is not forwarded, artex will not be able to receive or shut down gracefully, and will be hard-killed by SIGKILL after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/(SQLite + jwt.key) persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]

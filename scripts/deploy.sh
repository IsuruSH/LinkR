#!/usr/bin/env bash
#
# Production deploy on a single host (EC2).
#
#   bash scripts/deploy.sh
#
# Images are built and pushed by CI (GitHub Actions -> GHCR); this host only
# PULLS them, so no compiler ever runs here — deploys take seconds and a 1 GB
# instance never OOMs mid-build.
#
# One-time on this host, for private GHCR images:
#   echo "$GHCR_TOKEN" | docker login ghcr.io -u <github-username> --password-stdin
# (a Personal Access Token with read:packages scope)

set -euo pipefail

# Run from the repo root regardless of where the script is invoked.
cd "$(dirname "$0")/.."

COMPOSE="docker compose -f docker-compose.prod.yml"

# .env is the single source of config (APP_ORIGIN, JWT_SECRET, POSTGRES_PASSWORD…).
if [[ ! -f .env ]]; then
  echo "ERROR: .env not found in $(pwd)." >&2
  echo "       cp .env.example .env  and set APP_ORIGIN + secrets, then re-run." >&2
  exit 1
fi

echo "==> [1/4] Pulling latest code"
# --ff-only refuses to create a surprise merge commit if local history diverged.
git pull --ff-only

echo "==> [2/4] Pulling images (app images from GHCR; postgres/redis/nginx official)"
# Fails with 'denied' if this host has not `docker login ghcr.io`'d for private
# images — see the header for the one-time login.
$COMPOSE pull

echo "==> [3/4] Starting / updating containers (waiting for healthchecks)"
$COMPOSE up -d --remove-orphans --wait

echo "==> [4/4] Pruning dangling images"
docker image prune -f

echo
echo "==> Done. Current status:"
$COMPOSE ps

#!/usr/bin/env bash
#
# Full production deploy on a single host (EC2).
#
#   bash scripts/deploy.sh
#
# Use this for a normal release, or after base images / dependencies change:
# it pulls code, refreshes the third-party images, rebuilds the app images,
# restarts everything, and reclaims disk from old layers.
#
# For a quicker app-only release that leaves the database and cache running,
# use scripts/update.sh instead.

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

echo "==> [1/5] Pulling latest code"
# --ff-only refuses to create a surprise merge commit if local history diverged.
git pull --ff-only

echo "==> [2/5] Refreshing third-party images (postgres, redis, nginx)"
# Only the image-based services. The app services are built below, so a bare
# 'compose pull' would just warn about them.
$COMPOSE pull postgres redis nginx

echo "==> [3/5] Building application images (backend, frontend)"
$COMPOSE build

echo "==> [4/5] Starting / updating containers (waiting for healthchecks)"
$COMPOSE up -d --remove-orphans --wait

echo "==> [5/5] Pruning dangling images"
docker image prune -f

echo
echo "==> Done. Current status:"
$COMPOSE ps

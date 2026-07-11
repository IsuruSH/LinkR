#!/usr/bin/env bash
#
# Minimal-downtime application update on a single host.
#
#   bash scripts/update.sh
#
# Rebuilds ONLY the backend and frontend, swaps them one at a time, and reloads
# Nginx so it picks up the new container IPs. Postgres and Redis are left running
# untouched — an app release never restarts the data layer.
#
# Honest about the limits: this is a FEW-SECONDS-PER-SERVICE gap, not true
# zero-downtime. On a single Compose host the old container stops before the new
# one is in rotation. True zero-downtime needs blue-green or an external load
# balancer that health-checks and drains connections — that is the natural next
# step when this migrates to ECS (a rolling deployment does exactly that).

set -euo pipefail

cd "$(dirname "$0")/.."

COMPOSE="docker compose -f docker-compose.prod.yml"

if [[ ! -f .env ]]; then
  echo "ERROR: .env not found in $(pwd)." >&2
  exit 1
fi

echo "==> [1/5] Pulling latest code"
git pull --ff-only

echo "==> [2/5] Building new application images (no disruption yet)"
# Built before anything is swapped, so a build failure never leaves the site down.
$COMPOSE build backend frontend

# Swap one service at a time, wait for its healthcheck, then reload Nginx before
# touching the next — so each service's gap is only its own recreate, and the two
# are never down together. --no-deps leaves postgres/redis (and their data) up.
# The reload re-resolves the recreated container's new IP without dropping the
# connections Nginx already has open.
reload_nginx() { $COMPOSE exec -T nginx nginx -s reload; }

echo "==> [3/5] Swapping backend"
$COMPOSE up -d --no-deps --wait backend
reload_nginx

echo "==> [4/5] Swapping frontend"
$COMPOSE up -d --no-deps --wait frontend
reload_nginx

echo "==> [5/5] Pruning dangling images"
docker image prune -f

echo
echo "==> Done. Current status:"
$COMPOSE ps

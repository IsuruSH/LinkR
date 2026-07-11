#!/usr/bin/env bash
#
# One-time TLS bootstrap: obtain the Let's Encrypt certificate so Nginx can serve
# HTTPS. Idempotent — if a real certificate already exists it does nothing, so
# deploy.sh can call it on every run.
#
#   bash scripts/init-tls.sh
#
# Prerequisites (once):
#   - DNS: an A record for $DOMAIN pointing at this host's public IP.
#   - Firewall: inbound 80 and 443 open.
#   - .env: DOMAIN and CERTBOT_EMAIL set (CERTBOT_STAGING=1 while testing, to
#     avoid Let's Encrypt's rate limits).

set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE="docker compose -f docker-compose.prod.yml"
CERT_NAME="linkr"                      # fixed name -> fixed path in nginx.conf
LIVE="/etc/letsencrypt/live/$CERT_NAME"

[[ -f .env ]] || { echo "ERROR: .env not found." >&2; exit 1; }

# Read a value from .env, stripping any inline comment and trailing space.
env_val() { grep -E "^$1=" .env | head -1 | cut -d= -f2- | sed 's/[[:space:]]*#.*$//; s/[[:space:]]*$//'; }
DOMAIN="$(env_val DOMAIN)"
EMAIL="$(env_val CERTBOT_EMAIL)"
STAGING="$(env_val CERTBOT_STAGING)"

[[ -n "$DOMAIN" ]] || { echo "ERROR: set DOMAIN in .env (e.g. parfumworld.store)." >&2; exit 1; }
[[ -n "$EMAIL"  ]] || { echo "ERROR: set CERTBOT_EMAIL in .env." >&2; exit 1; }

# --- Already have a real cert? Then there is nothing to do. -----------------
# The placeholder below is self-signed with CN=localhost; a real Let's Encrypt
# cert is not, so that distinguishes them.
if $COMPOSE run --rm --entrypoint sh certbot -c \
     "[ -f $LIVE/fullchain.pem ] && ! openssl x509 -in $LIVE/fullchain.pem -noout -subject | grep -q 'CN *= *localhost'" \
     >/dev/null 2>&1; then
  echo "==> TLS certificate already present for $CERT_NAME — nothing to do."
  exit 0
fi

echo "==> Bootstrapping TLS for $DOMAIN"

# 1. Placeholder self-signed cert, so Nginx can start on :443 (its config
#    references the cert path, which must exist).
echo "==> [1/5] Creating a temporary self-signed certificate"
$COMPOSE run --rm --entrypoint sh certbot -c "
  mkdir -p $LIVE &&
  openssl req -x509 -nodes -newkey rsa:2048 -days 1 \
    -keyout $LIVE/privkey.pem -out $LIVE/fullchain.pem -subj '/CN=localhost'"

# 2. Start the stack. Nginx comes up on :80 (serving the ACME challenge) and
#    :443 (with the placeholder).
echo "==> [2/5] Starting the stack"
$COMPOSE up -d --wait

# 3. Remove the placeholder so certbot writes a clean, managed certificate.
#    Nginx keeps serving the placeholder from memory until we reload in step 5.
echo "==> [3/5] Clearing the placeholder"
$COMPOSE run --rm --entrypoint sh certbot -c \
  "rm -rf /etc/letsencrypt/live/$CERT_NAME /etc/letsencrypt/archive/$CERT_NAME /etc/letsencrypt/renewal/$CERT_NAME.conf"

# 4. Request the real certificate over the HTTP-01 webroot challenge.
echo "==> [4/5] Requesting the certificate from Let's Encrypt"
staging_arg=""
[[ "$STAGING" == "1" ]] && { staging_arg="--staging"; echo "    (staging mode — this cert is NOT trusted by browsers)"; }
$COMPOSE run --rm --entrypoint certbot certbot certonly \
  --webroot -w /var/www/certbot \
  --cert-name "$CERT_NAME" -d "$DOMAIN" \
  --email "$EMAIL" --agree-tos --no-eff-email --non-interactive \
  $staging_arg

# 5. Reload Nginx to pick up the real certificate.
echo "==> [5/5] Reloading Nginx"
$COMPOSE exec -T nginx nginx -s reload

echo
echo "==> TLS is live. Visit https://$DOMAIN"

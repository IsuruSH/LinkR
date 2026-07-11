# Linkr

A URL shortener with click analytics. Create short links, share them, and see
how often each is clicked over time.

**Stack:** Go API · Next.js frontend · PostgreSQL · Redis · Nginx (production).

---

## Architecture

Five moving parts:

| Component | Role |
|---|---|
| **Frontend** (Next.js) | The dashboard UI, **and** a thin server-side proxy (BFF) that holds the session |
| **Backend** (Go) | The API: create/list/stats links, and the short-link redirects |
| **PostgreSQL** | Source of truth — users, links, clicks |
| **Redis** | Redirect cache (hot path) and rate-limit counters |
| **Nginx** | Production reverse proxy — the only public service |

### How a request flows

```
                        Browser
                           │
        ┌──────────────────┼─────────────────────────┐
        │ pages, /api/auth, /api/bff        /{code} (short-link redirect)
        ▼                                            ▼
   ┌──────────┐   server-side, Bearer token   ┌──────────────┐
   │ Frontend │ ────────────────────────────► │   Backend    │
   │ (Next.js)│                                │   (Go API)   │
   └──────────┘                                └──────┬───────┘
                                            ┌─────────┴─────────┐
                                            ▼                   ▼
                                       PostgreSQL             Redis
```

Three design points worth knowing up front:

- **Auth uses a BFF, not a token in the browser.** On login, the Go API returns
  a JWT; a Next.js route handler stores it in an **httpOnly cookie** the browser
  can't read, and every dashboard call goes through `/api/bff/*`, which attaches
  the token server-side. An XSS can't steal a session it can never see.

- **The redirect is the hot path.** `GET /{code}` resolves through Redis first
  (Postgres only on a miss, then back-fills the cache) and returns a `302`
  immediately. The click is recorded **asynchronously** — a bounded channel feeds
  a small worker pool that batches inserts — so recording never slows the redirect.

- **Short links live at the root** (`/gh-repo`), alongside the app's own pages
  (`/login`, `/dashboard`). A reserved-word list guarantees a short code can never
  collide with an app route, which is what lets one domain serve both in production.

---

## Local development

**Prerequisites:** Docker with the Compose plugin. Nothing else — no Go, no Node.

```bash
git clone <repo-url> linkr
cd linkr
cp .env.example .env      # defaults work as-is for local dev
docker compose up --build
```

That's it. On first boot the backend migrates the database and seeds a demo
account, so there's something to click immediately.

| | |
|---|---|
| **Dashboard** | http://localhost:3000 |
| **API / short links** | http://localhost:8080 |
| **Demo login** | `demo@linkr.dev` / `demo-password-123` |

Locally the frontend (`:3000`) and backend (`:8080`) run on separate ports and
the browser talks to both directly — Nginx is production-only. Try a seeded link:
http://localhost:8080/gh-repo, then reload its stats and watch the count move.

### Everyday commands

```bash
docker compose up --build        # start (rebuilds changed images)
docker compose logs -f           # tail logs
docker compose down              # stop (keeps data)
docker compose down -v           # stop and wipe the database/cache
```

### Tests

```bash
make test              # unit tests, race detector on, no services needed
make test-integration  # against a real Postgres + Redis
make lint              # go vet + gofmt
```

---

## Production (single EC2 host)

Production adds **Nginx** in front and keeps everything else private. It's still
Docker Compose — one host, one command — just a different compose file.

### Production architecture

```
                       Internet
                          │  :80 / :443
                    ┌─────▼─────┐
                    │   Nginx    │   ← the only service with published ports
                    └─────┬─────┘
          routes by path ─┤
     / /api /_next /login │ /l …        /{code}  /healthz  /readyz
                          ▼                       ▼
                   ┌────────────┐          ┌──────────────┐
                   │  Frontend  │──(BFF)──►│  Backend ×N   │  replicas,
                   │  (Next.js) │          │  (Go API)     │  load-balanced
                   └────────────┘          └──────┬───────┘
                                            ┌─────┴─────┐
                                        PostgreSQL     Redis
                            (all four internal-only, on the Docker network)
```

- **Only Nginx is exposed** (ports 80/443). Backend, frontend, Postgres and Redis
  are reachable only on the internal Docker network.
- **Nginx routes by specificity** (not a blunt `/api → backend`): the browser's
  `/api/auth` and `/api/bff` calls are Next.js handlers, so they go to the
  **frontend**; only bare short codes and the health probes go to the **backend**.
- **The backend scales horizontally.** Set `BACKEND_REPLICAS` and Nginx
  round-robins across the replicas (it re-resolves the service via Docker DNS, so
  a static upstream doesn't pin to one). Safe because the backend is stateless and
  migrations take a Postgres advisory lock.

### First-time setup on EC2

1. **Instance & firewall.** Launch an instance (Ubuntu). In its security group,
   allow inbound **22 (SSH)**, **80**, and **443** only.

2. **Install Docker + Compose:**
   ```bash
   curl -fsSL https://get.docker.com | sudo sh
   sudo usermod -aG docker $USER      # log out/in so docker runs without sudo
   ```
   The Compose plugin ships with Docker Engine; verify with `docker compose version`.

3. **Clone and configure:**
   ```bash
   git clone <repo-url> linkr && cd linkr
   cp .env.example .env
   ```
   Edit `.env` and set the four production values at the top:
   - `APP_ORIGIN` — your public URL (domain or the EC2 DNS), e.g.
     `http://ec2-1-2-3-4.compute.amazonaws.com`. The compose file derives every
     other URL from this, so you set it **once**.
   - `JWT_SECRET` — `openssl rand -base64 48`. The server refuses to start in
     production with the example value.
   - `POSTGRES_PASSWORD` — a real password.
   - `SEED_DEMO_DATA` — `true` for a demo, `false` for a clean install.

4. **Log in to the image registry (one-time).** The app images are built by CI
   and stored privately in GHCR, so the host authenticates once to pull them:
   ```bash
   echo "<GHCR_TOKEN>" | docker login ghcr.io -u <github-username> --password-stdin
   ```
   `<GHCR_TOKEN>` is a GitHub Personal Access Token with the `read:packages`
   scope. (Skip this if you made the images public.)

5. **Start it:**
   ```bash
   docker compose -f docker-compose.prod.yml up -d
   ```
   This **pulls** the images and starts everything — no build runs on the host.
   The app is now on `http://<APP_ORIGIN>/`.

> **Images are built in CI, not on the server.** GitHub Actions builds the
> backend and frontend on every push to `main` and pushes them to GHCR
> (`ghcr.io/<owner>/linkr-backend` / `-frontend`). The EC2 host only pulls, so a
> small instance never compiles (and never OOMs mid-build). To roll back, set
> `IMAGE_TAG` in `.env` to a specific commit SHA and re-run the deploy.

### Operating it

```bash
# View logs
docker compose -f docker-compose.prod.yml logs -f
docker compose -f docker-compose.prod.yml logs -f backend   # one service

# Restart a service
docker compose -f docker-compose.prod.yml restart backend

# Scale the backend (Nginx picks up the replicas automatically)
BACKEND_REPLICAS=3 docker compose -f docker-compose.prod.yml up -d
```

### Updating

Push to `main`, let CI build and push the new images, then on the host run one of
the scripts in `scripts/` (with `bash scripts/<name>.sh`):

- **`deploy.sh`** — full release: pull code, **pull** all images, restart, prune.
  Deploys take seconds because nothing is compiled on the host.
- **`update.sh`** — app-only, minimal downtime: pulls just the new backend +
  frontend images, swaps them one at a time, reloads Nginx, and leaves
  Postgres/Redis untouched.

### Backups

Postgres holds all the data (Redis is a rebuildable cache). Back it up with
`pg_dump` from the running container:

```bash
# Create a compressed dump on the host
docker compose -f docker-compose.prod.yml exec -T postgres \
  pg_dump -U linkr -d linkr | gzip > backup-$(date +%F).sql.gz

# Restore it
gunzip -c backup-2026-01-01.sql.gz | \
  docker compose -f docker-compose.prod.yml exec -T postgres psql -U linkr -d linkr
```

Automate it with cron (e.g. daily at 03:00, keeping the dumps off-box in S3):

```cron
0 3 * * * cd /home/ubuntu/linkr && docker compose -f docker-compose.prod.yml exec -T postgres pg_dump -U linkr -d linkr | gzip > /home/ubuntu/backups/linkr-$(date +\%F).sql.gz
```

The Postgres and Redis data live in named Docker volumes, so `docker compose down`
(without `-v`) never loses them.

### HTTPS

`nginx/nginx.conf` ships HTTP-only with a commented, ready-to-enable `443` block
and an ACME challenge location. To turn on TLS: point a DNS record at the host,
issue a certificate with certbot (webroot `/var/www/certbot`), then uncomment the
`443` server block and reload Nginx.

---

## Project structure

```
linkr/
├── docker-compose.yml          # dev stack (bare `docker compose up`)
├── docker-compose.prod.yml     # production stack (Nginx + replicas)
├── nginx/nginx.conf            # production reverse proxy
├── scripts/                    # deploy.sh, update.sh
├── .env.example                # single config source for both stacks
├── backend/                    # Go API (handlers → services → repos → domain)
└── frontend/                   # Next.js app + BFF proxy
```

Dependencies point inward: backend handlers never touch SQL, repositories never
touch HTTP, and the domain layer imports nothing but the standard library.

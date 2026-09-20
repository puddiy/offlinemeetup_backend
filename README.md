# Meetuper Backend

Backend for **Meetuper**, a mobile app for organizing, discovering, and joining offline meetups. A Go service exposing a **REST API** and a **real-time WebSocket** layer (chat + presence), built on a layered ("Clean") architecture with **PostGIS** geo-search, **Redis** for caching / pub-sub / presence, and **S3-compatible** object storage for media.

## Tech Stack

* **Language:** Go 1.26
* **HTTP Router:** [chi](https://github.com/go-chi/chi)
* **Database:** PostgreSQL 16 + **PostGIS** (spatial queries)
* **ORM / Query Builder:** [Bun](https://github.com/uptrace/bun)
* **Cache · Pub/Sub · Presence:** Redis
* **Object Storage:** S3-compatible (MinIO in dev)
* **Real-time:** WebSocket
* **Migrations:** [Goose](https://github.com/pressly/goose) — embedded, auto-applied on startup
* **Metrics:** Prometheus
* **Docs:** Swagger (swaggo)
* **Infra:** Docker, Docker Compose, GitHub Actions (CI/CD)

## Architecture

The project follows a layered architecture; dependencies are wired manually in `internal/app`. Request flow:

```
transport/http/handler  →  service          →  repo         →  Bun / Postgres
   (DTO ↔ domain)          (business logic)     (SQL)
```

* `cmd/app/` — Entry point: config load, DB/Redis/S3 wiring, migrations, HTTP server + WebSocket hub startup.
* `internal/transport/http/` — REST layer: router, handlers, middleware (auth, rate limiting, security headers), DTOs.
* `internal/transport/websocket/` — Real-time hub-and-client layer: chat, presence, multi-instance broadcast.
* `internal/service/` — Business logic. Each service declares the repository interface it depends on (the seam used for mocking).
* `internal/repo/` — Data access via Bun.
* `internal/domain/` — Core domain models.
* `internal/cache/` — Redis caching helpers (cache-aside, presence store).
* `internal/config/` — Environment-based configuration.

## Key Features

### Geo-search
* Radius search with distance ranking using PostGIS `ST_DWithin` / `ST_Distance`.
* **DaData** integration for address autocomplete and geocoding.

### Authentication & Authorization
* Three login methods: **Google ID Token**, **Telegram Login Widget**, and **email/password**.
* Short-lived **JWT access tokens** plus **rotating opaque refresh tokens** (only their SHA-256 hash is stored) with **reuse detection** — replaying a rotated token revokes all of that user's sessions.
* Fail-fast on missing secrets (`JWT_SECRET_KEY`, `TELEGRAM_BOT_TOKEN`, and — outside `local`/`dev` — the `MAIL_SMTP_*` relay credentials) in every environment.
* Per-request authorization (ownership, membership, capability tokens) enforced inside repository transactions.

#### Email/password flow
* **Register → verify → login.** `POST /v1/auth/register` takes `email`, `username`, `password` and always answers `202` with a `registration_id` (registering on an email that already has a Google/Telegram account is indistinguishable from a fresh one — no account-enumeration oracle). Nothing is written to `users` yet: the pending registration (password hash, confirmation-code hash, attempt count) lives in Redis for 15 minutes, keyed by `(email, registration_id)`. `POST /v1/auth/verify-email` takes that `registration_id` plus the 6-digit code emailed to the user, and either creates the account or attaches a password to the existing one, returning an access/refresh token pair. `POST /v1/auth/resend-code` re-sends the code for one `registration_id` (cooldown + hourly quota per email).
* **Why `registration_id`.** It scopes a registration attempt so two concurrent ones on the same email can't overwrite each other. With a single pending object per email, anyone could fire `/register` at an address mid-signup and swap in their own password hash; the victim then had two code emails in the inbox, and confirming the newer one created the account under the *attacker's* password. Attempts now live side by side and neither can see the other. The id is an addressing token, not a secret — the emailed code is still what proves ownership of the address.
* **Account linking is symmetric.** Registering with a password on an email that already has a social account attaches the password to it, and a social login on an email that already has a password account links that provider to it (`social_accounts`, unique on `(provider, social_id)`) rather than trying to create a second `users` row. Linking only ever keys on a **provider-verified** email — Google's `email_verified` claim is required, and Telegram supplies no email at all.
* **Login.** `POST /v1/auth/login` takes a single `login` field (email or username — chosen by the presence of `@`) plus `password`. An unknown login and a wrong password return the identical error, and a dummy bcrypt comparison runs even when the login doesn't exist, so response timing can't reveal whether an account exists.
* **Forgot / reset password.** `POST /v1/auth/forgot-password` always answers `202` regardless of whether the account exists; if it does, a reset code is emailed. `POST /v1/auth/reset-password` takes the code and a new password, and — like a password change — revokes every refresh token for that user, so a stolen session doesn't survive a reset.
* **Change password.** `PATCH /v1/auth/password` (authenticated) takes the current and new password and, like reset, revokes all other sessions.
* Passwords are hashed with **bcrypt** (cost 12); the hash lives in its own `user_credentials` table, never on the `users` row returned by authenticated requests.
* Outbound mail goes through a `Mailer` seam, and **the presence of `MAIL_SMTP_HOST` picks the implementation**, not `APP_ENV`: unset ⇒ `logMailer` (logs the code instead of sending — see `make logs`), set ⇒ a real SMTP relay (`github.com/wneessen/go-mail`, via `MAIL_SMTP_HOST/PORT/USER/PASSWORD/FROM`). Outside `local`/`dev` the relay credentials are required, so the relay is always used there; inside them, filling the vars in is how you smoke-test real delivery without flipping `APP_ENV` (which would also turn off dev-login and Swagger). Sends happen off the request goroutine, so a relay failure doesn't block the `202`; failures are tracked via the `mail_send_failures_total` Prometheus counter.
* The mailer speaks **STARTTLS on the submission port** (`MAIL_SMTP_PORT`, default `587`). An implicit-TLS port (465) is not supported without a code change. Whatever relay you point it at, the sending domain in `MAIL_FROM` needs SPF/DKIM/DMARC records or the confirmation codes land in spam.

### Real-time chat (WebSocket)
* Hub-and-client model: send / edit / delete messages, replies, read receipts, typing indicators.
* **Horizontally scalable across instances** via Redis Pub/Sub fan-out — the database is the source of truth and clients backfill history over REST on reconnect.
* Ordered per-connection event processing; every spawned goroutine is panic-isolated.
* **Idempotent send** via a client-supplied `request_id`.

### Presence
* Online / last-seen state backed by Redis. Offline↔online transitions are computed **atomically with Lua** so racing instances never double-notify; a TTL heartbeat lets a crashed instance's connections decay to offline.

### Media uploads
* Upload-then-reference flow for covers, avatars, and chat attachments; files stored in S3.
* MIME **validated by real bytes** (never the client filename), gated to a media whitelist.
* **Server-side metadata extraction**: duration (audio/video) and dimensions (images/video); an audio-only MP4 is corrected from `video/mp4` to `audio/mp4`.
* Covers/avatars are image-only; max size configurable (default 100 MB).

### Caching & resilience
* Redis cache-aside with **singleflight** (anti-stampede), **TTL jitter** (anti-avalanche), and timeout-bounded ops that degrade to the loader when Redis is slow — all instrumented with Prometheus.
* **Distributed fixed-window rate limiter** (Redis) on the auth and upload routes.
* Security headers, request body limit, and a configurable WebSocket origin allowlist.

### Observability & Ops
* `GET /health` liveness check and `GET /metrics` (Prometheus).
* Migrations are embedded and applied automatically on startup — there is no separate migrate step.
* CI/CD via GitHub Actions (Docker build/push, deploy).

## Getting Started

### Prerequisites
* Docker & Docker Compose
* Go 1.26 (for running without Docker)
* Make

### Configuration
Create a `.env` file in the project root. `DB_DSN`, `JWT_SECRET_KEY`, and `TELEGRAM_BOT_TOKEN` are **required** — the app refuses to start without them. Outside `local`/`dev`, `MAIL_SMTP_HOST/USER/PASSWORD/FROM` are required too; in `local`/`dev` they may be left empty and mail is logged instead of sent. `MAIL_SMTP_PORT` always defaults to `587`.

```env
APP_PORT=9090
APP_ENV=local            # local | dev | prod

# Database (PostGIS)
DB_DSN=postgres://user:password@localhost:5432/meetuper_db?sslmode=disable
POSTGRES_USER=user
POSTGRES_PASSWORD=password
POSTGRES_DB=meetuper_db

# Redis
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# Object storage (S3 / MinIO)
S3_ENDPOINT=http://localhost:9000
S3_REGION=us-east-1
S3_BUCKET=meetuper
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_PUBLIC_URL=http://localhost:9000/meetuper

# Auth
JWT_SECRET_KEY=your_secret_key
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=720h
GOOGLE_WEB_CLIENT_ID=your_google_client_id
TELEGRAM_BOT_TOKEN=your_telegram_bot_token

# Email/password auth — outbound mail.
# Setting MAIL_SMTP_HOST switches from the logging mailer to a real relay in
# ANY environment; outside local/dev the relay vars are required.
# STARTTLS only — port 465 (implicit TLS) is not supported.
# Gmail works for low volume: host smtp.gmail.com, user = the full address,
# password = a Google App Password (not the account password; needs 2FA).
# Caveats: ~500 mails/day, and Gmail rewrites MAIL_FROM to the authenticated
# address unless the alias is verified under "Send mail as".
MAIL_SMTP_HOST=smtp.example.com
MAIL_SMTP_PORT=587               # optional (default shown)
MAIL_SMTP_USER=your_smtp_user
MAIL_SMTP_PASSWORD=your_smtp_password
MAIL_FROM=noreply@example.com
MAIL_SEND_TIMEOUT=10s            # optional (default shown)
EMAIL_CODE_TTL=15m               # optional (default shown)
EMAIL_CODE_MAX_ATTEMPTS=5        # optional (default shown)
EMAIL_RESEND_COOLDOWN=60s        # optional (default shown)
EMAIL_SEND_QUOTA_PER_HOUR=5      # optional (default shown)

# External APIs
DADATA_TOKEN=your_dadata_api_key

# Optional (defaults shown)
MAX_UPLOAD_SIZE=104857600         # 100 MB
PRESENCE_TTL=2m
CACHE_TIMEOUT=200ms
TRUST_PROXY_HEADERS=false          # trust X-Forwarded-For/X-Real-IP only behind a trusted proxy
WS_ALLOWED_ORIGINS=http://localhost:3000

# Admin panel
ADMIN_SESSION_TTL=8h              # session idle window (extended on each request)
ADMIN_SESSION_MAX_TTL=24h         # absolute cap from login time
```

### Running with Docker (recommended)
Spin up PostgreSQL (PostGIS), Redis, MinIO, and the app:

```bash
make up
```

Migrations apply automatically on startup. The API is available at `http://localhost:9090` (Swagger at `/swagger/index.html`).

### Running locally (development)
1. Start the infrastructure containers only:
   ```bash
   docker-compose -f docker-compose.dev.yml up -d meetuper_db meetuper_redis minio
   ```
2. Run the app (migrations apply automatically on startup):
   ```bash
   REDIS_ADDR=localhost:6379 make run
   ```

> `make migration NAME=...` only **scaffolds** a new migration file — it does not run migrations. Applying them happens on every startup.

## API Documentation

Swagger UI (enabled in `local`/`dev`): `http://localhost:9090/swagger/index.html`. Regenerate after changing handlers:

```bash
make swag
```

The WebSocket endpoint is `GET /v1/ws` (JWT `Bearer` auth); it carries chat and presence events.

## Testing

```bash
make test   # unit tests with the race detector (the codebase is heavily concurrent)
make lint   # golangci-lint
```

Service-layer tests use gomock with miniredis for an in-memory Redis.

## Makefile Commands

| Command | Description |
|---|---|
| `make up` / `down` / `restart` | Start / stop / restart the Docker dev environment |
| `make logs` / `logs-app` | Tail container logs (all / app only) |
| `make run` | Run the app locally (needs reachable DB/Redis) |
| `make build` | Compile the binary to `bin/meetuper` |
| `make test` | Run unit tests with the race detector |
| `make lint` | Run golangci-lint |
| `make swag` | Regenerate Swagger docs |
| `make migration NAME=xxx` | Scaffold a new SQL migration |
| `make clean` | Stop containers and remove volumes (drops DB data) |

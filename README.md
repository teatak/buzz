# Buzz

Buzz is a self-hosted LLM API proxy with multi-user API keys, provider key routing, failover, automatic bad-key disabling, and a web admin UI.

[简体中文](README.zh-CN.md)

## Features

- Web admin UI for users, user API keys, providers, provider keys, models, and runtime status.
- User-facing API keys sent with `Authorization: Bearer <api-key>`.
- Provider key routing with retry, cooldown, failover, and request counting.
- Automatically disables invalid/suspended upstream keys on 400 API key invalid, 401, and 403 responses.
- Postgres-backed users, providers, keys, and models; Redis-backed admin sessions and runtime state.

## Architecture Docs

- [Canonical protocol task](docs/canonical-protocol-task.zh-CN.md): passthrough-first protocol conversion plan.
- [Model preset catalog](docs/model-presets.zh-CN.md): verified models, token limits, and protocol requirements.

## Quick Install

```bash
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/main/install.sh | sh
```

Then open:

```text
http://<server-ip>:9622/admin/
```

Optional:

```bash
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/main/install.sh | env INSTALL_DIR=/opt/buzz PORT=9622 IMAGE=teatak/buzz:latest sh
```

Run the same command again to refresh the installer files. The installer keeps `.env`, `config.yaml`, `./pgdata`, and `./redisdata`.

The installer writes a small `makefile` into the install directory:

```bash
make upgrade
make logs
make restart
make stop
```

## Docker Compose

Create `config.yaml`:

```yaml
server:
  addr: 0.0.0.0:9622
```

Create `docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: buzz
      POSTGRES_USER: buzz
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-buzz-change-me}
    volumes:
      - ./pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U buzz -d buzz"]
      interval: 10s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - ./redisdata:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

  buzz:
    image: ${IMAGE:-teatak/buzz:latest}
    restart: unless-stopped
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    ports:
      - "${PORT:-9622}:9622"
    environment:
      TZ: ${TZ:-Asia/Singapore}
      BUZZ_DATABASE_URL: postgres://buzz:${POSTGRES_PASSWORD:-buzz-change-me}@postgres:5432/buzz?sslmode=disable
      BUZZ_REDIS_ADDR: redis:6379
    volumes:
      - ./config.yaml:/config/config.yaml:ro
```

Start:

```bash
docker compose up -d
```

Upgrade:

```bash
make upgrade
```

For local source builds, use [docker-compose.dev.yml](docker-compose.dev.yml).

## Local Development

```bash
make dev
```

Admin UI:

```text
http://127.0.0.1:9622/admin/
```

Public API:

```text
GET  http://127.0.0.1:9622/v1/models
POST http://127.0.0.1:9622/v1/chat/completions
POST http://127.0.0.1:9622/v1/responses
POST http://127.0.0.1:9622/v1/messages
POST http://127.0.0.1:9622/v1beta/models/{model}:generateContent
POST http://127.0.0.1:9622/v1beta/models/{model}:streamGenerateContent
```

Buzz exposes OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, and Gemini GenerateContent-compatible endpoints. Put the user-visible Buzz model name in the request model field or Gemini URL path; the backend routes it to the configured provider route.

## API Protocols

Client-facing endpoints:

| Protocol | Endpoint |
| --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Gemini GenerateContent | `POST /v1beta/models/{model}:generateContent` |
| Gemini StreamGenerateContent | `POST /v1beta/models/{model}:streamGenerateContent` |
| OpenAI-compatible models list | `GET /v1/models` |

Provider endpoints can be configured per provider protocol:

| Provider protocol | Typical base URL |
| --- | --- |
| `openai` | `https://api.openai.com/v1` |
| `openai-responses` | `https://api.openai.com/v1` |
| `anthropic` | `https://api.anthropic.com` |
| `gemini` | `https://generativelanguage.googleapis.com` |

Routing is passthrough-first: when the inbound protocol and provider protocol match, Buzz forwards the original request. When they differ, Buzz converts through its internal canonical protocol layer. Core text, image, basic tool call, usage, and text streaming paths are covered; advanced streamed tool deltas, hosted tools, file input, and reasoning/thinking content streaming are future enhancements.

On first launch, create the initial admin user in the admin UI. Then create user API keys in the UI and pass them as:

```http
Authorization: Bearer <api-key>
```

## Admin Frontend

```bash
cd admin
pnpm install
pnpm build
```

Frontend dev server:

```bash
make admin-dev
```

## Models And Providers

Models and providers are managed in the admin UI:

- Models: user-visible models, with preset import and per-model route management in the model detail view.
- Providers: upstream providers, with preset import. The DeepSeek preset uses the official `https://api.deepseek.com` base URL.
- Provider Keys: upstream API keys directly attached to providers; Ollama / keyless providers are not a current target.

The old `gemini-auto` cross-model fallback and public Gemini native proxy have been removed. Each model only rotates through its own routes.

## Build And Publish

```bash
make docker-build
```

Release flow:

```bash
make version-patch
go test ./...
cd admin && pnpm run build
cd ..
git add VERSION
git commit -m "chore: bump version"
git push
make docker-publish
```

`make docker-publish` publishes the current committed `VERSION` as both `latest` and that version for `linux/amd64` and `linux/arm64`. It does not change `VERSION`.

Useful helpers:

```bash
make version-patch
make version-minor
make version-major
```

## Notes

- The Go server serves `admin/dist` by default.
- Admin sessions are stored in Redis for 7 days and renew when they have 3 days or less remaining. Without Redis config, local source runs fall back to the database.
- `config.yaml`, database files, and built frontend assets are intentionally ignored.

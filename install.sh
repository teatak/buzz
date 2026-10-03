#!/bin/sh
set -eu

INSTALL_DIR="${INSTALL_DIR:-$PWD}"
REQUESTED_IMAGE="${IMAGE:-}"
REQUESTED_PORT="${PORT:-}"
REQUESTED_POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
IMAGE="${REQUESTED_IMAGE:-teatak/buzz:latest}"
PORT="${REQUESTED_PORT:-9622}"
POSTGRES_PASSWORD="$REQUESTED_POSTGRES_PASSWORD"

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required. Install Docker first: https://docs.docker.com/engine/install/"
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose plugin is required. Install Docker Compose first."
  exit 1
fi

if [ "$(id -u)" -eq 0 ]; then
  mkdir -p "$INSTALL_DIR"
  chown "$(id -u):$(id -g)" "$INSTALL_DIR"
elif mkdir -p "$INSTALL_DIR" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1; then
  sudo mkdir -p "$INSTALL_DIR"
  sudo chown "$(id -u):$(id -g)" "$INSTALL_DIR"
else
  echo "sudo is required to create $INSTALL_DIR. Run as root or set INSTALL_DIR to a writable path."
  exit 1
fi

cd "$INSTALL_DIR"

if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

if [ -n "$REQUESTED_IMAGE" ]; then
  IMAGE="$REQUESTED_IMAGE"
fi
if [ -n "$REQUESTED_PORT" ]; then
  PORT="$REQUESTED_PORT"
fi
if [ -n "$REQUESTED_POSTGRES_PASSWORD" ]; then
  POSTGRES_PASSWORD="$REQUESTED_POSTGRES_PASSWORD"
fi

if [ -z "$POSTGRES_PASSWORD" ]; then
  if command -v openssl >/dev/null 2>&1; then
    POSTGRES_PASSWORD="$(openssl rand -hex 16)"
  else
    POSTGRES_PASSWORD="buzz-change-me"
  fi
fi

cat > .env <<EOF
IMAGE=${IMAGE}
PORT=${PORT}
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
EOF
chmod 600 .env

cat > config.yaml <<EOF
server:
  addr: 0.0.0.0:9622
EOF
chmod 644 config.yaml

cat > docker-compose.yml <<EOF
services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: buzz
      POSTGRES_USER: buzz
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
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
    image: \${IMAGE}
    restart: unless-stopped
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    ports:
      - "\${PORT}:9622"
    environment:
      BUZZ_DATABASE_URL: postgres://buzz:\${POSTGRES_PASSWORD}@postgres:5432/buzz?sslmode=disable
      BUZZ_REDIS_ADDR: redis:6379
    volumes:
      - ./config.yaml:/config/config.yaml:ro
EOF

cat > makefile <<'EOF'
G = $(shell printf "\033[0;32m")
C = $(shell printf "\033[0m")

default:
	@docker compose -f docker-compose.yml up -d
	@echo "$(G)[OK]$(C)"

upgrade:
	@docker compose -f docker-compose.yml pull
	@docker compose -f docker-compose.yml up -d
	@echo "$(G)[OK]$(C)"

restart:
	@docker compose -f docker-compose.yml restart
	@echo "$(G)[OK]$(C)"

stop:
	@docker compose -f docker-compose.yml down
	@echo "$(G)[OK]$(C)"

logs:
	@docker compose -f docker-compose.yml logs -f buzz
EOF

docker compose pull
docker compose up -d

echo
echo "Buzz is running."
echo "Install dir: $INSTALL_DIR"
echo "Open: http://<server-ip>:${PORT}/admin/"

# Buzz

Buzz 是一个自托管 LLM API 代理，支持多用户 API Key、提供方 Key 路由、失败重试、故障切换、异常 Key 自动停用和 Web 管理后台。

[English](README.md)

## 功能

- Web 管理后台：用户、用户 API Keys、提供方、提供方 Keys、模型和运行状态。
- 对外使用用户 API Key，通过 `Authorization: Bearer <api-key>` 访问。
- 提供方 Key 路由：支持重试、冷却、故障切换和请求计数。
- 上游 Key 异常时自动停用：包括 400 API key invalid、401、403。
- Postgres 持久化用户、提供方、Keys 和模型；Redis 保存管理后台会话和短期运行态。

## 架构文档

- [Canonical 协议转换层任务](docs/canonical-protocol-task.zh-CN.md)：透传优先的协议转换层计划。
- [模型预设清单](docs/model-presets.zh-CN.md)：已核验模型、token 上限与协议要求。

## 快速安装

```bash
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/main/install.sh | sh
```

然后打开：

```text
http://<服务器 IP>:9622/admin/
```

可选参数：

```bash
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/main/install.sh | env INSTALL_DIR=/opt/buzz PORT=9622 IMAGE=teatak/buzz:latest sh
```

再次运行同一条命令会刷新安装文件。安装脚本会保留 `.env`、`config.yaml`、`./pgdata` 和 `./redisdata`。

安装脚本会在安装目录写入一个小 `makefile`：

```bash
make upgrade
make logs
make restart
make stop
```

## Docker Compose

创建 `config.yaml`：

```yaml
server:
  addr: 0.0.0.0:9622
```

创建 `docker-compose.yml`：

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

启动：

```bash
docker compose up -d
```

升级：

```bash
make upgrade
```

本地源码构建使用 [docker-compose.dev.yml](docker-compose.dev.yml)。

## 本地开发

```bash
make dev
```

管理后台：

```text
http://127.0.0.1:9622/admin/
```

公开 API：

```text
GET  http://127.0.0.1:9622/v1/models
POST http://127.0.0.1:9622/v1/chat/completions
POST http://127.0.0.1:9622/v1/responses
POST http://127.0.0.1:9622/v1/messages
POST http://127.0.0.1:9622/v1beta/models/{model}:generateContent
POST http://127.0.0.1:9622/v1beta/models/{model}:streamGenerateContent
```

Buzz 对外支持 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 和 Gemini GenerateContent 兼容接口。请求中的 `model` 字段或 Gemini URL 路径中的模型名填写用户可见的 Buzz 模型名，后端再路由到配置好的 provider route。

## API 协议

客户端入口：

| 协议 | 地址 |
| --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Gemini GenerateContent | `POST /v1beta/models/{model}:generateContent` |
| Gemini StreamGenerateContent | `POST /v1beta/models/{model}:streamGenerateContent` |
| OpenAI-compatible 模型列表 | `GET /v1/models` |

Provider endpoint 可按协议单独配置：

| Provider 协议 | 常见 Base URL |
| --- | --- |
| `openai` | `https://api.openai.com/v1` |
| `openai-responses` | `https://api.openai.com/v1` |
| `anthropic` | `https://api.anthropic.com` |
| `gemini` | `https://generativelanguage.googleapis.com` |

路由策略是透传优先：入口协议和 provider 协议一致时直接透传；不一致时通过内部 Canonical 协议层转换。当前已覆盖普通文本、图片、基础工具调用、usage 统计和文本流式转换；流式工具调用增量、hosted tools、file input、reasoning/thinking 内容流式回传属于后续增强。

首次启动时，在管理后台创建初始管理员。之后在 UI 中创建用户 API Key，并这样调用：

```http
Authorization: Bearer <api-key>
```

## 前端管理后台

```bash
cd admin
pnpm install
pnpm build
```

前端开发：

```bash
make admin-dev
```

## 模型和提供方

模型和提供方都在管理后台配置：

- Models：用户可见模型，支持从预设批量添加，并在模型详情中管理 routes。
- Providers：上游提供方，支持从预设添加。DeepSeek 预设使用官方推荐的 `https://api.deepseek.com`。
- Provider Keys：上游 API Keys，直接归属 provider；Ollama / 无 Key provider 暂不作为当前目标。

旧的 `gemini-auto` 跨模型降级和公开 Gemini native proxy 已移除；每个模型只在自己的 routes 内轮询。

## 构建和发布

```bash
make docker-build
```

发布流程：

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

`make docker-publish` 会发布当前已提交的 `VERSION`，同时打 `latest` 和版本号标签，默认包含 `linux/amd64`、`linux/arm64`。它不会修改 `VERSION`。

常用命令：

```bash
make version-patch
make version-minor
make version-major
```

## 说明

- Go 服务默认提供 `admin/dist`。
- Docker 安装下管理员会话保存在 Redis，有效期 7 天；剩余 3 天以内时自动续期。本地源码未配置 Redis 时回退到数据库。
- `config.yaml`、数据库文件和前端构建产物已加入忽略列表。

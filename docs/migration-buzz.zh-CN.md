# 从 BuzzHive 迁移到 Buzz

适用于原仓库安装脚本 / Compose 的单机部署：Postgres 16、Redis 7，数据分别挂载 `./pgdata`、`./redisdata`。改名版本为 **0.1.21**。表结构没有因改名改变，用户、API Key、提供方、模型、额度和历史记录通过数据库备份恢复保留。

本次服务器迁移由管理员手动执行。新目录、新数据库和新 Redis 副本与原数据分开，原目录保留以便回退。预计停机时间取决于数据量和磁盘速度；先拉镜像再停服务可缩短停机。不要对现有数据直接运行新版 `install.sh`，它是新安装入口。

## 1. 准备（旧服务继续运行）

以下命令使用 Bash，以能够管理 Docker 和复制数据文件的用户执行；按实际位置修改前两行。若原服务名、数据挂载、Postgres 版本或 Compose 项目名不同，先按实际部署调整命令。不要直接套用到外部数据库、共享 Redis 或 Swarm 部署。

```bash
OLD_DIR=/opt/buzzhive
NEW_DIR=/opt/buzz
set -euo pipefail
umask 077

# NEW_DIR 必须是尚未使用的新目录，避免覆盖已有数据。
test -d "$OLD_DIR"
test ! -e "$NEW_DIR"
mkdir -p "$NEW_DIR/migrations"
cd "$OLD_DIR"
docker compose -f docker-compose.yml ps
# 如原部署使用 -p，下面每条旧部署 docker compose 命令都必须加同一个 -p。
# 记录旧镜像和 Compose 配置供回退，不要把含密码的输出贴到公开地方。
docker inspect --format '{{.Config.Image}} {{.Image}}' "$(docker compose -f docker-compose.yml ps -q buzzhive)" > "$NEW_DIR/previous-image.txt"

cp -p .env config.yaml "$NEW_DIR/"
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/v0.1.21/docker-compose.yml -o "$NEW_DIR/docker-compose.yml"
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/v0.1.21/scripts/migrations/buzz-model-icons.sql -o "$NEW_DIR/migrations/buzz-model-icons.sql"
curl -fsSL https://raw.githubusercontent.com/teatak/buzz/v0.1.21/scripts/migrations/buzz-redis.lua -o "$NEW_DIR/migrations/buzz-redis.lua"

cd "$NEW_DIR"
# 只替换镜像设置，原 POSTGRES_PASSWORD、PORT 等设置保留。
sed '/^IMAGE=/d' .env > .env.new
printf '\nIMAGE=teatak/buzz:0.1.21\n' >> .env.new
mv .env.new .env
chmod 600 .env
docker compose -p buzz -f docker-compose.yml config --quiet
docker compose -p buzz -f docker-compose.yml pull
```

检查新目录中的 `config.yaml` 和 Compose 自定义项。非默认配置需逐项合并：

| 旧名称 | 新名称 |
| --- | --- |
| `teatak/buzzhive` | `teatak/buzz` |
| 服务名 / 二进制 `buzzhive` | `buzz` |
| 数据库 / 数据库用户 `buzzhive` | `buzz` |
| `BUZZHIVE_DATABASE_URL` | `BUZZ_DATABASE_URL` |
| `BUZZHIVE_REDIS_URL`、`ADDR`、`PASSWORD`、`DB` | 相应的 `BUZZ_REDIS_*` |
| 测试变量 `BUZZHIVE_TEST_DATABASE_URL` | `BUZZ_TEST_DATABASE_URL` |

数据库密码不改动。新 Compose 中 `BUZZ_DATABASE_URL` 会覆盖 YAML 数据库地址；如原来使用外部 Redis URL，不要留下指向旧 Redis 的设置。不要批量替换密码、用户创建的模型名或上游 URL。原安装脚本生成的 `makefile` 不要复制，新目录使用文中的 Compose 命令。

## 2. 停写并备份

此处开始停机，先等正在执行的模型请求结束。

```bash
cd "$OLD_DIR"
docker compose -f docker-compose.yml stop -t 120 buzzhive
# 标准原安装的数据库和用户均为 buzzhive。
docker compose -f docker-compose.yml exec -T postgres pg_dump -U buzzhive -d buzzhive -Fc > "$NEW_DIR/database.dump"
test -s "$NEW_DIR/database.dump"
# 停止 Redis 后复制整个目录，保留 AOF 清单、权限和 TTL。
docker compose -f docker-compose.yml stop redis
cp -a "$OLD_DIR/redisdata" "$NEW_DIR/redisdata"
# 旧 Postgres 和原 pgdata 不删除、不改名。
```

## 3. 恢复到新数据库

```bash
cd "$NEW_DIR"
docker compose -p buzz -f docker-compose.yml up -d --wait postgres redis
docker compose -p buzz -f docker-compose.yml exec -T postgres pg_restore --exit-on-error --no-owner --no-acl -U buzz -d buzz < database.dump
# 将已保存的 Buzz 图标标识迁移一次，不改用户命名。
docker compose -p buzz -f docker-compose.yml exec -T postgres psql -v ON_ERROR_STOP=1 -U buzz -d buzz < migrations/buzz-model-icons.sql
# 将复制的 Redis 中 bh: 键改为 buzz:，保留内容、类型和过期时间。
# 只在本实例专用 Redis 上执行；若目标键已存在则报错且不改任何键。
docker compose -p buzz -f docker-compose.yml exec -T redis redis-cli EVAL "$(cat migrations/buzz-redis.lua)" 0
```

Redis 返回迁移键数量（空库为 0）。看到任何错误都应停在此处检查，不要启动新服务。数据库恢复失败时，不要反复向部分恢复的库导入；原数据仍在原目录，可以先回退。

比较新旧核心实体数量；只显示计数，不输出密钥：

```bash
COUNTS='SELECT '\''users'\'', count(*) FROM users UNION ALL SELECT '\''user_api_keys'\'', count(*) FROM user_api_keys UNION ALL SELECT '\''providers'\'', count(*) FROM providers UNION ALL SELECT '\''provider_keys'\'', count(*) FROM provider_keys UNION ALL SELECT '\''models'\'', count(*) FROM models UNION ALL SELECT '\''usage_logs'\'', count(*) FROM usage_logs ORDER BY 1;'
cd "$OLD_DIR"
docker compose -f docker-compose.yml exec -T postgres psql -At -U buzzhive -d buzzhive -c "$COUNTS" > "$NEW_DIR/counts-before.txt"
cd "$NEW_DIR"
docker compose -p buzz -f docker-compose.yml exec -T postgres psql -At -U buzz -d buzz -c "$COUNTS" > counts-after.txt
diff counts-before.txt counts-after.txt

docker compose -p buzz -f docker-compose.yml up -d --wait buzz
docker compose -p buzz -f docker-compose.yml ps
# 9622 替换为原 .env 的 PORT（若不是默认值）。
curl --fail http://127.0.0.1:9622/health
```

## 4. 域名与验收

现有入口是 `https://buzzhive.home.teatak.com:8443/admin/`。在 DNS、反向代理和 TLS 证书中添加 `buzz.home.teatak.com`，仍代理到原服务端口，然后访问 `https://buzz.home.teatak.com:8443/admin/`。新域名 HTTPS 正常前保留旧入口。

- 登录管理后台，核对用户、模型、提供方、上游 Key、额度和历史记录；账户密码和 API Key 不变。
- 浏览器存储键已改名，且新域名有独立存储，需要重新登录并选择语言。
- 用现有用户 API Key 调用 `/v1/models`，并完成一次正常模型请求。
- Pudding 中已保存的接口地址不会自动改变。把原地址的主机名手动改为 `buzz.home.teatak.com`，保留 `:8443` 及原协议路径。
- 客户端尚未更新时，旧 API 域名可暂时代理到同一个 Buzz 服务；不要用网页重定向替代模型 API 代理。所有客户端更新并验证后再移除旧域名。
- 验证完成再停止旧 Postgres；旧目录与备份保留至确认无需回退。不要执行 `down -v` 或删除 `pgdata`。

## 5. 回退

在新服务尚未承接真实写入时，停止新服务并启动原服务，原数据无须恢复：

```bash
cd "$NEW_DIR"
docker compose -p buzz -f docker-compose.yml stop
cd "$OLD_DIR"
docker compose -f docker-compose.yml up -d --wait
```

如果已经在新服务创建用户、改配置或产生用量，不能直接回到旧副本，否则会丢失迁移后的记录。先停止新服务、另做新 Postgres 和 Redis 备份，再安排数据回迁。反向代理同步指回恢复后的服务；原镜像和原配置保持不变。

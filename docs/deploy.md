# Job Center 部署文档（P8）

对照：job-docs `ac06ab5` `architecture.md` §7 / `protocol-http.md` §1。

## 1. 拓扑

```
nginx :80 ─┬─ /                 → GlassAdmin 静态
           ├─ /backend/         → glassadmin PHP-FPM
           ├─ /jc/sse/          → job-center :8081（仅 SSE GET + ticket）
           └─ /v1/executor/ws   → job-center :8080（WebSocket）
job-center :8081 管理 API 仅 compose 内网（GlassAdmin / 发布脚本）
mysql:8.0 —— job_center / job_admin（独立账号）
```

## 2. Docker Compose

路径：`deploy/docker-compose.yml`、`deploy/nginx/default.conf`、`deploy/mysql/init/01-databases.sh`、`deploy/.env.example`、仓库根 `Dockerfile`。

```bash
cd deploy
cp .env.example .env   # 改密码与密钥
docker compose up -d --build
```

**本机（开发盒子）无 Docker，Compose 在 P8 标记为未实测。** 有 Docker 的环境按上面命令验证。

容器必须设置 `TZ`（默认 `Asia/Shanghai`）：调度默认时区 = Job Center 服务器本地时区。

## 3. 本机直接跑（当前联调方式）

依赖：本机 MySQL（例 `127.0.0.1:3307`）、`configs/local.yaml`。

```bash
go build -o /tmp/job-center-server ./cmd/server
/tmp/job-center-server -config configs/local.yaml
# Executor WS: 127.0.0.1:18080
# Admin HTTP:  127.0.0.1:18081
curl -s http://127.0.0.1:18081/health/live
curl -s http://127.0.0.1:18081/health/ready
```

发布登记示例：`scripts/register-deployment.sh`（HMAC 客户端 `deployer`）。

## 4. 健康检查

| 路径 | 含义 |
|------|------|
| `/health/live` | 进程存活 |
| `/health/ready` | MySQL Ping；失败 503 |
| `/metrics` | 表行数 / 字节 / 归档滞后天数（Admin 端口，Prometheus text） |

## 5. 配置要点

- `security.admin_clients`：`glassadmin`（需 Operator）、`deployer`（发布登记）
- `archive.dir` / `daily_at` / retention days
- `http.executor_addr` / `http.admin_addr` 必须分端口

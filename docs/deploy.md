# Job Center 部署说明

完整部署手册见 `GlassJob/job-docs` 仓库 `deployment.md`。

## 1. 网关拓扑

```
nginx :80 ─┬─ /                 → GlassAdmin 静态资源
           ├─ /backend/         → GlassAdmin PHP-FPM（内网通过 JC_ADMIN_* 调 job-center:8081）
           ├─ /jc/sse/          → job-center :8081（仅放行管理端 SSE GET + ?ticket=）
           ├─ /v1/trigger/      → job-center :8081（外部业务触发、查询、换票、取消与 SSE，Client HMAC 或 ?ticket=）
           ├─ /v1/executor/ws   → job-center :8080（Executor WebSocket）
           └─ /v1/*             → 404（屏蔽 /v1/admin/* 直接公网暴露）
mysql:8.0 —— job_center / job_admin（独立数据库与最小权限账号）
```

## 2. 克隆与裸机运行

```bash
git clone https://github.com/GlassJob/job-center.git
cd job-center
go mod download
go build -o bin/job-center ./cmd/server

cp configs/config.example.yaml configs/local.yaml
# 编辑 configs/local.yaml 中的 mysql.dsn、credential_pepper、admin_clients（或执行 ./scripts/init-local.sh）
./bin/job-center -config configs/local.yaml
```

## 3. Docker Compose 启动

路径：`deploy/docker-compose.yml`、`deploy/nginx/default.conf`、`deploy/mysql/init/01-databases.sh`、`deploy/.env.example`、仓库根 `Dockerfile`。

```bash
cd deploy
cp .env.example .env   # 修改密码与密钥（>=16 字节）
docker compose up -d --build
```

容器必须设置 `TZ`（默认 `Asia/Shanghai`）：调度默认时区等于 Job Center 服务器本地时区。

## 4. 健康检查与指标

| 路径 | 含义 |
|------|------|
| `/health/live` | 进程存活（`8080` / `8081` 均提供） |
| `/health/ready` | MySQL 连通性探测，失败返回 `503` |
| `/metrics` | 表行数、表字节数、归档滞后天数（Admin 端口，Prometheus 文本格式） |

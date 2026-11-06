# GlassJob — Job Center (`job-center`)

独立可运行的分布式任务调度中心（Go 单二进制，内嵌 MySQL 自动迁移、WebSocket JSON Schema 校验与离线 Swagger UI，无任何外部基准框架依赖）。

## 快速启动（克隆即跑）

前置条件：Go 1.27+、MySQL 8.0+（已创建空库，例如 `CREATE DATABASE job_center CHARACTER SET utf8mb4;`）。

```bash
git clone https://github.com/GlassJob/job-center.git
cd job-center

# 1. 安装依赖
go mod download

# 2. 生成本地配置 configs/local.yaml（可通过 JC_DB_DSN 指定数据库连接串）
JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" ./scripts/init-local.sh

# 3. 启动服务（启动时自动执行 migrations/*.sql 建表，自动加载 configs/local.yaml）
go run ./cmd/server
```

或直接使用 `Makefile`：

```bash
JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" make run
```

启动后默认监听：
- **Executor WebSocket 端口**（`:8080`）：`ws://127.0.0.1:8080/v1/executor/ws`、`/health/live`、`/health/ready`
- **管理与外部触发 API 端口**（`127.0.0.1:8081`）：
  - 离线交互文档：`http://127.0.0.1:8081/swagger/`（内置本地 HMAC + Operator 自动签名栏）
  - OpenAPI 规范：`http://127.0.0.1:8081/openapi.yaml`
  - Prometheus 指标：`http://127.0.0.1:8081/metrics`

## 纯环境变量运行（无需配置文件）

```bash
JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" \
JC_CREDENTIAL_PEPPER="replace-with-at-least-16-byte-random-pepper" \
JC_ADMIN_CLIENT_SECRETS="glassadmin:replace-with-at-least-16-byte-secret" \
go run ./cmd/server
```

## Docker Compose 启动

```bash
cd deploy
cp .env.example .env   # 按需修改数据库密码与密钥
docker compose up -d
```

## 目录说明

- `cmd/server`：服务入口
- `configs/config.example.yaml`：完整配置项模板
- `migrations/`：内嵌 SQL 迁移脚本（进程启动时幂等自动迁移）
- `internal/protocol/schemas/`：内嵌 WebSocket 消息 JSON Schema
- `internal/transport/http/swagger/`：内嵌离线 Swagger UI 与 `openapi.yaml`
- `deploy/`：Docker Compose、MySQL 初始化与 Nginx 反代示例
- `docs/`：部署、运维与排障手册

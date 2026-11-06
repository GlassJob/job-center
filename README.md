# GlassJob — Job Center (`job-center`)

独立可运行的分布式任务调度中心（Go 单二进制，内嵌 MySQL 自动迁移、WebSocket JSON Schema 校验与离线 Swagger UI，无任何外部基准框架依赖）。

## 快速启动（克隆、编译、配模板直接运行）

前置条件：Go 1.27+、MySQL 8.0+（已创建空库，例如 `CREATE DATABASE job_center CHARACTER SET utf8mb4;`）。

### 方式一：基于 `config.example.yaml` 手动配置并编译运行

```bash
git clone https://github.com/GlassJob/job-center.git
cd job-center

# 1. 下载依赖并编译二进制
go mod download
go build -o bin/job-center ./cmd/server

# 2. 复制配置模板并修改 mysql.dsn、security.credential_pepper、security.admin_clients
cp configs/config.example.yaml configs/local.yaml

# 3. 启动服务（进程启动时自动幂等执行 migrations/*.sql 建表）
./bin/job-center -config configs/local.yaml
```

### 方式二：一键自动生成随机密钥配置并启动

```bash
JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" ./scripts/init-local.sh
go run ./cmd/server
# 或直接：JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" make run
```

启动后默认监听：
- **Executor WebSocket 端口**（`:8080`）：`ws://127.0.0.1:8080/v1/executor/ws`、`/health/live`、`/health/ready`
- **管理与外部触发 API 端口**（`127.0.0.1:8081`）：
  - 离线交互文档：`http://127.0.0.1:8081/swagger/`（内置本地 HMAC + Operator 自动签名栏）
  - OpenAPI 规范：`http://127.0.0.1:8081/openapi.yaml`
  - Prometheus 指标：`http://127.0.0.1:8081/metrics`

## 纯环境变量运行（无需配置文件，适合容器化）

```bash
JC_DB_DSN="job_center:password@tcp(127.0.0.1:3306)/job_center" \
JC_CREDENTIAL_PEPPER="replace-with-at-least-16-byte-random-pepper" \
JC_ADMIN_CLIENT_SECRETS="glassadmin:replace-with-at-least-16-byte-secret" \
./bin/job-center
```

## Docker 与 Docker Compose 启动

```bash
# 单镜像构建（无需本地存在 configs/local.yaml）
docker build -t glassjob/job-center:dev .

# 全栈 Docker Compose（要求同级目录已克隆 GlassJob/job-admin）
cd deploy
cp .env.example .env   # 修改数据库密码与 HMAC/Pepper 密钥
docker compose up -d --build
```

## 常用测试与构建命令

```bash
go test ./...              # 单元测试 + 集成测试（无测试库时集成用例自动 skip）
make build                 # 编译到 ./bin/job-center
```

## 目录说明

- `cmd/server`：服务入口
- `configs/config.example.yaml`：完整配置项模板
- `migrations/`：内嵌 SQL 迁移脚本（进程启动时幂等自动迁移）
- `internal/protocol/schemas/`：内嵌 WebSocket 消息 JSON Schema
- `internal/transport/http/swagger/`：内嵌离线 Swagger UI 与 `openapi.yaml`
- `deploy/`：Docker Compose（`JC_ADMIN_*` 环境变量对齐）、MySQL 初始化与 Nginx 反代（放行 `/v1/trigger/` 与 `/jc/sse/`，屏蔽 `/v1/admin/`）
- `docs/`：部署、运维与排障说明

# P1 阶段报告：Job Center 执行器网关

**仓库**：[GlassJob/job-center](https://github.com/GlassJob/job-center)（private，`main`）  
**范围**：任务书第 16 章 — 认证、注册、心跳、实例生命周期（SUSPECT/OFFLINE）、会话替换、版本可查。  
**库名**：仅 `job_center`（已清理 `glassjob_center` 与临时 `job_center_p0_*`）。

## 交付内容

| 模块 | 说明 |
|------|------|
| `cmd/server` | 进程入口：迁移 → HTTP（执行器/管理）→ WS 挂载 → 实例监视 |
| `internal/transport/websocket` | `jobcenter.v1` 子协议；auth / register / heartbeat / unregister；session_epoch fencing；替换踢旧连接（close 4003） |
| `internal/transport/http` | `/health/live|ready`；只读 `GET /v1/services|tasks|…/instances`（P1 观测用） |
| `internal/application/registration` | 凭据校验、注册 upsert、DEFAULT 调度不覆盖 `config_source=ADMIN`、心跳、注销 |
| `internal/application/monitoring` | 心跳超时 ONLINE→SUSPECT→OFFLINE |
| `internal/application/catalog` | 服务/凭据创建（给测试与后续 Admin API） |
| `migrations/0001_init.sql` | 全量表结构（嵌入迁移） |
| `tests/integration` | 端到端 WS 验收（见下） |

## 验收结果（本地 MySQL `127.0.0.1:3307` / 库 `job_center`）

`go test ./tests/integration/ -count=1` **7/7 PASS**：

1. `auth_ok` — 合法凭据拿到 `session_epoch`
2. `auth_bad_token` — 错误凭据 close `4001` / `AUTH_INVALID_CREDENTIAL`
3. `register_and_heartbeat` — 注册 + 心跳；Admin 只读接口可查 `app_version` / `git_commit` / `ONLINE`
4. `reregister_preserves_schedule` — `config_source=ADMIN` 的 cron/enabled 不被重新注册覆盖
5. `multi_instance` — 同服务两实例同时 ONLINE
6. `session_replace` — 同 `instance_id` 新连替换旧连，epoch 递增
7. `suspect_offline` — 断连后进入 SUSPECT 并落到 OFFLINE

单元测试：`go test ./...` 通过（含 protocol / config / domain / security）。

## 本地运行

```bash
# 需 MySQL 8，库/用户 job_center（见 deploy/）
cp configs/config.example.yaml configs/local.yaml   # 填 DSN 与 pepper（≥16 字节）
go run ./cmd/server -config configs/local.yaml
# 执行器 WS: ws://127.0.0.1:18080/v1/executor/ws  (Sec-WebSocket-Protocol: jobcenter.v1)
```

`configs/local.yaml` / `configs/.pepper` 已 gitignore，勿提交。

## 已知边界（P1 不做）

- 任务下发 / lease / result_ack / 取消（P2）
- Admin 写接口与 HMAC 鉴权完整面（后续；P1 仅健康检查 + 只读观测）
- 未与真实 SDK 联调（等本推送后由 Executor 接）

## 给 Executor / Admin

- 协议与 schema 以内嵌 `internal/protocol/schemas` 为准（与 job-docs 同步过的 WS 契约）
- 重新注册：**不会**覆盖 `job_schedule.config_source='ADMIN'` 的 cron/enabled
- `instance_id` schema 要求 **≥ 8** 字符

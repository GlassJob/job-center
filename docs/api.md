# Job Center 接口文档

统一入口。联调集合：`docs/bruno/`（Admin HMAC 自动签名；executor 含 **WS** 请求 `executor/ws.bru`）、`docs/postman/`（Admin 子集）。

| 通道 | 路径 / 文档 | 状态 |
|------|-------------|------|
| Health | `GET /health/live` · `GET /health/ready` | 可用 |
| Admin HTTP | `/v1/admin/*` | Phase A 可用 |
| Executor **WS 1.1** | `GET /v1/executor/ws` | **主路径**，见 [protocol-websocket-v1.1.md](./protocol-websocket-v1.1.md) |
| Admin executions | `GET /executions` · `GET|POST .../jobs/:id/executions` · `GET .../logs` · `POST .../cancel` | 列表 / 触发 / 详情 / 日志 / 取消 |
| Admin overview / jobs / instances | `GET /overview` · `GET /jobs` · `GET /instances` | 概览 · 全局任务 · 全局实例 |
| Go SDK | 仓库根目录 `job-executor-go/` | 协议 1.1 + cancel |

冒烟：`scripts/ws_smoke.py`。

---

## 通用约定

- 时间：UTC；JSON 时间多为 RFC3339 / 协议 `TimeFormat`
- Admin 响应信封：`{ "code": 0, "message": "ok", "data": ... }`；业务错误 `code ≠ 0`
- Admin 鉴权头：`X-JC-Client-Id` / `X-JC-Timestamp` / `X-JC-Nonce` / `X-JC-Operator-Id` / `X-JC-Operator-Name` / `X-JC-Signature`
- Canonical：`METHOD\nPATH\nQUERY\nClient-Id\nTimestamp\nNonce\nOperator-Id\nOperator-Name\nhex(sha256(body))`
- 服务业务编码字段名：`code`（表列与 JSON 均已统一，不再使用 `service_code`）

---

## Admin：服务凭据（与 Executor 相关）

| 方法 | 路径 | 权限 |
|------|------|------|
| GET | `/v1/admin/services/:serviceId/credentials` | `job.credential.view` |
| POST | `/v1/admin/services/:serviceId/credentials` | `job.credential.manage` |
| GET | `/v1/admin/services/:serviceId/credentials/:credentialId` | `job.credential.view` |
| PATCH | `/v1/admin/services/:serviceId/credentials/:credentialId` | `job.credential.manage` |
| POST | `.../credentials/:credentialId/rotate` | `job.credential.manage` |
| DELETE | `.../credentials/:credentialId`（`?hard=true` 硬删） | `job.credential.manage` |

### Create body

```json
{
  "name": "executor-default",
  "allowed_job_codes": ["demo.*"],
  "allowed_ips": ["127.0.0.1", "10.0.0.0/8"],
  "expires_at": null
}
```

- `allowed_job_codes`：`null`=本服务全部 Job；数组可含前缀通配
- `allowed_ips`：`null`/省略=不限制；否则 IP 或 CIDR，用于 **WS HMAC 鉴权**
- 响应 `data` 含一次性 `secret`（WS 签名用）及兼容字段 `token`；视图字段含 `allowed_ips`，**不含** `secret_hash` / `secret_enc`

### Patch body

```json
{
  "name": "executor-default",
  "allowed_job_codes": ["demo.*"],
  "clear_allowed_job_codes": false,
  "allowed_ips": ["127.0.0.1"],
  "clear_allowed_ips": false,
  "expires_at": null,
  "clear_expires_at": true
}
```

`clear_allowed_*` / `clear_expires_at` 用于显式清空。

Rotate 会重新生成 `secret`/`secret_enc`/`secret_hash`；旧行无 `secret_enc` 的凭据需 Rotate 或重建后才能走 WS HMAC。

其余 Admin 资源（services / clients / jobs / states / schedules / audits）见 Bruno 目录；行为与 Phase A 一致。

---

## Admin：执行器实例（只读）

| 方法 | 路径 | 权限 |
|------|------|------|
| GET | `/v1/admin/instances` | `job.instance.view` |
| GET | `/v1/admin/services/:serviceId/instances` | `job.instance.view` |
| GET | `/v1/admin/services/:serviceId/instances/:instanceId` | `job.instance.view` |

List query：`page` / `limit` / `status`（`ONLINE`\|`OFFLINE`\|`DRAINING`\|`SUSPECT`\|`REGISTERING`）/ `keyword`（匹配 id、instance_key、hostname、ip）/ 全局列表另支持 `service_id`。

响应字段要点：`instance_key`、`status`、`running_count` / `max_concurrency`、`last_heartbeat_at`、`jobs[]`（`job_id` / `job_code` / `handler_version`）；全局列表含 `service_code` / `service_name`。  
实例由 Executor WS 自注册，本轮不提供创建/踢线等写接口。

---

## Admin：全局任务 / 概览 / 服务列表

| 方法 | 路径 | 权限 |
|------|------|------|
| GET | `/v1/admin/jobs` | `job.job.view` |
| GET | `/v1/admin/overview` | `job.service.view` |
| GET | `/v1/admin/scheduler` | `job.schedule.view` |
| POST | `/v1/admin/scheduler/regenerate` | `job.schedule.manage` |
| GET | `/v1/admin/triggers` | `job.schedule.view` |
| GET | `/v1/admin/services` | `job.service.view` |

- `GET /jobs` query：`keyword` / `service_id` / `status` / `enabled` / `page` / `limit`。列表项在 `JobView` 上含 `service_code`/`service_name`、`schedules[]` 摘要、`last_execution_id/status/at`。
- `GET /overview`：`service_count` / `job_count` / `enabled_job_count` / `instance_counts` / `execution_today` / `upcoming[]` / `recent_failed[]`。
- `GET /scheduler`：三循环工作状态 `claim` / `generate` / `queued`（`phase`：`IDLE`\|`RUNNING`\|`SLEEPING`\|`DISABLED`\|`STOPPED`，含 `detail` / 最近起止时间）。当前无 Prometheus metrics。
- `POST /scheduler/regenerate`：全平台 enabled+cron 排班**先删再插**（同步），返回 `total` / `succeeded` / `failed` / `errors[]`。
- `GET /triggers` query：`schedule_id` / `job_id` / `service_id` / `status` / `fire_from` / `fire_to` / `page` / `limit`。默认按 `fire_at ASC`；列表项含 schedule/job/service 摘要。
- `GET /services` 每条直接带 `job_count` / `instance_count` / `online_instance_count` / `credential_count`（与详情一致）。

---

## Executor WebSocket（主路径）

详见 **[protocol-websocket-v1.1.md](./protocol-websocket-v1.1.md)**。

摘要：

1. `GET /v1/executor/ws`，子协议 `jobcenter.v1`
2. `auth.request` HMAC（secret 不进帧）+ 可选凭据 `allowed_ips`
3. `service.register` → jobs + 调度策略 C（`default_cron`）
4. `service.heartbeat` / `service.unregister`
5. 断连 → `OFFLINE`；同 `instance_key` 顶号 → close `4003`

---

## Admin：手动执行

| 方法 | 路径 | 权限 |
|------|------|------|
| GET | `/v1/admin/executions` | `job.job.view` |
| GET | `/v1/admin/services/:serviceId/jobs/:jobId/executions` | `job.job.view` |
| POST | `/v1/admin/services/:serviceId/jobs/:jobId/executions` | `job.job.manage` |
| GET | `/v1/admin/services/:serviceId/jobs/:jobId/executions/:executionId` | `job.job.view` |
| GET | `/v1/admin/services/:serviceId/jobs/:jobId/executions/:executionId/logs` | `job.job.view` |
| POST | `/v1/admin/services/:serviceId/jobs/:jobId/executions/:executionId/cancel` | `job.job.manage` |

执行列表 query：`keyword` / `service_id` / `job_id` / `status` / `instance_id` / `created_from` / `created_to` / `page` / `limit`。  
列表项在 `ExecutionView` 上含 `job_name` / `service_code` / `service_name`（列表省略大体量 `params`/`result`）。  
日志 query：`after`（sequence 游标）/ `limit` / `attempt_id`；Executor `task.log` 写入 `execution_logs`。  
执行详情中的 `attempt` 含日志统计：物理列 `log_{info,warn,error}_{collected,dropped}`；虚拟列 `log_warn_count` / `log_error_count`（= collected + dropped）。

Create body：

```json
{
  "params": { "name": "x" },
  "idempotency_key": "optional-unique-key",
  "instance_id": "optional-prefer-instance"
}
```

响应：`execution_id` / `status`（`DISPATCHING` / `QUEUED` / `FAILED`）/ `attempt_id` / `instance_id`。  
有 ONLINE 且已 register 该 job、有容量的实例时同步下发 `task.execute`；有 ONLINE 但暂无空位 → `QUEUED`（QueuedScanner 补派）；**无 ONLINE** → 直接 `FAILED`（`NO_ONLINE_INSTANCE`）。

### Scheduler / Triggers

配置项 `scheduler.*`（默认开启）：`generate_interval=24h` / `horizon=168h`（7 天）/ `claim_lead=1s` / `queue_interval` / `claim_batch` / `queue_batch`。

| 循环 | 行为 |
|------|------|
| Generate | 日扫：**增量**从 `horizon_at`（或 now）补到 `now+horizon`；Create/改 cron·时区/启用/手动 regenerate 走**先删再插** |
| Claim | 有 PENDING：睡到最早 `fire_at`（提前 `claim_lead`）；无 PENDING：只等 `WakeClaim`（不扫库）；due 时 `SKIP LOCKED` → `CronTrigger` |
| QueuedScanner | 扫 `executions.status=QUEUED`，有容量则 `dispatch` |

单排班触点：

| 方法 | 路径 | 权限 |
|------|------|------|
| GET | `/v1/admin/services/:serviceId/jobs/:jobId/schedules/:scheduleId/triggers` | `job.schedule.view` |
| POST | `/v1/admin/services/:serviceId/jobs/:jobId/schedules/:scheduleId/regenerate` | `job.schedule.manage` |

- 单排班 triggers query 同全局（无 `schedule_id`/`job_id`/`service_id`，由路径固定）。
- `POST .../regenerate`：该排班先删再插；未启用/无 cron → 400。

手工验收：启 cron schedule → triggers 立刻出现 → 到期建 execution；无 ONLINE 手动触发 → FAILED；有 ONLINE 打满并发 → QUEUED → 腾空位后被扫走；`POST /scheduler/regenerate` 后近端 PENDING 重新对齐。

Cancel body 可选：`{ "reason": "..." }`。

| 状态 | 取消行为 |
|------|----------|
| `QUEUED` / `PENDING` | 直接 `CANCELLED` |
| `DISPATCHING` / `RUNNING` | `cancel_state` → `REQUESTED`/`SENT`，下发 `task.cancel`；收到 `cancelled` 后终态 |
| 已终态 | HTTP 409 |
| 重复取消 | 幂等 OK |

---

## 相关配置

```yaml
security:
  timestamp_skew_seconds: 300
websocket:
  auth_timeout: 10s
  ping_timeout: 60s
  max_message_bytes: 1048576
  send_queue_size: 64
  heartbeat_interval: 10s
  lease_ttl: 30s
scheduler:
  enabled: true
  generate_interval: 24h
  claim_lead: 1s
  queue_interval: 2s
  horizon: 168h
  claim_batch: 50
  queue_batch: 50
```

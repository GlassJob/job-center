# Job Center 接口文档

统一入口。联调集合：`docs/bruno/`（Admin HMAC 自动签名；executor 含 **WS** 请求 `executor/ws.bru`）、`docs/postman/`（Admin 子集）。

| 通道 | 路径 / 文档 | 状态 |
|------|-------------|------|
| Health | `GET /health/live` · `GET /health/ready` | 可用 |
| Admin HTTP | `/v1/admin/*` | Phase A 可用 |
| Executor **WS 1.1** | `GET /v1/executor/ws` | **主路径**，见 [protocol-websocket-v1.1.md](./protocol-websocket-v1.1.md) |
| Admin executions | `POST|GET .../executions` · `POST .../cancel` | 手动触发 / 查询 / 取消 |
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
| POST | `/v1/admin/services/:serviceId/jobs/:jobId/executions` | `job.job.manage` |
| GET | `/v1/admin/services/:serviceId/jobs/:jobId/executions/:executionId` | `job.job.view` |
| POST | `/v1/admin/services/:serviceId/jobs/:jobId/executions/:executionId/cancel` | `job.job.manage` |

Create body：

```json
{
  "params": { "name": "x" },
  "idempotency_key": "optional-unique-key",
  "instance_id": "optional-prefer-instance"
}
```

响应：`execution_id` / `status`（`DISPATCHING` 或 `QUEUED`）/ `attempt_id` / `instance_id`。  
有 ONLINE 且已 register 该 job、有容量的实例时同步下发 `task.execute`；否则 `QUEUED`（本轮无后台扫队）。

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
```

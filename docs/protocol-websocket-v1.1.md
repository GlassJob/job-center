# Executor WebSocket 协议 1.1

总览与 Admin 接口见 [api.md](./api.md)。

## 连接

- 路径：`GET /v1/executor/ws`
- 子协议：`Sec-WebSocket-Protocol: jobcenter.v1`
- 帧：UTF-8 JSON 文本（不接受 binary）

## Envelope

```json
{
  "protocol_version": "1.1",
  "message_id": "m_…",
  "type": "auth.request",
  "request_id": "可选，回包回显为响应 request_id",
  "timestamp": "2006-01-02T15:04:05.000Z07:00",
  "trace_id": "可选",
  "data": {}
}
```

本轮消息：

| type | 方向 | 说明 |
|------|------|------|
| `auth.request` / `auth.response` | E→C / C→E | HMAC 鉴权换会话 |
| `service.register` / `service.register_ack` | E→C / C→E | 注册 jobs + 调度 C |
| `service.heartbeat` / `service.heartbeat_ack` | E→C / C→E | 心跳（须已 register） |
| `service.unregister` | E→C | 进入 DRAINING；随后可关连接 |
| `task.execute` | C→E | 派发执行（字段 `job_code`） |
| `task.execute_ack` / `task.started` / `task.progress` / `task.completed` / `task.failed` | E→C | 执行回传 |
| `task.cancel` | C→E | Admin 取消下发 |
| `task.cancel_ack` / `task.cancelled` | E→C | 取消确认 / 已停止 |
| `task.result_ack` | C→E | 中心确认（`OK` / `REJECTED`） |
| `task.sync_running` / `task.sync_running_ack` | E→C / C→E | 重连后同步本机仍在跑的 attempts 并续租 |
| `task.log` | E→C | 落库 `execution_logs`；幂等 seq；回填 `acked_through_seq`；帧可带 `dropped_*` |
| `task.checkpoint` | E→C | 写入 `job_states`（key=`default`） |
| `system.error` / `system.notice` | C→E | 错误 / 顶号通知 |

未实现：`CANCEL_UNCONFIRMED` 超时回收、LOST 扫描重派。

应用层关闭码：`4001` 鉴权失败、`4002` 鉴权超时、`4003` 会话被顶、`4004` 协议不支持、`4005` 服务禁用。

---

## 鉴权 HMAC

### `auth.request` data（secret **不进帧**）

| 字段 | 类型 | 说明 |
|------|------|------|
| `method` | string | 默认 `hmac`；预留 `mtls_token` |
| `code` | string | 服务业务编码 |
| `credential_id` | string | 凭据 UUID |
| `instance_key` | string | 执行器侧稳定实例键（≤128） |
| `timestamp` | string | Unix 秒（十进制字符串） |
| `nonce` | string | 随机串，进程内防重放 |
| `signature` | string | hex(HMAC-SHA256) |
| `protocol_versions` | string[] | 须含 `1.1`（兼容 `1` / `1.0`） |
| `sdk.language` / `sdk.version` | string | SDK 标识 |

Canonical（`\n` 换行）：

```
AUTH
{code}
{credential_id}
{instance_key}
{timestamp}
{nonce}
```

`signature = hex(HMAC-SHA256(secret, canonical))`

校验：

1. `method` 默认 `hmac`
2. 时间窗与 Admin 相同（`security.timestamp_skew_seconds`）
3. nonce 进程内防重放
4. 凭据 ACTIVE/ROTATING、服务 ACTIVE、未过期，且库中有 `secret_enc`
5. 凭据级 `allowed_ips`（NULL=不限制；否则匹配对端 IP/CIDR）
6. 成功后 upsert 实例骨架并 `session_epoch+1`

### `auth.response` data

| 字段 | 说明 |
|------|------|
| `ok` | 是否成功 |
| `session_id` / `session_epoch` / `instance_id` | 会话 |
| `protocol_version` | 协商结果，当前 `1.1` |
| `server_time` | 服务端时间 |
| `heartbeat_interval_seconds` / `lease_ttl_seconds` / `max_message_bytes` | 来自 `websocket` 配置 |
| `error.code` / `error.message` | 失败时 |

常见错误码：`AUTH_INVALID_CREDENTIAL`、`AUTH_IP_DENIED`、`AUTH_SERVICE_DISABLED`、`PROTOCOL_UNSUPPORTED`。

鉴权成功后同连接内心跳/注销不再带密钥；同 `instance_key` 新连接 auth 成功会顶掉旧连接（`system.notice` + close `4003`）。未 auth 只接受 `auth.request`；超时关连接 `4002`。

---

## 注册与调度策略 C

### `service.register` data

`service.register` data 字段如下。

| 字段 | 说明 |
|------|------|
| `code` | 须与 auth 的服务一致 |
| `instance_key` | 须与 auth 一致 |
| `environment` | 默认 `dev` |
| `host.hostname` / `host.ip` / `host.pid` | 可选 |
| `runtime.language` | **必填**；`runtime.version` 可选 |
| `deployment.version` / `git_commit` / `build_time` | 可选 |
| `capacity.max_concurrency` | ≥1，默认 1 |
| `sdk_version` / `started_at` | 可选 |
| `jobs[]` | 见下表 |

`jobs[]` 元素：

| 字段 | 说明 |
|------|------|
| `job_code` | 必填，`^[a-z0-9][a-z0-9._:-]{0,127}$` |
| `job_name` | 默认=job_code |
| `handler` / `handler_version` | version 必填，semver 风格 |
| `default_cron` | 可选，**6 段**（含秒） |
| `timeout_seconds` / `max_concurrency` | 可选 |
| `params_schema` | JSON Schema object，可选 |
| `description` | 可选 |

### `service.register_ack` data

| 字段 | 说明 |
|------|------|
| `ok` | 整体是否接受 |
| `instance_id` / `instance_key` / `session_id` / `session_epoch` / `instance_status` | |
| `accepted_jobs[]` | `{ job_code, job_id }` |
| `rejected_jobs[]` | `{ job_code, code, message }` |
| `warnings[]` | 如 `DEFAULT_CRON_CONFLICT`、`INVALID_DEFAULT_CRON` |
| `error` | 整体失败时 |

策略 C：

- Job **尚无 default schedule** 且 `default_cron` 合法 → 插入 `schedules`：`code=default`，`config_source=DEFAULT`，`enabled=true`
- 已存在 `DEFAULT` 且 cron 与上报不一致 → `warnings` 含 `DEFAULT_CRON_CONFLICT`，**不覆盖**
- `config_source=ADMIN` 的 default **不碰**
- 非法 cron → `INVALID_DEFAULT_CRON` 警告，不插 schedule

---

## 心跳 / 注销 / 断连

### `service.heartbeat` data

```json
{ "status": "ONLINE", "running": 0, "max_concurrency": 2 }
```

`status` 为 `DRAINING`（大小写不敏感）时标记排空。须已 register。

### `service.heartbeat_ack` data

```json
{ "server_time": "…", "instance_status": "ONLINE" }
```

### `service.unregister` data

```json
{ "reason": "optional", "instance_key": "optional echo" }
```

- unregister → 实例 `DRAINING`
- 连接断开（非顶号）→ `OFFLINE`（本轮无 attempt，一律 OFFLINE）

### `system.error` / `system.notice`

```json
{ "code": "NOT_IMPLEMENTED", "message": "…", "retryable": false }
```

顶号 notice：`code=SESSION_REPLACED`。

---

## Task 执行（MVP）

中心下发 `task.execute`（关键字段）：

| 字段 | 说明 |
|------|------|
| `execution_id` / `attempt_id` / `attempt_number` | |
| `lease_token` / `lease_expires_at` | 回传必须带上 |
| `job_code` / `job_name` | 1.1 用 job_code |
| `params` / `state` / `state_version` | `state` 来自 `job_states.key=default`；无记录则为 `{}` / `0` |
| `timeout_seconds` / `deadline_at` / `trigger_type` / `handler_version` | |

Executor 顺序建议：`execute_ack(accepted)` → `started` →（可选 `checkpoint`）→ `completed`|`failed`；每帧后收 `task.result_ack`。  
`task.completed.next_state` 与 `task.checkpoint.state` 均 upsert 到 `job_states.default`（`updated_by=executor`）。  
错误 lease / 错 instance → `REJECTED`，库状态不变。  
同一终态重复上报 → 幂等 `OK`（便于 pending 重放）；不同终态 → `REJECTED` / `ALREADY_FINISHED`。  
`lease_expires_at` 过期仍接受合法 `lease_token` 的晚到终态（Center 宕机恢复场景）。

Admin 触发：`POST /v1/admin/services/:serviceId/jobs/:jobId/executions`（见 [api.md](./api.md)）。

### WS 断线续跑（同 attempt）

- 任务 goroutine 不随 WS 断开取消；`service.heartbeat` 会续该 instance 上非终态 attempt 的租约。
- 重连 register 后：Executor 先重放本地磁盘 pending 终态，再发 `task.sync_running`（`attempts[{execution_id,attempt_id,lease_token}]`）。
- 中心 `task.sync_running_ack.results[]`：`OK`（附新 `lease_expires_at`）/ `GONE`（已终态，本地应停）/ `REJECTED`。
- 终态（`completed`/`failed`/`cancelled`）先写入执行器 `{JC_DATA_DIR}/pending_results/`，收到 `result_ack OK` 后删除；保证 Center 恢复后必达。

### `task.log`（执行日志落库）

Go SDK：`job.Log()` 为**独立上报通道**（默认不打本机控制台）。本机 slog 照旧；需要双写时业务自行封装（`local.Info` + `job.Log().Info`）。

SDK 缓冲（按 attempt）：

- 容量 **100**；满则先 flush；仍满则按 DEBUG/INFO → WARN → ERROR 挤旧条目
- 每 **1s** / 写入 **ERROR** / handler 返回前 flush → `publishSoft(task.log)`
- 丢弃 WARN/ERROR 时合成一条 WARN（`fields.dropped_info/warn/error`），帧级带 `dropped` / `dropped_info` / `dropped_warn` / `dropped_error`

中心 `OnLog`：

- 校验 lease；`seq <= last_log_seq` 幂等跳过；写入 `execution_logs`
- 按 level 累加 attempt 物理列 `log_{info,warn,error}_collected`；累加 `log_*_dropped`
- 虚拟列 `log_warn_count` / `log_error_count` = collected + dropped
- `result_ack` 回填 `acked_through_seq`

### 取消

Admin：`POST .../executions/:executionId/cancel` → 中心下发 `task.cancel`（`attempt_id` / `lease_token` / `reason`）。

Executor：`task.cancel_ack`（`state=CANCELLING|NOT_FOUND`）→ 协作停止后 `task.cancelled`。  
attempt.`cancel_state`：`NONE → REQUESTED → SENT → ACKED → STOPPED`。

---

## 配置

```yaml
websocket:
  auth_timeout: 10s
  ping_interval: 20s
  ping_timeout: 60s
  write_timeout: 10s
  max_message_bytes: 1048576
  send_queue_size: 64
  heartbeat_interval: 10s
  lease_ttl: 30s
```

联调：`scripts/ws_smoke.py`；Go SDK：`../job-executor-go`。

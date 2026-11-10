# Executor WebSocket 协议 1.1

总览与 Admin/HTTP 联调见 [api.md](./api.md)。

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
| `system.error` / `system.notice` | C→E | 错误 / 顶号通知 |

任务执行帧（`task.*`）本轮返回 `system.error`：`NOT_IMPLEMENTED`。

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

与 HTTP `POST /v1/executor/register` body 相同。

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

联调脚本：`scripts/ws_smoke.py`。

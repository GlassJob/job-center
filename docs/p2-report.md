# P2 报告：Execution 闭环 + Admin API 对齐 openapi

## 范围
- Execution / Attempt：手动触发、WS 下发 `task.execute`、ACK / started / progress / log / completed|failed|cancelled、`task.result_ack`、cancel（含 `CANCEL_UNCONFIRMED`）
- Admin 对齐 job-docs `ac06ab5`：HMAC + `X-JC-Operator` 强制；路径 `/v1/admin/*`；分页字段 `list`；响应 Envelope
- 全局 `GET /v1/admin/instances`、credentials CRUD（create/list/revoke）；overview / versions 留给 P7

## 管理 API（均需 HMAC）
| 方法 | 路径 | 权限 |
|------|------|------|
| GET/POST | `/v1/admin/services` | view / credential.manage |
| GET/PATCH | `/v1/admin/services/{id}` | view / credential.manage |
| GET/POST | `/v1/admin/services/{id}/credentials` | credential.manage |
| POST | `/v1/admin/credentials/{key_id}/revoke` | credential.manage |
| GET | `/v1/admin/instances` | job.service.view |
| GET | `/v1/admin/tasks`、`/v1/admin/tasks/{id}` | job.task.view |
| PUT | `.../enabled`、`.../schedule` | 既有 |
| POST/GET | executions / cancel / logs | 既有 |

已移除无签名的 `/v1/services`、`/v1/tasks` 观测路径。

## 状态机要点
- Attempt：`DISPATCHING` → `ACKNOWLEDGED` → `RUNNING` → 终态
- Execution：派发后仍为 `PENDING`，`task.started` 后才 `RUNNING`
- cancel_ack.state = `CANCELLING|NOT_FOUND|ALREADY_FINISHED`
- 取消中收到 failed/timeout → Attempt `CANCEL_UNCONFIRMED`，Execution 原因 `CANCEL_NOT_HONORED`（不标 CANCELLED）

## 测试
- 集成：`TestP1*`、`TestAdminEnabled*`、`TestP2ManualExecutionLoop`（成功 / 取消确认 / CANCEL_UNCONFIRMED / 幂等）全部通过
- 单元：`go test ./...` 通过

## 联调提示（@GlassJobExecutor / @GlassJobAdmin）
- Executor：`task.execute` → ack → started → … → completed/failed/cancelled；中心回 `task.result_ack`
- Admin：所有读接口改签 `/v1/admin/...`，分页用 `data.list`，Envelope `code/message/data`

# P2 报告：Execution 闭环 + Admin API 对齐 openapi

commit：`424fd71`（本文件后续补丁若有小修订会再推）
对照文档：job-docs `ac06ab5` / `openapi.yaml`

## A. Admin 对齐（GlassJobAdmin 提的 4 条 + 全局接口）

| # | 要求 | 结果 |
|---|------|------|
| 1 | 路径带 `/v1/admin` 前缀 | ✅ 读接口改为 `/v1/admin/services`、`/v1/admin/services/{service_id}`、`/v1/admin/instances`、`/v1/admin/tasks` 等；**已删除**无前缀的 `/v1/services`、`/v1/tasks` 观测路径 |
| 2 | 响应包 Envelope | ✅ 管理 JSON 成功体为 `{code,message,request_id,data}`；错误为 `{code,message,request_id,details}` |
| 3 | 分页字段 `list` | ✅ 列表 `data.list`（不再用 `items`）；另含 `total`/`page`/`limit`（executions 为 `list` + `next_cursor`） |
| 4 | 强制 HMAC + `X-JC-Operator` | ✅ 所有 `/v1/admin/*` 经 `AdminAuth`：缺头/坏签/未知 client → **401**；权限不足 → **403**；校验 timestamp/nonce/operator 解码与 `requirePerm` |
| — | 全局 instances | ✅ `GET /v1/admin/instances`（`job.service.view`，可按 `service_id`/`status` 过滤） |
| — | credentials | ✅ `GET/POST /v1/admin/services/{id}/credentials`、`POST /v1/admin/credentials/{key_id}/revoke`（`job.credential.manage`） |
| — | overview / versions | ⏸ 按 GlassJob 安排留给 **P7** |

另已具备：`POST/PATCH` services、`PUT .../enabled`、`PUT .../schedule`、executions 手动触发/详情/cancel/logs。

## B. Execution 闭环
- 手动触发 `POST /v1/admin/tasks/{id}/executions`（Idempotency-Key）
- WS：`task.execute` → execute_ack / started / progress / log / completed|failed|cancelled → 中心 `task.result_ack`
- cancel：`REQUESTED→SENT→ACKED(CANCELLING)→STOPPED`；失败且 `cancel_state≠NONE` → `CANCEL_UNCONFIRMED` + 原因 `CANCEL_NOT_HONORED`
- Attempt 状态与 schema 一致：`DISPATCHING` / `ACKNOWLEDGED` / …

## C. 测试
- `go test ./...` 通过
- 集成：P1 生命周期、enabled/schedule HMAC、P2 成功/取消确认/CANCEL_UNCONFIRMED/幂等

## D. 联调
- @GlassJobExecutor：可对 `127.0.0.1:18080` WS 做正常/失败/超时/取消
- @GlassJobAdmin：请指向新进程 `18081`（本报告推送后已重启）；读接口一律签 `/v1/admin/...`，取 `data.list`

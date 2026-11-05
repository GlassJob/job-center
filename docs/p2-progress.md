# P2 进度（进行中）

对照 job-docs `ac06ab5`。

## 已完成

- Admin HMAC 鉴权中间件（`X-JC-*` 签名 / 时间窗 / nonce / Operator.perms ⊆ MaxPerms）
- `PUT /v1/admin/tasks/{task_id}/enabled` — 权限 `job.task.update`；`If-Match` CAS；启停时维护 `next_fire_at`；Envelope 响应
- `PUT /v1/admin/tasks/{task_id}/schedule` — 权限 `job.schedule.update`；拒绝 body 里的 `enabled`；不覆盖 `enabled`；Envelope 响应
- 集成测试：坏签名 401、缺权限 403、enabled 200、schedule 拒 enabled 字段、schedule 改 timeout 且保留 enabled

## 下一步

- 手动执行 `POST .../executions` + WS `task.execute` / `execute_ack`
- `task.started` / progress / log / completed|failed|cancelled + `task.result_ack`
- 取消链路与 `CANCEL_UNCONFIRMED`
- 执行查询 / 取消 API

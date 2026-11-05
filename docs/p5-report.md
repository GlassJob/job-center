# P5 报告：Cron 调度 / Misfire / 并发 / 租约恢复

对照文档：job-docs `ac06ab5`（`scheduling.md` / `execution-lifecycle.md` / `state-machine.md` / `openapi.yaml`）

## A. 行为逐项

| # | 要求 | 结果 |
|---|------|------|
| 1 | Cron + 时区（NULL=服务器本地时区） | ✅ `scheduler` 用 `task.ValidateCron`（6 字段）+ `schedule.ResolveLocation`；`cfg.Scheduler.DefaultTimezone` / `TZ` / `/etc/localtime` 与 P1 一致 |
| 2 | Misfire SKIP / FIRE_ONCE / CATCH_UP + threshold + catch_up_max≤100 | ✅ `domain/schedule.ComputeFires`；SKIP 写 1 条 `SKIPPED(MISFIRE_SKIP)` 汇总；FIRE_ONCE 补 1；CATCH_UP 上限截断 |
| 3 | 并发 ALLOW / SKIP / QUEUE（DB 原子） | ✅ 事务内锁 `job_schedule`；SKIP/QUEUE 用 `concurrency_slot` 唯一键；ALLOW 用活跃 COUNT；QUEUE 超 `queue_max` → `QUEUE_FULL`；终态清槽并唤醒队首 |
| 4 | 租约续期 | ✅ 合法 `lease_token` 的结果/进度/日志/started 路径 `RenewLease`（`GREATEST`） |
| 5 | 超时 → TIMED_OUT | ✅ deadline sweeper 发 `task.cancel(TIMEOUT)`，宽限期后 `TIMED_OUT(TIMEOUT_UNCONFIRMED)` |
| 6 | 重试退避 | ✅ `retry_policy` JSON → EXPONENTIAL/FIXED + jitter；失败/LOST 按策略 `ScheduleRetry` |
| 7 | 拒收旧租约结果 | ✅ `loadAndCheckLease` 校验 token + `current_attempt_id`；失败写 `STALE_RESULT_REJECTED`，回 `REJECTED/LEASE_INVALID` |
| 8 | 重启恢复、不爆补任务 | ✅ 不清状态；启动后首个 `lease_ttl` 内暂停 LOST；Scheduler 按 misfire 处理 overdue（SKIP 不补一堆）；`uk_schedule_tick` 兜底 |
| — | Leader 租约 | ✅ `job_node_lease`：`scheduler` / `lease-sweeper` |
| — | Dispatcher | ✅ 异步领取 `PENDING` + `next_attempt_at`，派发新 Attempt |

## B. openapi 对齐（同批）

| 项 | 结果 |
|----|------|
| `GET /v1/admin/executions/{id}` → 扁平 `ExecutionDetail` | ✅ `data` 顶层为 Execution 字段 + `attempts[]`（不再包 `{execution, attempts}`） |
| `POST .../cancel` → **202** | ✅ Center 本身返回 202（GlassAdmin 代理归一成 200 属代理行为，不改 Center） |
| SSE tickets | ⏸ P7 |

## C. 测试场景（最低四条）

| 场景 | 断言 | 测试 |
|------|------|------|
| 不重复调度 | 同一 `(schedule_id, scheduled_at)` 仅 1 条 | `TestP5SchedulerAndLease/no_duplicate_schedule_tick` |
| 重启不爆补 | overdue + SKIP → ≤2 条且含 SKIPPED 审计，`next_fire_at` 推进 | `.../restart_no_burst` |
| 旧租约结果被拒 | 错误 token → `REJECTED`；Execution 非 SUCCEEDED；详情扁平 | `.../stale_lease_rejected` |
| Executor 崩溃/租约过期可恢复 | Attempt→LOST → 重试 Attempt#2 → SUCCEEDED | `.../lease_expiry_recovers` |

另：`go test ./...` 通过（含 P1/P2；P2 详情断言已改扁平；cancel 仍断言 202）。

## D. 联调

- 进程：`127.0.0.1:18080`（WS）/ `:18081`（Admin），本报告推送后重启到含 P5 的二进制
- @GlassJobAdmin：详情读扁平字段；overview/versions 仍 P7
- @GlassJobExecutor：可联调 Cron 下发、租约续期、超时取消、LOST 重试

## E. 相对文档的已知差距（可接受 / 后续）

- ACK 超时重发耗尽 → LOST：V1 简化，主要靠租约过期路径
- 续租写合并（每 Attempt 每 5s 最多一次）：未做节流，每次合法消息都 UPDATE
- `no_instance_wait` 超时 FAILED：目前仅退避 bump `next_attempt_at`
- ALLOW 多槽不用 `task:id:n` 占位，用 COUNT（与 docs §4.3 ALLOW 一致）

# Job Center 运维手册（P8）

## 日常

- 看 `/health/ready`：MySQL 不通会 503。
- 看 `/metrics`：`job_center_archive_lag_days` 上升说明日归档落后。
- Admin 审计：`GET /v1/admin/audits`（权限 `job.credential.manage`，Envelope + `data.list` + keyset `cursor`）。
- 实例离线 / 任务逾期：`GET /v1/admin/alerts`；后台 `monitoring.Watcher` 周期评估。

## 日志与归档

- 执行日志在 `job_execution_log`；每日 `archiver`（`job_node_lease('archiver')`）按 UTC 日导出 JSONL.gz，校验通过后才删。
- 保留默认 365 天（`archive.log_retention_days` / `execution_retention_days`）。
- 归档目录：`archive.dir`（Compose 挂卷 `/var/lib/glassjob/archive`）。

## 故障与恢复

| 场景 | 行为 |
|------|------|
| Executor 断线 | SDK 指数退避重连；实例 → SUSPECT → OFFLINE；进行中 Attempt 靠租约过期 → LOST → 按 `retry_on_lost` 重试 |
| 进程崩溃（无 complete） | 同租约过期恢复 |
| 任务超时 | Runtime 发 `task.cancel(TIMEOUT)`，宽限后 `TIMED_OUT` |
| 失败重试 | `retry_policy` FIXED/EXPONENTIAL；`ScheduleRetry` → PENDING |
| 旧租约结果 | `REJECTED/LEASE_INVALID`，记 `STALE_RESULT_REJECTED` |
| Job Center 重启 | 不清状态；启动后短时暂停 LOST；调度按 misfire，不爆补 |
| DB 故障 | `/health/ready` 503；写路径返回 `STORAGE_UNAVAILABLE` |

## 调度注意

- Cron 6 字段；`timezone` 空/`null`/`""` → 服务器本地时区。
- Misfire：SKIP / FIRE_ONCE / CATCH_UP（有上限）。
- 并发：ALLOW / SKIP / QUEUE（DB 原子，`concurrency_slot`）。

## 发布与版本

- `POST /v1/deployments`（发布脚本 HMAC）
- `POST /v1/admin/services/{id}/deployments`（手工预期版本 + 审计）
- `GET /v1/admin/versions` 对账状态（Envelope）

## 备份建议

- 定期备份 MySQL `job_center`；归档目录与库一并保留。
- 密钥：`credential_pepper`、Admin/Deployer HMAC secret 勿入库明文仓库。

# P7 报告：SSE / overview / 版本对账 / 监控 / 归档

对照文档：job-docs `ac06ab5`（`openapi.yaml` / `protocol-http.md` / `database.md` / `architecture.md`）

## A. 行为逐项

| # | 要求 | 结果 |
|---|------|------|
| 1 | SSE 票据 `POST /v1/admin/sse-tickets`（60s、单执行、单次使用） | ✅ `security.TicketIssuer` + Admin HMAC |
| 2 | SSE 事件流 `GET .../events`，`Last-Event-ID`/`cursor` 续传 | ✅ DB 补发 event+log+progress；15s `: ping`；终态 `end`；票据或 HMAC；连接上限 30min |
| 3 | 日志增量 `after_seq`（+ `level` 最低级别） | ✅ `ListLogsFiltered`；响应同时带 `list`/`items`（兼容 P2 Admin 与 openapi） |
| 4 | overview 统计 | ✅ `GET /v1/admin/overview` Envelope；含 `version_mismatch_instance_count`、`today_timezone` |
| 5 | 实例离线 / 任务逾期监控 | ✅ `monitoring.Watcher` 周期评估；告警写 WARN；`GET /v1/admin/alerts` 快照（openapi 未列，便于联调） |
| 6 | 版本对账 | ✅ `GET /v1/admin/versions`；状态机 `domain/version.State` |
| 7 | 通用发布登记 `POST /v1/deployments`（HMAC 无 Operator） | ✅ 幂等 `(service,env,build_id)`；`scripts/register-deployment.sh` |
| 8 | 手工登记 `POST /v1/admin/services/{id}/deployments` | ✅ `source=MANUAL` + 审计；`job.deployment.manage` |
| 9 | 每日日志归档（保留 1 年；导出成功后才删） | ✅ `application/archive`：JSONL.gz → 校验 → EXPORTED → DELETE/DROP PARTITION → SUCCEEDED；`job_node_lease('archiver')` |
| 10 | Cron 预览 `POST /v1/admin/cron/preview` | ✅（Admin 联调缺口，并入本批） |
| 11 | `timezone:""` 视同 null | ✅ schedule PATCH / catalog UpdateSchedule / cron preview |

## B. 测试

- `go test ./internal/...` ✅（含 sse cursor、version reconcile、sseticket、archive sink）
- `go test ./tests/integration/ -count=1` ✅（含 `TestP7OverviewSSEDeployVersions`：overview / deployments / versions / SSE ticket+stream / cron preview / empty TZ / alerts；P1/P2/P5 未回归）

## C. 联调

- 进程：`127.0.0.1:18080`（WS）/ `:18081`（Admin），本报告推送后重启到含 P7 的二进制
- `@GlassJobAdmin`：可接 overview、versions、SSE 日志流、cron preview；调度编辑传 `timezone:""` 会落库 NULL
- `@GlassJobExecutor`：发布脚本可对 `:18081` 调 `POST /v1/deployments`（客户端 `deployer`）

## D. 相对文档的已知差距（可接受）

- openapi `GET /versions`、`GET /deployments` 写成裸数组；实现统一 Envelope（与 protocol-http.md §3 / 其它 Admin API 一致）
- openapi 日志响应写 `items` 且未标 Envelope；实现 Envelope + `list`/`items` 双字段
- SSE progress 合并「每 Attempt 每秒最多 1 条」未严格节流（DB 轮询 500ms，客户端可自行去抖）
- 日志分区 DROP 依赖 MySQL 分区表；开发库若未分区则 ensure/drop 会记 WARN/跳过，DELETE 路径仍可用
- `/metrics` 归档滞后指标未做（database.md 提及 P7，本次未纳入）
- `GET /v1/admin/audits` 仍未实现（openapi 有，非本批范围）

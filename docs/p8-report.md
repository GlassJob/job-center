# P8 报告：稳定性测试与生产交付

对照：job-docs `ac06ab5`（spec P8 / architecture §7 / openapi audits）

## A. Part A 前置修复（已先推）

| 项 | 结果 | Commit |
|----|------|--------|
| versions / deployments Envelope | ✅ 保持 `{code,message,request_id,data}`；集成测试断言 `code=OK` | `0cdfc67` |
| SSE progress 1/s 节流 | ✅ `domain/sse.ProgressThrottle` 每 Attempt ≤1 条/秒 | `0cdfc67` |

## B. P8 交付（`3f7bac4`）

| # | 要求 | 结果 |
|---|------|------|
| 1 | 断线重连 | ✅ `TestP8Stability/disconnect_reconnect` |
| 2 | 并发领取 | ✅ `TestP8Stability/concurrent_claim`（双 DispatchOnce → 仅 1 Attempt） |
| 3 | 超时 | ✅ `.../timeout` → `TIMED_OUT` |
| 4 | 失败重试 | ✅ `.../retry_on_fail` → 第 2 Attempt SUCCEEDED |
| 5 | 租约恢复 / 进程崩溃 | ✅ `.../process_crash_lease_recovery`（LOST → 重试成功） |
| 6 | DB 故障 | ✅ `.../db_failure_ready` → `/health/ready` 503 |
| 7 | 部署文档 | ✅ `docs/deploy.md` |
| 8 | 运维文档 | ✅ `docs/ops.md` |
| 9 | Docker Compose | ⚠️ 文件齐全（`deploy/*` + 根 `Dockerfile`）；**本机无 Docker，未实测** |
| 10 | `GET /v1/admin/audits` | ✅ Envelope + list/cursor；权限 `job.credential.manage` |
| 11 | `/metrics` 归档滞后 | ✅ Prometheus text：`job_center_archive_lag_days` / `table_rows` / `table_bytes` |

## C. 测试

- `go test ./internal/...` ✅
- `go test ./tests/integration/ -count=1` ✅（含 P1/P2/P5/P7/P8；跑集成时需停本机 `18080/18081` 以免抢 MySQL）

## D. 联调进程

- `127.0.0.1:18080` / `:18081` 已切到含 Part A+B 的二进制
- `@GlassJobAdmin`：可接 audits；overview/versions/SSE 仍用 P7
- `@GlassJobExecutor`：冒烟对着本机最新版即可

## E. 差距

- Docker Compose **未实测**（盒子无 `docker`）
- `/health/ready` 尚未检查「调度器近 10s 跑过」（protocol-http §7 完整定义；现为 MySQL Ping）
- SDK 接入长文仍以 job-docs `sdk-go.md` / `sdk-php.md` 为准（本仓不重复）
- 审计历史里曾写入 `Result=OK` 的路径已改为 `SUCCESS`（匹配表 CHECK）

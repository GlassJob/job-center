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

## F. 18.4 性能基准测试（`tests/integration/p8_bench_test.go`）

命令：`go test -run='^$' -bench='^BenchmarkP8_' -benchmem -benchtime=250ms ./tests/integration/`（真实 MySQL 8.0 + 真实 WebSocket，Intel Xeon 8 vCPU）

| 基准用例 | 覆盖 §18.4 指标 | 实测结果 | 内存分配 |
|----------|----------------|----------|----------|
| `BenchmarkP8_WSConcurrentConnect-8` | WebSocket 并发连接与鉴权 | `1.36 ms/op`（~735 conn/s） | `85.3 KB/op`, `819 allocs/op` |
| `BenchmarkP8_DispatchAndStatusLifecycle-8` | 任务下发延迟 + 状态更新吞吐（Trigger→Ack→Started→Completed 全链路落库） | `11.51 ms/op`（~87 full-lifecycle/s） | `87.3 KB/op`, `1614 allocs/op` |
| `BenchmarkP8_LogWriteThroughput-8` | 日志批量写入吞吐（20 条/批入月分区表） | `6.72 ms/op`（**`2977 lines/s`**） | `43.6 KB/op`, `682 allocs/op` |
| `BenchmarkP8_ConcurrentClaimConsistency-8` | MySQL 锁竞争 + 高并发执行状态一致性（8 协程并发 `DispatchOnce` 抢同一 PENDING） | `6.54 ms/op`；全部迭代断言仅生成 **1** 个 Attempt | `179.8 KB/op`, `3408 allocs/op` |

## G. 收尾对齐补充

- `GET /v1/admin/executions/{id}/logs` 已收敛为 Envelope + `data.list`（去掉 `items`，commit `651371f`）。
- 第 22 章文档已同步推入 `GlassJob/job-docs`（`b65e908`）：`deployment.md`、`operations.md`、`troubleshooting.md`，并通过 `_verify/verify_docs.sh` 校验。

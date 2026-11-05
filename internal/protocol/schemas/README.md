本目录是 [GlassJob/job-docs](https://github.com/GlassJob/job-docs) `schemas/*.json` 的同步副本（go:embed 需要模块内文件）。
不要手改；协议变更在 job-docs 修改生成器后执行 `scripts/sync-schemas.sh`。
`tests/contract` 会在能找到 job-docs（`JC_DOCS_DIR` 或同级 `../docs`）时逐字节比对，确保没有漂移。

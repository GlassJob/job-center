// Package migrations 内嵌 SQL 迁移文件（NNNN_name.sql，只含可重复执行的 CREATE IF NOT EXISTS 等语句）。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

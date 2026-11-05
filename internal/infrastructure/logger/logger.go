// Package logger 提供基于 log/slog 的结构化日志。
// 约定字段：node_id、service_code、instance_id、connection_id、session_epoch、trace_id、request_id。
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	if strings.ToLower(format) == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

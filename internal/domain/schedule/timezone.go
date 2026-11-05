// Package schedule 包含调度配置相关的领域规则。
package schedule

import (
	"fmt"

	"path/filepath"
	"strings"
	"time"
)

// ResolveDefaultTimezone 计算默认调度时区（已确认 2026-11-05：不硬编码，取服务器本地时区）。
// 顺序：配置值 → TZ 环境变量 → /etc/localtime 符号链接解析出的 IANA 名 → UTC。
// 返回生效的 IANA 名与来源说明（启动日志打印，便于发现容器未设置 TZ）。
func ResolveDefaultTimezone(configured string, getenv func(string) string, localtimePath string) (*time.Location, string, string, error) {
	if configured != "" {
		loc, err := time.LoadLocation(configured)
		if err != nil {
			return nil, "", "", fmt.Errorf("scheduler.default_timezone %q: %w", configured, err)
		}
		return loc, configured, "config", nil
	}
	if tz := strings.TrimPrefix(getenv("TZ"), ":"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil && !filepath.IsAbs(tz) {
			return loc, tz, "TZ", nil
		}
	}
	if localtimePath == "" {
		localtimePath = "/etc/localtime"
	}
	if target, err := filepath.EvalSymlinks(localtimePath); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if loc, err := time.LoadLocation(name); err == nil {
				return loc, name, localtimePath, nil
			}
		}
	}
	return time.UTC, "UTC", "fallback", nil
}

// ValidateTimezone 校验任务级时区覆盖（nil/空 = 使用默认时区）。
func ValidateTimezone(tz *string) error {
	if tz == nil || *tz == "" {
		return nil
	}
	if strings.ContainsAny(*tz, " \t") || filepath.IsAbs(*tz) {
		return fmt.Errorf("invalid IANA timezone %q", *tz)
	}
	if _, err := time.LoadLocation(*tz); err != nil {
		return fmt.Errorf("invalid IANA timezone %q", *tz)
	}
	return nil
}


package idgen

import "github.com/google/uuid"

// New 返回 UUID v7 字符串（36 位，带连字符，时间有序）。
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		// 极罕见；退化到 v4 保证进程不崩
		return uuid.NewString()
	}
	return id.String()
}

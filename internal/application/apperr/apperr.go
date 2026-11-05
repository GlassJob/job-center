// Package apperr 定义应用层错误（带稳定错误码），由传输层映射为 WS 错误或 HTTP 状态。
package apperr

import (
	"errors"
	"fmt"
)

type Kind int

const (
	KindInvalid Kind = iota + 1
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindUnavailable
	KindInternal
)

type Error struct {
	Kind      Kind
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
	Err       error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func New(k Kind, code, msg string) *Error { return &Error{Kind: k, Code: code, Message: msg} }

func Wrap(k Kind, code, msg string, err error) *Error {
	return &Error{Kind: k, Code: code, Message: msg, Err: err, Retryable: k == KindUnavailable}
}

// Storage 数据库错误统一为 STORAGE_UNAVAILABLE（可重试）。
func Storage(err error) *Error { return Wrap(KindUnavailable, "STORAGE_UNAVAILABLE", "database unavailable", err) }

// As 提取 *Error；非应用错误视为内部错误。
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(KindInternal, "INTERNAL_ERROR", "internal error", err)
}

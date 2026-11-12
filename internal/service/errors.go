package service

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidInput = errors.New("参数错误")
	ErrNotFound     = errors.New("资源不存在")
	ErrConflict     = errors.New("冲突")
	ErrForbidden    = errors.New("禁止操作")
)

func Invalidf(format string, args ...any) error {
	return fmt.Errorf("%w：%s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

func NotFoundf(format string, args ...any) error {
	return fmt.Errorf("%w：%s", ErrNotFound, fmt.Sprintf(format, args...))
}

func Conflictf(format string, args ...any) error {
	return fmt.Errorf("%w：%s", ErrConflict, fmt.Sprintf(format, args...))
}

func Forbiddenf(format string, args ...any) error {
	return fmt.Errorf("%w：%s", ErrForbidden, fmt.Sprintf(format, args...))
}

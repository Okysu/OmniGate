// Package apperr defines transport-independent application errors. Domain and
// service code returns these; HTTP adapters map Kind to a status code.
package apperr

import (
	"errors"
	"fmt"
)

type Kind int

const (
	KindInternal Kind = iota
	KindValidation
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindRateLimited
	// KindUpstream is a failure of a third-party service the server called.
	KindUpstream
)

type Error struct {
	Kind    Kind
	Code    string // stable machine-readable code, e.g. "version_conflict"
	Message string // safe to show to the caller; never contains secrets
	Details map[string]any
	Err     error // wrapped cause, logged but never returned to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, code, msg string) *Error { return &Error{Kind: kind, Code: code, Message: msg} }

func Validation(msg string, details map[string]any) *Error {
	return &Error{Kind: KindValidation, Code: "validation_failed", Message: msg, Details: details}
}
func Unauthenticated() *Error {
	return New(KindUnauthenticated, "unauthenticated", "需要登录")
}
func Forbidden() *Error { return New(KindForbidden, "forbidden", "没有执行此操作的权限") }
func NotFound(what string) *Error {
	return New(KindNotFound, "not_found", what+"不存在")
}
func VersionConflict() *Error {
	return New(KindConflict, "version_conflict", "资源已被修改，请刷新后重试")
}
func RateLimited() *Error {
	return New(KindRateLimited, "rate_limited", "请求过于频繁，请稍后再试")
}

func Internal(err error) *Error {
	return &Error{Kind: KindInternal, Code: "internal_error", Message: "服务器内部错误", Err: err}
}

// As extracts an *Error, wrapping unknown errors as internal.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

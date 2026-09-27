package module

import (
	"context"
	"errors"
	"fmt"
)

// Error is why a call failed: a code, for programs, and a message, for
// people. Modules may add codes of their own, as "<module>.<code>". On a
// link, it's a JSON-RPC error whose data holds the code. See spec/wire.md,
// "Errors".
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Codes of the errors any call can fail with.
const (
	CodeInvalidArgument  = "invalid_argument"
	CodeNotFound         = "not_found"
	CodePermissionDenied = "permission_denied"
	CodeUnavailable      = "unavailable"
	CodeDeadlineExceeded = "deadline_exceeded"
	CodeCanceled         = "canceled"
	CodeUnimplemented    = "unimplemented"
	CodeUnknown          = "unknown"
)

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// Errorf returns an error with the given code.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorOf returns err as an *Error. Errors without a code are unknown ones.
func ErrorOf(err error) *Error {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeDeadlineExceeded, Message: err.Error()}
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeCanceled, Message: err.Error()}
	}
	return &Error{Code: CodeUnknown, Message: err.Error()}
}

// Code returns the code of err, see ErrorOf.
func Code(err error) string {
	if err == nil {
		return ""
	}
	return ErrorOf(err).Code
}

// JSON-RPC's own error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcServerError    = -32000
)

// rpcError is an Error as JSON-RPC carries it: the code in its data, and the
// closest JSON-RPC code in its code, for generic JSON-RPC clients.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Code string `json:"code,omitempty"`
	} `json:"data"`
}

func toRPC(e *Error) *rpcError {
	r := &rpcError{Code: rpcServerError, Message: e.Error()}
	switch e.Code {
	case CodeInvalidArgument:
		r.Code = rpcInvalidParams
	case CodeUnimplemented:
		r.Code = rpcMethodNotFound
	}
	r.Data.Code = e.Code
	return r
}

func (r *rpcError) module() *Error {
	code := r.Data.Code
	if code == "" {
		switch r.Code {
		case rpcMethodNotFound:
			code = CodeUnimplemented
		case rpcInvalidParams, rpcInvalidRequest, rpcParseError:
			code = CodeInvalidArgument
		default:
			code = CodeUnknown
		}
	}
	return &Error{Code: code, Message: r.Message}
}

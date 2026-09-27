package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sourcegraph/jsonrpc2"
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

// toRPC returns e as a JSON-RPC error: the code in its data, and the closest
// JSON-RPC code in its code.
func toRPC(e *Error) *jsonrpc2.Error {
	code := int64(-32000) // a server error
	switch e.Code {
	case CodeInvalidArgument:
		code = jsonrpc2.CodeInvalidParams
	case CodeUnimplemented:
		code = jsonrpc2.CodeMethodNotFound
	}
	err := &jsonrpc2.Error{Code: code, Message: e.Error()}
	err.SetError(map[string]string{"code": e.Code})
	return err
}

// fromRPC returns a JSON-RPC error as an *Error.
func fromRPC(e *jsonrpc2.Error) *Error {
	var data struct {
		Code string `json:"code"`
	}
	if e.Data != nil {
		_ = json.Unmarshal(*e.Data, &data)
	}
	if data.Code == "" {
		switch e.Code {
		case jsonrpc2.CodeMethodNotFound:
			data.Code = CodeUnimplemented
		case jsonrpc2.CodeInvalidParams, jsonrpc2.CodeInvalidRequest, jsonrpc2.CodeParseError:
			data.Code = CodeInvalidArgument
		default:
			data.Code = CodeUnknown
		}
	}
	return &Error{Code: data.Code, Message: e.Message}
}

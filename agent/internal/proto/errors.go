package proto

import (
	"errors"
	"fmt"
)

// Error codes. See spec section 2.3.
const (
	CodeUnsupportedVersion = "unsupported_version"
	CodeUnknownType        = "unknown_type"
	CodeInvalidArgument    = "invalid_argument"
	CodeNotFound           = "not_found"
	CodeSessionExited      = "session_exited"
	CodeSessionRunning     = "session_running"
	CodeLimitExceeded      = "limit_exceeded"
	CodeStale              = "stale"
	CodeInternal           = "internal"
)

// Error is a protocol error. Its Message is shown to users, so it must never
// contain secrets.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds a protocol error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError converts err to a protocol error; anything that is not already one
// becomes CodeInternal.
func AsError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

package pat

import (
	"errors"
	"strings"
)

type ErrorKind string

const (
	ErrorInvalidInput      ErrorKind = "invalid_input"
	ErrorAuthentication    ErrorKind = "authentication_failed"
	ErrorRejected          ErrorKind = "validation_rejected"
	ErrorRateLimited       ErrorKind = "rate_limited"
	ErrorTransient         ErrorKind = "transient_failure"
	ErrorMalformedResponse ErrorKind = "malformed_response"
	ErrorAccountMismatch   ErrorKind = "account_mismatch"
	ErrorPersistence       ErrorKind = "persistence_failed"
)

// Error describes a redacted PAT lifecycle failure. It never includes a token
// or an upstream response body.
type Error struct {
	Kind       ErrorKind
	Message    string
	HTTPStatus int
	Retryable  bool
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return "PAT operation failed"
	}
	if message := strings.TrimSpace(e.Message); message != "" {
		return message
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsKind(err error, kind ErrorKind) bool {
	var patErr *Error
	return errors.As(err, &patErr) && patErr.Kind == kind
}

func IsAuthenticationFailure(err error) bool {
	return IsKind(err, ErrorAuthentication)
}

func newError(kind ErrorKind, message string, status int, retryable bool, cause error) error {
	return &Error{
		Kind:       kind,
		Message:    message,
		HTTPStatus: status,
		Retryable:  retryable,
		Cause:      cause,
	}
}

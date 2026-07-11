// Package domain holds entities and the error vocabulary they raise. It has zero
// dependencies outside the standard library — no HTTP, no SQL, no JSON tags that
// exist only to please a transport. Everything above it imports it; it imports
// nothing back.
package domain

import "fmt"

// Code is a stable, machine-readable identifier for a failure. Clients switch on
// it. Because it is part of the API contract, renaming one is a breaking change —
// which is precisely why it lives here and not inline in a handler.
type Code string

const (
	CodeInternal           Code = "INTERNAL"
	CodeNotFound           Code = "NOT_FOUND"
	CodeMethodNotAllowed   Code = "METHOD_NOT_ALLOWED"
	CodeValidation         Code = "VALIDATION_FAILED"
	CodeLinkNotFound       Code = "LINK_NOT_FOUND"
	CodeLinkExpired        Code = "LINK_EXPIRED"
	CodeAliasTaken         Code = "ALIAS_TAKEN"
	CodeInvalidURL         Code = "INVALID_URL"
	CodeInvalidAlias       Code = "INVALID_ALIAS"
	CodeReservedAlias      Code = "RESERVED_ALIAS"
	CodeEmailTaken         Code = "EMAIL_TAKEN"
	CodeInvalidCredentials Code = "INVALID_CREDENTIALS"
	CodeUnauthorized       Code = "UNAUTHORIZED"
	CodeInvalidCursor      Code = "INVALID_CURSOR"
	CodeCodeGeneration     Code = "CODE_GENERATION_FAILED"
	CodeRateLimited        Code = "RATE_LIMITED"
)

// Error is the single error type crossing layer boundaries. Services return it,
// httpx maps it to a status code and response body. Handlers never construct
// HTTP status codes themselves.
type Error struct {
	Code    Code
	Message string
	// Details carries field-level context, e.g. {"url": "must be http or https"}.
	// Safe to show a user; never put internal state here.
	Details map[string]string
	wrapped error
}

func (e *Error) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.wrapped)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.wrapped }

// Is lets errors.Is compare by Code, so callers can write
// errors.Is(err, domain.ErrLinkNotFound) without holding the exact instance.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

func NewError(code Code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

func WrapError(err error, code Code, msg string) *Error {
	return &Error{Code: code, Message: msg, wrapped: err}
}

func (e *Error) WithDetails(d map[string]string) *Error {
	e.Details = d
	return e
}

// Sentinels for errors.Is comparison.
var (
	ErrLinkNotFound       = NewError(CodeLinkNotFound, "link not found")
	ErrLinkExpired        = NewError(CodeLinkExpired, "this link has expired")
	ErrAliasTaken         = NewError(CodeAliasTaken, "that alias is already taken")
	ErrEmailTaken         = NewError(CodeEmailTaken, "an account with that email already exists")
	ErrInvalidCredentials = NewError(CodeInvalidCredentials, "invalid email or password")
	ErrUnauthorized       = NewError(CodeUnauthorized, "authentication required")
	ErrInvalidCursor      = NewError(CodeInvalidCursor, "malformed pagination cursor")
	ErrRateLimited        = NewError(CodeRateLimited, "too many requests, please slow down")
)

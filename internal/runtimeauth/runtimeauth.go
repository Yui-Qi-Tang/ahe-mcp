// Package runtimeauth defines launcher-owned identities and bounded errors for
// AHE process authorization. It does not authenticate a human reviewer.
package runtimeauth

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PrincipalIDMaxBytes bounds a non-secret launcher identity.
const PrincipalIDMaxBytes = 200

var (
	// ErrUnauthenticated reports an absent or invalid launcher identity.
	ErrUnauthenticated = errors.New("runtime principal is not authenticated")
	// ErrUnauthorized reports an operation outside the launcher-selected profile.
	ErrUnauthorized = errors.New("runtime operation is not authorized")
)

// Principal is fixed by a trusted launcher, never by tool arguments.
type Principal struct {
	ID string
}

// NewPrincipal accepts one bounded, already-normalized launcher identity.
func NewPrincipal(id string) (Principal, error) {
	if id == "" || len(id) > PrincipalIDMaxBytes || !utf8.ValidString(id) ||
		strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
		return Principal{}, NewUnauthenticatedError("a bounded normalized launcher principal is required")
	}
	return Principal{ID: id}, nil
}

// AuthorizationError exposes only a stable code and a safe message. Its cause
// and the rejected identity, profile, tool name, and payload stay out of JSON.
type AuthorizationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
}

// NewUnauthenticatedError returns a structured missing-identity error. The
// caller must supply a fixed message, not rejected input or configuration.
func NewUnauthenticatedError(message string) *AuthorizationError {
	return &AuthorizationError{Code: "unauthenticated", Message: message, cause: ErrUnauthenticated}
}

// NewUnauthorizedError returns a structured authorization error. The caller
// must supply a fixed message, not rejected input or configuration.
func NewUnauthorizedError(message string) *AuthorizationError {
	return &AuthorizationError{Code: "permission_denied", Message: message, cause: ErrUnauthorized}
}

func (e *AuthorizationError) Error() string {
	return e.Code + ": " + e.Message
}

// Unwrap preserves errors.Is without exposing the cause through transport.
func (e *AuthorizationError) Unwrap() error { return e.cause }

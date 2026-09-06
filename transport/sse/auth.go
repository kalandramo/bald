package sse

import (
	"errors"
	"net/http"
	"strings"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// TokenExtractor extracts an authentication token from an HTTP request.
type TokenExtractor func(r *http.Request) string

// AuthorizeFunc validates an incoming request and its token.
// Return ErrForbidden to produce a 403 response; any other error produces 401.
type AuthorizeFunc func(r *http.Request, token string) error

// DefaultTokenExtractor extracts tokens from the Authorization header,
// X-Token header, or "token" query parameter, in that order.
func DefaultTokenExtractor(r *http.Request) string {
	if r == nil {
		return ""
	}
	if token := extractBearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.Header.Get("X-Token")); token != "" {
		return token
	}
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	return ""
}

func isForbidden(err error) bool {
	return errors.Is(err, ErrForbidden)
}

func extractBearerToken(auth string) string {
	auth = strings.TrimSpace(auth)
	if auth == "" {
		return ""
	}
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return auth
}

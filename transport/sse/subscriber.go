package sse

import (
	"net/http"
	"net/url"
	"strings"
)

// Subscriber represents a single client connected to an SSE stream.
type Subscriber struct {
	quit       chan *Subscriber
	connection chan *Event
	removed    chan struct{}
	eventId    string
	URL        *url.URL
	Header     http.Header
}

// close deregisters the subscriber from its stream.
func (s *Subscriber) close() {
	s.quit <- s
	if s.removed != nil {
		<-s.removed
	}
}

// HeaderValue returns the value of a request header.
func (s *Subscriber) HeaderValue(key string) string {
	if s == nil || s.Header == nil {
		return ""
	}
	return s.Header.Get(key)
}

// Authorization returns the Authorization header value.
func (s *Subscriber) Authorization() string {
	return s.HeaderValue("Authorization")
}

// BearerToken extracts a Bearer token from the Authorization header.
func (s *Subscriber) BearerToken() string {
	auth := strings.TrimSpace(s.Authorization())
	if auth == "" {
		return ""
	}
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return auth
}

// Token extracts a token from a custom header, Bearer auth, or URL query parameter.
func (s *Subscriber) Token(headerKey string) string {
	if headerKey != "" {
		if token := strings.TrimSpace(s.HeaderValue(headerKey)); token != "" {
			return token
		}
	}
	if token := s.BearerToken(); token != "" {
		return token
	}
	if s != nil && s.URL != nil {
		return strings.TrimSpace(s.URL.Query().Get("token"))
	}
	return ""
}

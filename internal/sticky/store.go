package sticky

import (
	"fmt"
	"sync"
	"time"
)

const (
	defaultCookieName = "lb_session"
	defaultTTL        = 30 * time.Second
)

// SessionEntry stores the backend selected for a sticky session.
type SessionEntry struct {
	BackendID string
	ExpiresAt time.Time
}

// Store keeps sticky session state for an in-memory load balancer node.
type Store struct {
	mu         sync.RWMutex
	sessions   map[string]SessionEntry
	ttl        time.Duration
	cookieName string
}

// NewStore creates a sticky session store with a default TTL.
func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = defaultTTL
	}

	return &Store{
		sessions:   make(map[string]SessionEntry),
		ttl:        ttl,
		cookieName: defaultCookieName,
	}
}

// Resolve returns the backend ID for the provided session key if it is still valid.
// If it is missing or expired, it returns an empty string and the caller can assign a new backend.
func (s *Store) Resolve(sessionKey string) (string, bool) {
	if sessionKey == "" {
		return "", false
	}

	s.mu.RLock()
	entry, ok := s.sessions[sessionKey]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		s.Delete(sessionKey)
		return "", false
	}
	return entry.BackendID, true
}

// Set stores a sticky assignment for the provided session key.
func (s *Store) Set(sessionKey, backendID string) {
	if sessionKey == "" || backendID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionKey] = SessionEntry{
		BackendID: backendID,
		ExpiresAt: time.Now().Add(s.ttl),
	}
}

// Delete removes a sticky session entry.
func (s *Store) Delete(sessionKey string) {
	if sessionKey == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey)
}

// CookieName returns the sticky session cookie name used by the router.
func (s *Store) CookieName() string {
	return s.cookieName
}

// SetCookieName overrides the default cookie name.
func (s *Store) SetCookieName(name string) {
	if name == "" {
		return
	}
	s.cookieName = name
}

// SessionKeyFromCookie extracts the sticky session key from a cookie value.
func SessionKeyFromCookie(cookieValue string) (string, error) {
	if cookieValue == "" {
		return "", fmt.Errorf("cookie value is required")
	}
	return cookieValue, nil
}

// SessionKeyFromRequest extracts the value of the sticky cookie from a request.
func (s *Store) SessionKeyFromRequest(cookieValue string) (string, error) {
	return SessionKeyFromCookie(cookieValue)
}

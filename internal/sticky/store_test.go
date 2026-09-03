package sticky

import (
	"testing"
	"time"
)

func TestResolveReturnsBackendForValidSession(t *testing.T) {
	store := NewStore(30 * time.Second)
	store.Set("session-1", "backend-1")

	backendID, ok := store.Resolve("session-1")
	if !ok {
		t.Fatal("expected session to resolve")
	}
	if backendID != "backend-1" {
		t.Fatalf("expected backend-1, got %q", backendID)
	}
}

func TestResolveReturnsFalseForExpiredSession(t *testing.T) {
	store := NewStore(1 * time.Millisecond)
	store.Set("session-1", "backend-1")

	time.Sleep(10 * time.Millisecond)

	if _, ok := store.Resolve("session-1"); ok {
		t.Fatal("expected expired session to be rejected")
	}
}

func TestResolveReturnsFalseForMissingSession(t *testing.T) {
	store := NewStore(30 * time.Second)

	if _, ok := store.Resolve("missing"); ok {
		t.Fatal("expected missing session to be rejected")
	}
}

func TestSetAndDeleteSession(t *testing.T) {
	store := NewStore(30 * time.Second)
	store.Set("session-1", "backend-9")

	if _, ok := store.Resolve("session-1"); !ok {
		t.Fatal("expected session to exist")
	}

	store.Delete("session-1")
	if _, ok := store.Resolve("session-1"); ok {
		t.Fatal("expected session to be removed")
	}
}

func TestSessionKeyFromCookie(t *testing.T) {
	key, err := SessionKeyFromCookie("abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "abc123" {
		t.Fatalf("expected abc123, got %q", key)
	}
}

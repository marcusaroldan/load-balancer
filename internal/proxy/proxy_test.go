package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyForwardsRequestToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	handler := NewHandler([]Backend{
		{ID: "backend-1", Address: backend.URL},
	}, FirstBackend)

	req := httptest.NewRequest(http.MethodGet, "http://localhost/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}

	if string(body) != "hello from backend" {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

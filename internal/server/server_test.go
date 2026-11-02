package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tnpknhghzc/locallm/internal/embed"
	"github.com/tnpknhghzc/locallm/internal/llm"
	"github.com/tnpknhghzc/locallm/pkg/locallm"
)

func TestGuard(t *testing.T) {
	e, err := locallm.Open(locallm.Options{DataDir: t.TempDir(), LLM: &llm.Fake{}, Embedder: embed.NewHash(64)})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e, "secret").Handler()
	do := func(host, auth, origin string) int {
		r := httptest.NewRequest("GET", "http://"+host+"/v1/books", nil)
		r.Host = host
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do("127.0.0.1:8787", "", ""); c != 401 {
		t.Fatalf("без токена: %d", c)
	}
	if c := do("127.0.0.1:8787", "Bearer secret", ""); c != http.StatusOK {
		t.Fatalf("с токеном: %d", c)
	}
	if c := do("evil.example.com", "Bearer secret", ""); c != 403 {
		t.Fatalf("чужой Host (DNS rebinding): %d", c)
	}
	if c := do("127.0.0.1:8787", "Bearer secret", "https://evil.example.com"); c != 403 {
		t.Fatalf("чужой Origin: %d", c)
	}
	_ = strings.TrimSpace
}

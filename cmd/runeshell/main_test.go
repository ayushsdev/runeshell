package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWsURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		path string
		want string
	}{
		{name: "host-port", addr: "127.0.0.1:8081", path: "/ws/agent", want: "ws://127.0.0.1:8081/ws/agent"},
		{name: "http", addr: "http://localhost:8081", path: "/ws/agent", want: "ws://localhost:8081/ws/agent"},
		{name: "https", addr: "https://example.com", path: "/ws/agent", want: "https://example.com/ws/agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wsURL(tc.addr, tc.path)
			if got != tc.want {
				t.Fatalf("wsURL(%q,%q) = %q, want %q", tc.addr, tc.path, got, tc.want)
			}
		})
	}
}

func TestBuildShareURL(t *testing.T) {
	got := buildShareURL("", "127.0.0.1:8081", "agent1", "ai")
	want := "http://127.0.0.1:8081/?mode=hub&agent=agent1&session=ai"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = buildShareURL("https://tailnet.example.com/", "127.0.0.1:8081", "agent1", "ai")
	want = "https://tailnet.example.com/?mode=hub&agent=agent1&session=ai"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPortFromAddr(t *testing.T) {
	if got := portFromAddr("127.0.0.1:8081"); got != "8081" {
		t.Fatalf("expected 8081, got %q", got)
	}
	if got := portFromAddr(":9000"); got != "9000" {
		t.Fatalf("expected 9000, got %q", got)
	}
}

func TestNoCacheFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('x')"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := noCacheFiles(dir)
	cases := []struct {
		path    string
		noStore bool
	}{
		{path: "/", noStore: true},
		{path: "/app.js", noStore: true},
		{path: "/asset.txt", noStore: false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			_, _ = io.ReadAll(rec.Result().Body)
			got := rec.Header().Get("Cache-Control")
			if tc.noStore && !strings.Contains(got, "no-store") {
				t.Fatalf("expected no-store for %s, got %q", tc.path, got)
			}
			if !tc.noStore && got != "" {
				t.Fatalf("expected empty cache-control for %s, got %q", tc.path, got)
			}
		})
	}
}

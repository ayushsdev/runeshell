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

func TestNoCacheFilesHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "asset.dat"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	handler := noCacheFiles(dir)
	tests := []struct {
		path        string
		wantNoStore bool
	}{
		{path: "/", wantNoStore: true},
		{path: "/app.css", wantNoStore: true},
		{path: "/asset.dat", wantNoStore: false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			_, _ = io.ReadAll(rec.Result().Body)
			got := rec.Header().Get("Cache-Control")
			if tc.wantNoStore && !strings.Contains(got, "no-store") {
				t.Fatalf("expected no-store, got %q", got)
			}
			if !tc.wantNoStore && got != "" {
				t.Fatalf("expected empty cache-control, got %q", got)
			}
		})
	}
}

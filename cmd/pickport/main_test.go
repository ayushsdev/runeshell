package main

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestRunPickportPrintsPort(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-preferred", "0"}, &out); err != nil {
		if isBindDenied(err) {
			t.Skipf("bind not permitted in this environment: %v", err)
		}
		t.Fatalf("run: %v", err)
	}
	s := strings.TrimSpace(out.String())
	port, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("expected integer output, got %q", s)
	}
	if port <= 0 {
		t.Fatalf("expected positive port, got %d", port)
	}
}

func isBindDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}

package devutil

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
)

func TestPickFreePortPrefersAvailable(t *testing.T) {
	port, err := PickFreePort(0)
	if err != nil {
		if isBindError(err) {
			t.Skip("bind not permitted in this environment")
		}
		t.Fatalf("PickFreePort: %v", err)
	}
	if port <= 0 {
		t.Fatalf("expected positive port, got %d", port)
	}
}

func TestPickFreePortAvoidsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if isBindError(err) {
			t.Skip("bind not permitted in this environment")
		}
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	busy := addr.Port

	port, err := PickFreePort(busy)
	if err != nil {
		t.Fatalf("PickFreePort: %v", err)
	}
	if port == busy {
		t.Fatalf("expected different port, got %d", port)
	}
}

func isBindError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}

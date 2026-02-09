package qr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mdp/qrterminal/v3"
)

func TestRenderANSIProducesExpectedLines(t *testing.T) {
	data := "https://example.com"
	var buf bytes.Buffer
	if err := RenderANSI(&buf, data); err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
}

func TestRenderANSIContainsANSISequences(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderANSI(&buf, "test"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, qrterminal.BLACK) || !strings.Contains(out, qrterminal.WHITE) {
		t.Fatalf("expected qrterminal block characters")
	}
}

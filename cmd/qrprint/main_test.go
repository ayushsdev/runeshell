package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunQRPrintUsage(t *testing.T) {
	var out bytes.Buffer
	err := run(nil, &out)
	if !errors.Is(err, errUsage) {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestRunQRPrintRendersANSI(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"https://example.com"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if len(got) == 0 {
		t.Fatalf("expected QR output")
	}
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected multiline output")
	}
}

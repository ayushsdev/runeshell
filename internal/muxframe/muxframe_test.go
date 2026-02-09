package muxframe

import "testing"

func TestEncodeDecode(t *testing.T) {
	payload := []byte("hello")
	enc, err := Encode("ai", payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sid, out, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sid != "ai" {
		t.Fatalf("expected ai, got %q", sid)
	}
	if string(out) != "hello" {
		t.Fatalf("expected payload, got %q", string(out))
	}
}

func TestDecodeRejectsShort(t *testing.T) {
	if _, _, err := Decode([]byte{0x00}); err == nil {
		t.Fatalf("expected error")
	}
}

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

func TestEncodeRejectsEmptySessionID(t *testing.T) {
	if _, err := Encode("", []byte("x")); err == nil {
		t.Fatalf("expected error")
	}
}

func TestEncodeRejectsTooLongSessionID(t *testing.T) {
	long := make([]byte, 65536)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Encode(string(long), []byte("x")); err == nil {
		t.Fatalf("expected error")
	}
}

func TestDecodeRejectsMissingSessionID(t *testing.T) {
	if _, _, err := Decode([]byte{0x00, 0x02, 'a'}); err == nil {
		t.Fatalf("expected error")
	}
}

func TestDecodeRejectsEmptySessionID(t *testing.T) {
	if _, _, err := Decode([]byte{0x00, 0x00}); err == nil {
		t.Fatalf("expected error")
	}
}

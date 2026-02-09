package muxframe

import (
	"encoding/binary"
	"errors"
)

func Encode(sessionID string, payload []byte) ([]byte, error) {
	sid := []byte(sessionID)
	if len(sid) == 0 {
		return nil, errors.New("session id required")
	}
	if len(sid) > 0xFFFF {
		return nil, errors.New("session id too long")
	}
	buf := make([]byte, 2+len(sid)+len(payload))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(sid)))
	copy(buf[2:], sid)
	copy(buf[2+len(sid):], payload)
	return buf, nil
}

func Decode(data []byte) (string, []byte, error) {
	if len(data) < 2 {
		return "", nil, errors.New("frame too short")
	}
	sidLen := int(binary.BigEndian.Uint16(data[:2]))
	if len(data) < 2+sidLen {
		return "", nil, errors.New("frame missing session id")
	}
	sessionID := string(data[2 : 2+sidLen])
	payload := data[2+sidLen:]
	if sessionID == "" {
		return "", nil, errors.New("session id required")
	}
	return sessionID, payload, nil
}

package termserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeSession struct {
	writeCh  chan []byte
	resizeCh chan [2]int
	outputCh chan []byte
	closeCh  chan struct{}
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		writeCh:  make(chan []byte, 8),
		resizeCh: make(chan [2]int, 8),
		outputCh: make(chan []byte, 8),
		closeCh:  make(chan struct{}, 1),
	}
}

func (s *fakeSession) Write(p []byte) error {
	cp := make([]byte, len(p))
	copy(cp, p)
	s.writeCh <- cp
	return nil
}

func (s *fakeSession) Resize(cols, rows int) error {
	s.resizeCh <- [2]int{cols, rows}
	return nil
}

func (s *fakeSession) Output() <-chan []byte {
	return s.outputCh
}

func (s *fakeSession) Close() error {
	s.closeCh <- struct{}{}
	return nil
}

type fakeManager struct {
	session  *fakeSession
	attachCh chan string
}

func (m *fakeManager) Attach(sessionID string) (Session, error) {
	m.attachCh <- sessionID
	return m.session, nil
}

type frame struct {
	msgType int
	data    []byte
}

type fakeConn struct {
	in     chan frame
	out    chan frame
	closed chan struct{}
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		in:     make(chan frame, 16),
		out:    make(chan frame, 16),
		closed: make(chan struct{}),
	}
}

func (c *fakeConn) ReadJSON(v any) error {
	mt, data, err := c.ReadMessage()
	if err != nil {
		return err
	}
	if mt != websocket.TextMessage {
		return errors.New("expected text message")
	}
	return json.Unmarshal(data, v)
}

func (c *fakeConn) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteMessage(websocket.TextMessage, b)
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	select {
	case f := <-c.in:
		return f.msgType, f.data, nil
	case <-c.closed:
		return 0, nil, errors.New("closed")
	}
}

func (c *fakeConn) WriteMessage(messageType int, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	c.out <- frame{msgType: messageType, data: cp}
	return nil
}

func (c *fakeConn) Close() error {
	select {
	case <-c.closed:
		return nil
	default:
		close(c.closed)
		return nil
	}
}

func (c *fakeConn) sendJSON(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c.in <- frame{msgType: websocket.TextMessage, data: b}
}

func (c *fakeConn) sendBinary(data []byte) {
	c.in <- frame{msgType: websocket.BinaryMessage, data: data}
}

func (c *fakeConn) readFrame(t *testing.T) frame {
	t.Helper()
	select {
	case f := <-c.out:
		return f
	case <-time.After(2 * time.Second):
		t.Fatalf("read timeout")
		return frame{}
	}
}

func TestServerAttachResizeAndStdout(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	server := NewServer(mgr, nil)
	conn := newFakeConn()

	done := make(chan struct{})
	go func() {
		server.ServeConn(conn)
		close(done)
	}()

	conn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1})

	resp := conn.readFrame(t)
	if resp.msgType != websocket.TextMessage {
		t.Fatalf("expected text message")
	}
	var attached ControlMessage
	if err := json.Unmarshal(resp.data, &attached); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if attached.Type != "attached" {
		t.Fatalf("expected attached, got %q", attached.Type)
	}

	select {
	case sid := <-mgr.attachCh:
		if sid != "ai" {
			t.Fatalf("expected session ai, got %q", sid)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("attach not called")
	}

	conn.sendJSON(t, ControlMessage{Type: "resize", Cols: 120, Rows: 32})
	select {
	case dims := <-sess.resizeCh:
		if dims != [2]int{120, 32} {
			t.Fatalf("unexpected dims: %+v", dims)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("resize not called")
	}

	sess.outputCh <- []byte("hello")
	out := conn.readFrame(t)
	if out.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary message")
	}
	if string(out.data) != "hello" {
		t.Fatalf("expected stdout hello, got %q", string(out.data))
	}

	conn.Close()
	close(sess.outputCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not exit")
	}
}

func TestServerStdinForwarding(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	server := NewServer(mgr, nil)
	conn := newFakeConn()

	done := make(chan struct{})
	go func() {
		server.ServeConn(conn)
		close(done)
	}()

	conn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1})
	_ = conn.readFrame(t)

	conn.sendBinary([]byte("ls\n"))
	select {
	case data := <-sess.writeCh:
		if string(data) != "ls\n" {
			t.Fatalf("expected ls\\n, got %q", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("stdin not forwarded")
	}

	conn.Close()
	close(sess.outputCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not exit")
	}
}

func TestTokenAuthorizer(t *testing.T) {
	auth := TokenAuthorizer("secret")
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws?token=secret", nil)
	if err := auth(req); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/ws?token=wrong", nil)
	if err := auth(req); err == nil {
		t.Fatalf("expected error for wrong token")
	}
}

func TestServeWSAuthRejects(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	server := NewServer(mgr, nil)
	server.Authorizer = TokenAuthorizer("secret")

	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	rec := httptest.NewRecorder()
	server.ServeWS(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

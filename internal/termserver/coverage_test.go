package termserver

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type errManager struct {
	err error
}

func (m *errManager) Attach(string) (Session, error) {
	return nil, m.err
}

type badConn struct {
	readJSONErr  error
	readMsgErr   error
	writeJSONErr error
	writeMsgErr  error
}

func (c *badConn) ReadJSON(v any) error                            { return c.readJSONErr }
func (c *badConn) WriteJSON(v any) error                           { return c.writeJSONErr }
func (c *badConn) ReadMessage() (int, []byte, error)               { return 0, nil, c.readMsgErr }
func (c *badConn) WriteMessage(messageType int, data []byte) error { return c.writeMsgErr }
func (c *badConn) Close() error                                    { return nil }

func TestLocalSessionManagerAttachStartError(t *testing.T) {
	origExec := execCommand
	origPtyStart := ptyStart
	t.Cleanup(func() {
		execCommand = origExec
		ptyStart = origPtyStart
	})

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "exit 0")
	}
	ptyStart = func(cmd *exec.Cmd) (*os.File, error) {
		return nil, errors.New("pty failed")
	}

	mgr := &LocalSessionManager{}
	if _, err := mgr.Attach("ai"); err == nil {
		t.Fatal("expected attach error")
	}
}

func TestPTYSessionMethodsAndReadLoopEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	done := make(chan struct{})
	cmd := exec.Command("sh", "-c", "sleep 0.01")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s := &PTYSession{
		cmd:       cmd,
		pty:       w,
		output:    make(chan []byte, 8),
		sessionID: "ai",
		tmux:      "tmux",
	}

	if got := s.Output(); got == nil {
		t.Fatal("expected non-nil output channel")
	}
	if err := s.Write([]byte("abc")); err != nil {
		t.Fatalf("write: %v", err)
	}

	go func() {
		defer close(done)
		buf := make([]byte, 16)
		_, _ = r.Read(buf)
	}()

	// Resize is expected to fail on a regular pipe; the call itself covers the method.
	_ = s.Resize(80, 24)

	waitDone := make(chan struct{})
	go func() {
		s.readLoop()
		close(waitDone)
	}()
	_ = w.Close()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipe reader did not exit")
	}
}

func TestPTYSessionReadLoopDataAndEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()

	s := &PTYSession{
		pty:    r,
		output: make(chan []byte, 8),
	}

	done := make(chan struct{})
	go func() {
		s.readLoop()
		close(done)
	}()

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case chunk := <-s.output:
		if string(chunk) != "hello" {
			t.Fatalf("unexpected chunk: %q", string(chunk))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected output chunk")
	}
	_ = w.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not exit")
	}
}

func TestServerNewServerHelpers(t *testing.T) {
	s := NewServer(&fakeManager{session: newFakeSession(), attachCh: make(chan string, 1)}, nil)
	if s.logger == nil {
		t.Fatal("expected default logger")
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	if !s.upgrader.CheckOrigin(req) {
		t.Fatal("expected check origin true")
	}

	msgConn := newFakeConn()
	sendError(msgConn, "bad_request", "boom")
	f := msgConn.readFrame(t)
	if f.msgType != websocket.TextMessage {
		t.Fatalf("expected text message, got %d", f.msgType)
	}
	var msg ControlMessage
	if err := json.Unmarshal(f.data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" || msg.Code != "bad_request" || msg.Message != "boom" {
		t.Fatalf("unexpected error message: %+v", msg)
	}

	if err := decodeJSON(nil, &msg); err == nil {
		t.Fatal("expected decode error for empty payload")
	}
}

func TestTokenAuthorizerEmptyToken(t *testing.T) {
	auth := TokenAuthorizer("")
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	if err := auth(req); err == nil || !strings.Contains(err.Error(), "token required") {
		t.Fatalf("expected token required error, got %v", err)
	}
}

func TestServeWSUpgradeAndExit(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, log.New(os.Stdout, "", 0))

	ts := httptest.NewServer(http.HandlerFunc(s.ServeWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1}); err != nil {
		t.Fatalf("write attach: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read attached: %v", err)
	}
	_ = conn.Close()
	close(sess.outputCh)
}

func TestServeWSUpgradeErrorBranch(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, nil)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	rec := httptest.NewRecorder()
	s.ServeWS(rec, req)
	// plain HTTP request should not panic; upgrade failure exits.
}

func TestServeConnErrorBranches(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, nil)

	// ReadJSON failure branch.
	s.ServeConn(&badConn{readJSONErr: errors.New("boom")})

	// First message not attach.
	conn1 := newFakeConn()
	done1 := make(chan struct{})
	go func() {
		s.ServeConn(conn1)
		close(done1)
	}()
	conn1.sendJSON(t, ControlMessage{Type: "resize"})
	msg := conn1.readFrame(t)
	var control ControlMessage
	if err := json.Unmarshal(msg.data, &control); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if control.Type != "error" || control.Code != "bad_request" {
		t.Fatalf("unexpected control: %+v", control)
	}
	conn1.Close()
	<-done1

	// Unsupported protocol version.
	conn2 := newFakeConn()
	done2 := make(chan struct{})
	go func() {
		s.ServeConn(conn2)
		close(done2)
	}()
	conn2.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 2})
	msg = conn2.readFrame(t)
	if err := json.Unmarshal(msg.data, &control); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if control.Type != "error" || control.Code != "bad_version" {
		t.Fatalf("unexpected control: %+v", control)
	}
	conn2.Close()
	<-done2

	// Attach manager error.
	sErr := NewServer(&errManager{err: errors.New("attach failed")}, nil)
	conn3 := newFakeConn()
	done3 := make(chan struct{})
	go func() {
		sErr.ServeConn(conn3)
		close(done3)
	}()
	conn3.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1})
	msg = conn3.readFrame(t)
	if err := json.Unmarshal(msg.data, &control); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if control.Type != "error" || control.Code != "attach_failed" {
		t.Fatalf("unexpected control: %+v", control)
	}
	conn3.Close()
	<-done3
}

func TestServeConnWriteAndDecodeBranches(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, nil)

	conn := newFakeConn()
	done := make(chan struct{})
	go func() {
		s.ServeConn(conn)
		close(done)
	}()

	conn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1})
	_ = conn.readFrame(t)

	// Invalid JSON control triggers decode error path.
	conn.in <- frame{msgType: websocket.TextMessage, data: []byte("{")}
	msg := conn.readFrame(t)
	var control ControlMessage
	if err := json.Unmarshal(msg.data, &control); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if control.Type != "error" || control.Code != "bad_request" {
		t.Fatalf("unexpected control: %+v", control)
	}
	conn.Close()
	close(sess.outputCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve conn did not exit")
	}
}

func TestServeConnAttachedWriteFailure(t *testing.T) {
	type conn struct{}
	var c conn
	attach := ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1}
	_ = c

	bc := &struct {
		badConn
	}{}
	bc.readJSONErr = nil
	bc.writeJSONErr = errors.New("write json failed")
	bc.readMsgErr = errors.New("read failed")

	s := NewServer(&fakeManager{session: newFakeSession(), attachCh: make(chan string, 1)}, nil)
	custom := &customReadJSONConn{
		writeJSONErr: bc.writeJSONErr,
		attach:       attach,
	}
	s.ServeConn(custom)
}

type customReadJSONConn struct {
	attach       ControlMessage
	writeJSONErr error
}

func (c *customReadJSONConn) ReadJSON(v any) error {
	out := v.(*ControlMessage)
	*out = c.attach
	return nil
}
func (c *customReadJSONConn) WriteJSON(v any) error { return c.writeJSONErr }
func (c *customReadJSONConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("done")
}
func (c *customReadJSONConn) WriteMessage(messageType int, data []byte) error { return nil }
func (c *customReadJSONConn) Close() error                                    { return nil }

type outputErrConn struct {
	attachRead bool
	closeCh    chan struct{}
}

func (c *outputErrConn) ReadJSON(v any) error {
	*v.(*ControlMessage) = ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1}
	c.attachRead = true
	return nil
}
func (c *outputErrConn) WriteJSON(v any) error { return nil }
func (c *outputErrConn) ReadMessage() (int, []byte, error) {
	<-c.closeCh
	return 0, nil, errors.New("closed")
}
func (c *outputErrConn) WriteMessage(messageType int, data []byte) error {
	return errors.New("write failed")
}
func (c *outputErrConn) Close() error {
	select {
	case <-c.closeCh:
	default:
		close(c.closeCh)
	}
	return nil
}

func TestServeConnOutputWriteErrorPath(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, nil)
	conn := &outputErrConn{closeCh: make(chan struct{})}

	done := make(chan struct{})
	go func() {
		s.ServeConn(conn)
		close(done)
	}()

	sess.outputCh <- []byte("boom")
	close(sess.outputCh)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected serve conn exit")
	}
}

func TestServeConnControlAndMessageDefaultBranches(t *testing.T) {
	sess := newFakeSession()
	mgr := &fakeManager{session: sess, attachCh: make(chan string, 1)}
	s := NewServer(mgr, nil)
	conn := newFakeConn()

	done := make(chan struct{})
	go func() {
		s.ServeConn(conn)
		close(done)
	}()

	conn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ai", ProtocolVersion: 1})
	_ = conn.readFrame(t)

	conn.sendJSON(t, ControlMessage{Type: "heartbeat"})
	conn.sendJSON(t, ControlMessage{Type: "unknown"})
	conn.sendBinary([]byte{})
	conn.in <- frame{msgType: websocket.PingMessage, data: []byte("x")}
	conn.in <- frame{msgType: websocket.TextMessage, data: []byte("{")}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve conn did not exit")
	}
	close(sess.outputCh)
}

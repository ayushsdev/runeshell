package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"

	"runeshell/internal/muxframe"
)

type noReplyConn struct{}

func (c *noReplyConn) ReadJSON(v any) error                            { return errors.New("not implemented") }
func (c *noReplyConn) WriteJSON(v any) error                           { return nil }
func (c *noReplyConn) ReadMessage() (int, []byte, error)               { return 0, nil, errors.New("closed") }
func (c *noReplyConn) WriteMessage(messageType int, data []byte) error { return nil }
func (c *noReplyConn) Close() error                                    { return nil }

type firstReadFailWriteConn struct {
	readPayload []byte
	consumed    bool
}

func (c *firstReadFailWriteConn) ReadJSON(v any) error {
	if c.consumed {
		return errors.New("closed")
	}
	c.consumed = true
	return json.Unmarshal(c.readPayload, v)
}
func (c *firstReadFailWriteConn) WriteJSON(v any) error { return errors.New("write failed") }
func (c *firstReadFailWriteConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("closed")
}
func (c *firstReadFailWriteConn) WriteMessage(messageType int, data []byte) error {
	return errors.New("write failed")
}
func (c *firstReadFailWriteConn) Close() error { return nil }

func TestTokenManagerRemainingBranches(t *testing.T) {
	empty := NewTokenManager("")
	if _, err := empty.Issue(Claims{AgentID: "a", SessionID: "s"}, time.Second); err == nil {
		t.Fatal("expected issue error for empty secret")
	}
	if _, err := empty.Verify("x"); err == nil {
		t.Fatal("expected verify error for empty secret")
	}

	mgr := NewTokenManager("secret")
	if _, err := mgr.Verify("not-a-token"); err == nil {
		t.Fatal("expected parse error")
	}

	// ParseWithClaims succeeds, but claims are not tokenClaims.
	tok := jwt.New(jwt.SigningMethodHS256)
	signed, err := tok.SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, _ = mgr.Verify(signed)
}

func TestServeWSUpgradeFailureAuthorized(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{"agent1": "secret1"})
	token, err := h.tokens.Issue(Claims{AgentID: "agent1", SessionID: "ai", Write: true}, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ws/client?token="+token, nil)
	rec := httptest.NewRecorder()
	h.ServeClientWS(rec, req) // plain HTTP request triggers upgrader error path

	req = httptest.NewRequest(http.MethodGet, "/ws/agent?agent_id=agent1&agent_secret=secret1", nil)
	rec = httptest.NewRecorder()
	h.ServeAgentWS(rec, req) // plain HTTP request triggers upgrader error path
}

func TestTokenAndLockHandlerRemainingBranches(t *testing.T) {
	h := NewHub(NewTokenManager(""), nil)
	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(`{"agent_id":"a","session_id":"s"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec := httptest.NewRecorder()
	h.TokenHandler("admin", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	h2 := NewHub(NewTokenManager("secret"), nil)
	h2.registerAgent("agent1", newFakeConn())
	h2.setSession("agent1", "ai")
	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"wrong","writer":"web"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h2.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHubUtilityRemainingBranches(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)

	// unregister mismatch branch
	connA := newFakeConn()
	connB := newFakeConn()
	h.registerAgent("a", connA)
	h.unregisterAgent("a", connB)
	if h.currentAgent("a") == nil {
		t.Fatal("expected agent to remain registered")
	}

	// addClient missing and nil-map branch
	if h.addClient("missing", &clientState{id: "c1"}) {
		t.Fatal("expected addClient false for missing agent")
	}
	state := h.registerAgent("b", newFakeConn())
	state.clients = nil
	if !h.addClient("b", &clientState{id: "c2", conn: newFakeConn()}) {
		t.Fatal("expected addClient true")
	}

	// removeClient writer branch
	h.setWriter("b", "c2")
	if !h.removeClient("b", "c2") {
		t.Fatal("expected removeClient to clear writer")
	}

	// nil-state branches
	h.setSession("missing", "s")
	if h.isWriter("missing", "c") {
		t.Fatal("unexpected writer for missing agent")
	}
	if h.hasWriter("missing") {
		t.Fatal("unexpected hasWriter for missing agent")
	}
	h.setWriter("missing", "c")
	h.clearWriter("missing")
	if h.webWritable("missing") {
		t.Fatal("unexpected webWritable for missing agent")
	}
	h.broadcastWriteStatus("missing")
	h.syncSessions("missing")

	// deliverSessions nil and full-channel default paths.
	h.deliverSessions("missing", []string{"x"})
	h.pendingMu.Lock()
	ch := make(chan []string, 1)
	ch <- []string{"already-full"}
	h.pendingSessions["req-full"] = ch
	h.pendingMu.Unlock()
	h.deliverSessions("req-full", []string{"new"})
	select {
	case got := <-ch:
		if len(got) != 1 || got[0] != "already-full" {
			t.Fatalf("unexpected channel contents: %+v", got)
		}
	default:
		t.Fatal("expected buffered value")
	}

	// requestSessions timeout path
	h.registerAgent("timeout-agent", &noReplyConn{})
	start := time.Now()
	_, err := h.requestSessions("timeout-agent")
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) < 2*time.Second {
		t.Fatal("expected requestSessions to wait for timeout")
	}
}

func TestSessionsAndIPBranches(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	req := httptest.NewRequest(http.MethodGet, "/sessions?agent_id=a", nil)
	rec := httptest.NewRecorder()
	h.SessionsHandler("").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	if isTailnetRequest(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("expected false for empty remote addr")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	if !isTailnetRequest(req) {
		t.Fatal("expected true for loopback")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "bad-remote"
	if isTailnetRequest(req) {
		t.Fatal("expected false for bad remote")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1"
	if ip := remoteIP(req); ip == nil || ip.String() != "127.0.0.1" {
		t.Fatalf("unexpected remote ip: %v", ip)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ""
	if ip := remoteIP(req); ip != nil {
		t.Fatalf("expected nil ip, got %v", ip)
	}
}

func TestServeAgentConnRemainingBranches(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	agentConn := newFakeConn()
	done := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(done)
	}()
	waitForAgent(t, h, "agent1")

	// decode error path
	agentConn.sendBinary([]byte{1})
	// no clients path
	frameData, _ := muxframe.Encode("s1", []byte("x"))
	agentConn.sendBinary(frameData)
	time.Sleep(50 * time.Millisecond)

	// add proto1 client with different session to hit sid mismatch path
	clientConn := newFakeConn()
	client := &clientState{id: "c1", conn: clientConn, proto: 1, session: "other"}
	if !h.addClient("agent1", client) {
		t.Fatal("expected addClient true")
	}
	frameData, _ = muxframe.Encode("s1", []byte("x"))
	agentConn.sendBinary(frameData)

	// bad text json
	agentConn.in <- frame{msgType: websocket.TextMessage, data: []byte("{")}
	// default message type
	agentConn.in <- frame{msgType: websocket.PingMessage, data: []byte("x")}
	time.Sleep(50 * time.Millisecond)

	agentConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent handler did not exit")
	}
}

func TestServeClientConnRemainingBranches(t *testing.T) {
	newHubWithAgent := func() (*Hub, *fakeConn, chan struct{}) {
		h := NewHub(NewTokenManager("secret"), nil)
		agentConn := newFakeConn()
		agentDone := make(chan struct{})
		go func() {
			h.ServeAgentConn("agent1", agentConn)
			close(agentDone)
		}()
		waitForAgent(t, h, "agent1")
		return h, agentConn, agentDone
	}

	t.Run("beforeAddClient hook and addClient false", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		defer func() {
			agentConn.Close()
			<-agentDone
		}()
		testHookBeforeAddClient = func() {
			h.unregisterAgent("agent1", agentConn)
		}
		t.Cleanup(func() { testHookBeforeAddClient = nil })

		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		msg := readUntilType(t, clientConn, "error")
		if msg.Code != "agent_offline" {
			t.Fatalf("unexpected error: %+v", msg)
		}
		clientConn.Close()
		<-done
	})

	t.Run("invalid json in loop", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		clientConn.in <- frame{msgType: websocket.TextMessage, data: []byte("{")}
		errMsg := readUntilType(t, clientConn, "error")
		if errMsg.Code != "bad_request" {
			t.Fatalf("unexpected error: %+v", errMsg)
		}
		<-done
		agentConn.Close()
		<-agentDone
	})

	t.Run("proto2 attach session mismatch", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 2})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		clientConn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "s2"})
		errMsg := readUntilType(t, clientConn, "error")
		if errMsg.Code != "not_authorized" {
			t.Fatalf("unexpected error: %+v", errMsg)
		}
		<-done
		agentConn.Close()
		<-agentDone
	})

	t.Run("proto2 activeSession empty then set", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "", ProtocolVersion: 2})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "", []string{})
		clientConn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "s2"})
		readAgentAttachAndSync(t, agentConn, "s2", []string{"s2"})
		clientConn.Close()
		<-done
		agentConn.Close()
		<-agentDone
	})

	t.Run("request_write denied when claims write false and binary ignored", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", SessionID: "s1", Write: false})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
		msg := readUntilType(t, clientConn, "write_denied")
		if msg.Code != "not_authorized" {
			t.Fatalf("unexpected write_denied: %+v", msg)
		}
		clientConn.sendBinary([]byte("ignored"))
		select {
		case <-agentConn.out:
		case <-time.After(200 * time.Millisecond):
		}
		clientConn.Close()
		<-done
		agentConn.Close()
		<-agentDone
	})

	t.Run("request_write locked", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		if !h.setWebWritable("agent1", "s1", false) {
			t.Fatal("failed to lock web writable")
		}
		clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
		msg := readUntilType(t, clientConn, "write_denied")
		if msg.Code != "locked" {
			t.Fatalf("unexpected write_denied: %+v", msg)
		}
		clientConn.Close()
		<-done
		agentConn.Close()
		<-agentDone
	})

	t.Run("request_write denied another writer", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		client1 := newFakeConn()
		client2 := newFakeConn()
		done1 := make(chan struct{})
		done2 := make(chan struct{})
		go func() {
			h.ServeClientConn(client1, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done1)
		}()
		go func() {
			h.ServeClientConn(client2, Claims{AgentID: "agent1", SessionID: "s1", Write: true})
			close(done2)
		}()
		client1.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		_ = readUntilType(t, client1, "attached")
		_ = readUntilType(t, client1, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		client2.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
		_ = readUntilType(t, client2, "attached")
		_ = readUntilType(t, client2, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		client1.sendJSON(t, ControlMessage{Type: "request_write"})
		_ = readUntilType(t, client1, "write_status")
		_ = readUntilType(t, client2, "write_status")
		client2.sendJSON(t, ControlMessage{Type: "request_write"})
		msg := readUntilType(t, client2, "write_denied")
		if msg.Code != "another_writer" {
			t.Fatalf("unexpected write_denied: %+v", msg)
		}
		client1.Close()
		client2.Close()
		<-done1
		<-done2
		agentConn.Close()
		<-agentDone
	})

	t.Run("proto2 detach and active require session", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 2})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		clientConn.sendJSON(t, ControlMessage{Type: "detach"})
		msg := readUntilType(t, clientConn, "error")
		if msg.Code != "bad_request" {
			t.Fatalf("unexpected detach error: %+v", msg)
		}
		<-done

		clientConn2 := newFakeConn()
		done2 := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn2, Claims{AgentID: "agent1", Write: true})
			close(done2)
		}()
		clientConn2.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 2})
		_ = readUntilType(t, clientConn2, "attached")
		_ = readUntilType(t, clientConn2, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
		clientConn2.sendJSON(t, ControlMessage{Type: "active"})
		msg = readUntilType(t, clientConn2, "error")
		if msg.Code != "bad_request" {
			t.Fatalf("unexpected active error: %+v", msg)
		}
		<-done2
		agentConn.Close()
		<-agentDone
	})

	t.Run("proto2 resize fills active, decode error on binary", func(t *testing.T) {
		h, agentConn, agentDone := newHubWithAgent()
		clientConn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(clientConn, Claims{AgentID: "agent1", Write: true})
			close(done)
		}()
		clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 2})
		_ = readUntilType(t, clientConn, "attached")
		_ = readUntilType(t, clientConn, "write_status")
		readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})

		clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
		_ = readUntilType(t, clientConn, "write_status")
		clientConn.sendJSON(t, ControlMessage{Type: "resize", Cols: 120, Rows: 40})
		msg := readControl(t, agentConn)
		if msg.Type != "resize" || msg.SessionID != "s1" {
			t.Fatalf("unexpected resize forwarded: %+v", msg)
		}

		clientConn.sendBinary([]byte{1})
		clientConn.sendJSON(t, ControlMessage{Type: "focus", State: "on"})
		clientConn.Close()
		<-done
		agentConn.Close()
		<-agentDone
	})
}

func TestServeClientConnValidationBranches(t *testing.T) {
	t.Run("readjson error", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		conn.Close()
		h.ServeClientConn(conn, Claims{})
	})

	t.Run("first message not attach", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "resize"})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "bad_request" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})

	t.Run("bad version", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{AgentID: "a", SessionID: "s", Write: true})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "a", SessionID: "s", ProtocolVersion: 3})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "bad_version" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})

	t.Run("claims empty requires agent and session", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "attach", ProtocolVersion: 1})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "bad_request" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})

	t.Run("agent mismatch", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{AgentID: "a", SessionID: "s", Write: true})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "b", SessionID: "s", ProtocolVersion: 1})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "not_authorized" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})

	t.Run("session mismatch proto1", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{AgentID: "a", SessionID: "s1", Write: true})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "a", SessionID: "s2", ProtocolVersion: 1})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "not_authorized" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})

	t.Run("session mismatch proto2", func(t *testing.T) {
		h := NewHub(NewTokenManager("secret"), nil)
		conn := newFakeConn()
		done := make(chan struct{})
		go func() {
			h.ServeClientConn(conn, Claims{AgentID: "a", SessionID: "s1", Write: true})
			close(done)
		}()
		conn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "a", SessionID: "s2", ProtocolVersion: 2})
		msg := readControl(t, conn)
		if msg.Type != "error" || msg.Code != "not_authorized" {
			t.Fatalf("unexpected msg: %+v", msg)
		}
		conn.Close()
		<-done
	})
}

func TestServeClientConnProtoAndLoopBranches(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	agentConn := newFakeConn()
	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")

	// Proto 0 + derived claims branch.
	clientProto0 := newFakeConn()
	done0 := make(chan struct{})
	go func() {
		h.ServeClientConn(clientProto0, Claims{})
		close(done0)
	}()
	clientProto0.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 0})
	_ = readUntilType(t, clientProto0, "attached")
	_ = readUntilType(t, clientProto0, "write_status")
	readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})
	clientProto0.Close()
	<-done0

	// Proto 1 with empty claim session hits line 341 and firstSession fallback.
	clientProto1 := newFakeConn()
	done1 := make(chan struct{})
	go func() {
		h.ServeClientConn(clientProto1, Claims{AgentID: "agent1", Write: true})
		close(done1)
	}()
	clientProto1.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "", ProtocolVersion: 1})
	_ = readUntilType(t, clientProto1, "attached")
	_ = readUntilType(t, clientProto1, "write_status")
	readAgentAttachAndSync(t, agentConn, "", []string{"s1"})
	clientProto1.sendJSON(t, ControlMessage{Type: "attach", SessionID: "ignored"})
	clientProto1.sendJSON(t, ControlMessage{Type: "detach", SessionID: "ignored"})
	clientProto1.sendJSON(t, ControlMessage{Type: "active", SessionID: "ignored"})
	clientProto1.sendJSON(t, ControlMessage{Type: "request_write"})
	_ = readUntilType(t, clientProto1, "write_status")
	clientProto1.sendBinary([]byte("input"))
	select {
	case <-agentConn.out:
	case <-time.After(200 * time.Millisecond):
	}
	clientProto1.Close()
	<-done1

	// Proto 2 negative branches in control and binary handling.
	clientProto2 := newFakeConn()
	done2 := make(chan struct{})
	go func() {
		h.ServeClientConn(clientProto2, Claims{AgentID: "agent1", Write: true})
		close(done2)
	}()
	clientProto2.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "", ProtocolVersion: 2})
	_ = readUntilType(t, clientProto2, "attached")
	_ = readUntilType(t, clientProto2, "write_status")
	readAgentAttachAndSync(t, agentConn, "", []string{"s1"})

	clientProto2.sendJSON(t, ControlMessage{Type: "attach"})
	errMsg := readUntilType(t, clientProto2, "error")
	if errMsg.Code != "bad_request" {
		t.Fatalf("unexpected attach error: %+v", errMsg)
	}
	<-done2

	// client write failure branch (attached write fails)
	payload, _ := json.Marshal(ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 1})
	failConn := &firstReadFailWriteConn{readPayload: payload}
	h.ServeClientConn(failConn, Claims{AgentID: "agent1", SessionID: "s1", Write: true})

	agentConn.Close()
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent handler did not exit")
	}
}

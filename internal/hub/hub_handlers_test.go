package hub

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHubUtilityHelpers(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{"agent1": "secret1"})

	var buf bytes.Buffer
	h.SetLogger(log.New(&buf, "", 0))
	h.logger.Print("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("expected logger output, got %q", buf.String())
	}

	h.SetLogger(nil)
	if h.logger == nil {
		t.Fatal("expected non-nil default logger")
	}

	if !h.validateAgent("agent1", "secret1") {
		t.Fatal("expected valid agent credentials")
	}
	if h.validateAgent("agent1", "bad") {
		t.Fatal("expected invalid credentials")
	}
	if h.validateAgent("", "secret1") {
		t.Fatal("expected empty agent id to be rejected")
	}

	if got := timeSeconds(0); got != 60*time.Second {
		t.Fatalf("expected default ttl 60s, got %v", got)
	}
	if got := timeSeconds(7); got != 7*time.Second {
		t.Fatalf("expected ttl 7s, got %v", got)
	}

	conn := newFakeConn()
	sendError(conn, "bad_request", "boom")
	msg := readControl(t, conn)
	if msg.Type != "error" || msg.Code != "bad_request" || msg.Message != "boom" {
		t.Fatalf("unexpected error message: %+v", msg)
	}
}

func TestServeClientWSUnauthorized(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	req := httptest.NewRequest(http.MethodGet, "/ws/client", nil)
	rec := httptest.NewRecorder()
	h.ServeClientWS(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestServeClientWSUpgradeAndAgentOffline(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	token, err := h.tokens.Issue(Claims{AgentID: "agent1", SessionID: "ai", Write: true}, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(h.ServeClientWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	if err := conn.WriteJSON(ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1}); err != nil {
		t.Fatalf("write attach: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg ControlMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" || msg.Code != "agent_offline" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestServeAgentWSUnauthorized(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{"agent1": "secret1"})
	req := httptest.NewRequest(http.MethodGet, "/ws/agent?agent_id=agent1&agent_secret=wrong", nil)
	rec := httptest.NewRecorder()
	h.ServeAgentWS(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestServeAgentWSUpgradeRegistersAndUnregisters(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{"agent1": "secret1"})
	ts := httptest.NewServer(http.HandlerFunc(h.ServeAgentWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/?agent_id=agent1&agent_secret=secret1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitForAgent(t, h, "agent1")
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.currentAgent("agent1") == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent was not unregistered after websocket close")
}

func TestTokenHandler(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)

	req := httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(`{"agent_id":"a","session_id":"s"}`))
	rec := httptest.NewRecorder()
	h.TokenHandler("", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(`{"agent_id":"a","session_id":"s"}`))
	rec = httptest.NewRecorder()
	h.TokenHandler("admin", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString("{"))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.TokenHandler("admin", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(`{"agent_id":"","session_id":"s"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.TokenHandler("admin", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/token", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","write":true}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.TokenHandler("admin", 60).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp tokenResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	claims, err := h.tokens.Verify(resp.Token)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.AgentID != "agent1" || claims.SessionID != "ai" || !claims.Write {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestLockHandler(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), nil)
	h.registerAgent("agent1", newFakeConn())
	h.setSession("agent1", "ai")

	req := httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","writer":"none"}`))
	rec := httptest.NewRecorder()
	h.LockHandler("").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","writer":"none"}`))
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString("{"))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"","session_id":"ai","writer":"none"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","writer":"desktop"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"missing","session_id":"ai","writer":"none"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"wrong","writer":"none"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","writer":"none"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if h.webWritable("agent1") {
		t.Fatal("expected web writer disabled")
	}

	req = httptest.NewRequest(http.MethodPost, "/lock", bytes.NewBufferString(`{"agent_id":"agent1","session_id":"ai","writer":"web"}`))
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.LockHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !h.webWritable("agent1") {
		t.Fatal("expected web writer enabled")
	}
}

func TestSessionsHandler(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{"agent1": "secret1"})

	req := httptest.NewRequest(http.MethodGet, "/sessions?agent_id=agent1", nil)
	rec := httptest.NewRecorder()
	h.SessionsHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/sessions?agent_id=agent1", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.SessionsHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/sessions", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.SessionsHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/sessions?agent_id=agent1", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.SessionsHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	agentConn := newFakeConn()
	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")
	go func() {
		for {
			select {
			case <-agentConn.closed:
				return
			case f := <-agentConn.out:
				if f.msgType != websocket.TextMessage {
					continue
				}
				var msg ControlMessage
				if err := json.Unmarshal(f.data, &msg); err != nil {
					continue
				}
				if msg.Type == "list_sessions" && msg.RequestID != "" {
					b, _ := json.Marshal(ControlMessage{
						Type:      "sessions",
						RequestID: msg.RequestID,
						Sessions:  []string{"ai", "ops"},
					})
					agentConn.in <- frame{msgType: websocket.TextMessage, data: b}
					return
				}
			}
		}
	}()

	req = httptest.NewRequest(http.MethodGet, "/sessions?agent_id=agent1", nil)
	req.Header.Set("Authorization", "Bearer admin")
	rec = httptest.NewRecorder()
	h.SessionsHandler("admin").ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string][]string
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	sessions := payload["sessions"]
	if len(sessions) != 2 || sessions[0] != "ai" || sessions[1] != "ops" {
		t.Fatalf("unexpected sessions payload: %+v", payload)
	}

	h.AuthMode = AuthModeTailnet
	h.TailnetOnly = true
	req = httptest.NewRequest(http.MethodGet, "/sessions?agent_id=agent1", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec = httptest.NewRecorder()
	h.SessionsHandler("ignored").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	_ = agentConn.Close()
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatal("agent handler did not exit")
	}
}

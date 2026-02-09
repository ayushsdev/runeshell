package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"runeshell/internal/agent"
	"runeshell/internal/hub"
	"runeshell/internal/termserver"
)

type integrationSession struct {
	writeCh  chan []byte
	resizeCh chan [2]int
	outputCh chan []byte
	closeCh  chan struct{}
	once     sync.Once
}

func newIntegrationSession() *integrationSession {
	return &integrationSession{
		writeCh:  make(chan []byte, 8),
		resizeCh: make(chan [2]int, 8),
		outputCh: make(chan []byte, 8),
		closeCh:  make(chan struct{}, 1),
	}
}

func (s *integrationSession) Write(p []byte) error {
	cp := make([]byte, len(p))
	copy(cp, p)
	s.writeCh <- cp
	return nil
}

func (s *integrationSession) Resize(cols, rows int) error {
	s.resizeCh <- [2]int{cols, rows}
	return nil
}

func (s *integrationSession) Output() <-chan []byte {
	return s.outputCh
}

func (s *integrationSession) Close() error {
	s.once.Do(func() { s.closeCh <- struct{}{} })
	return nil
}

type integrationManager struct {
	mu       sync.Mutex
	attachCh chan string
	list     []string
	sessions map[string]*integrationSession
}

func newIntegrationManager(list []string) *integrationManager {
	return &integrationManager{
		attachCh: make(chan string, 8),
		list:     append([]string{}, list...),
		sessions: make(map[string]*integrationSession),
	}
}

func (m *integrationManager) Attach(sessionID string) (termserver.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = newIntegrationSession()
		m.sessions[sessionID] = s
	}
	m.attachCh <- sessionID
	return s, nil
}

func (m *integrationManager) ListSessions() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string{}, m.list...)
	return out, nil
}

func (m *integrationManager) session(id string) *integrationSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func TestHubAgentClientRoundTripAndSessionsEndpoint(t *testing.T) {
	tokens := hub.NewTokenManager("dev-secret")
	manager := newIntegrationManager([]string{"ai", "ops"})
	h := hub.NewHub(tokens, map[string]string{"agent1": "agent-secret"})
	h.SetLogger(log.New(io.Discard, "", 0))

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/client", h.ServeClientWS)
	mux.HandleFunc("/ws/agent", h.ServeAgentWS)
	mux.HandleFunc("/api/sessions", h.SessionsHandler("dev-admin"))
	ts := newHTTPTestServerOrSkip(t, mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentErr := make(chan error, 1)
	go func() {
		c := &agent.Client{
			HubURL:  toWS(ts.URL) + "/ws/agent",
			AgentID: "agent1",
			Secret:  "agent-secret",
			Manager: manager,
			Logger:  log.New(io.Discard, "", 0),
		}
		agentErr <- c.Run(ctx)
	}()

	waitForAgentReady(t, ts.URL)

	token, err := tokens.Issue(hub.Claims{
		AgentID:   "agent1",
		SessionID: "ai",
		Write:     true,
	}, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	clientConn, _, err := websocket.DefaultDialer.Dial(toWS(ts.URL)+"/ws/client?token="+token, nil)
	if err != nil {
		t.Fatalf("dial client ws: %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))

	attach := hub.ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1}
	if err := clientConn.WriteJSON(attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	select {
	case sid := <-manager.attachCh:
		if sid != "ai" {
			t.Fatalf("unexpected attached session %q", sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attach did not reach agent")
	}

	if _, err := readControlOfType(clientConn, "attached"); err != nil {
		t.Fatalf("read attached: %v", err)
	}
	if _, err := readControlOfType(clientConn, "write_status"); err != nil {
		t.Fatalf("read initial write_status: %v", err)
	}

	if err := clientConn.WriteJSON(hub.ControlMessage{Type: "request_write"}); err != nil {
		t.Fatalf("request_write: %v", err)
	}
	msg, err := readControlOfType(clientConn, "write_status")
	if err != nil {
		t.Fatalf("read writer status: %v", err)
	}
	if !msg.Write {
		t.Fatalf("expected write=true after request_write")
	}

	if err := clientConn.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	sess := manager.session("ai")
	if sess == nil {
		t.Fatal("expected ai session")
	}
	select {
	case in := <-sess.writeCh:
		if string(in) != "ping" {
			t.Fatalf("unexpected stdin payload: %q", string(in))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive stdin at agent session")
	}

	sess.outputCh <- []byte("pong")
	if got, err := readBinary(clientConn); err != nil {
		t.Fatalf("read binary from client: %v", err)
	} else if string(got) != "pong" {
		t.Fatalf("unexpected output payload: %q", string(got))
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions?agent_id=agent1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dev-admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sessions request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("sessions status %d body=%s", resp.StatusCode, string(body))
	}
	var payload struct {
		Sessions []string `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(payload.Sessions) != 2 || payload.Sessions[0] != "ai" || payload.Sessions[1] != "ops" {
		t.Fatalf("unexpected sessions payload: %+v", payload.Sessions)
	}

	cancel()
	ts.CloseClientConnections()
	select {
	case <-agentErr:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not exit")
	}
}

func waitForAgentReady(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/sessions?agent_id=agent1", nil)
		req.Header.Set("Authorization", "Bearer dev-admin")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("agent did not become ready")
}

func readControlOfType(conn *websocket.Conn, want string) (hub.ControlMessage, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			return hub.ControlMessage{}, err
		}
		if mt != websocket.TextMessage {
			continue
		}
		var msg hub.ControlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == want {
			return msg, nil
		}
	}
	return hub.ControlMessage{}, errors.New("timeout waiting for control message " + want)
}

func readBinary(conn *websocket.Conn) ([]byte, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			return nil, err
		}
		if mt == websocket.BinaryMessage {
			return data, nil
		}
	}
	return nil, errors.New("timeout waiting for binary message")
}

func toWS(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func newHTTPTestServerOrSkip(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			if strings.Contains(msg, "failed to listen on a port") ||
				strings.Contains(msg, "operation not permitted") ||
				strings.Contains(msg, "permission denied") {
				t.Skipf("network listen not permitted in this environment: %s", msg)
			}
			panic(r)
		}
	}()
	return httptest.NewServer(handler)
}

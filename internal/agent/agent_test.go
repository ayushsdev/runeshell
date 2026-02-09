package agent

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

	"runeshell/internal/hub"
	"runeshell/internal/muxframe"
	"runeshell/internal/termserver"
)

type fakeSession struct {
	writeCh  chan []byte
	resizeCh chan [2]int
	outputCh chan []byte
	closeCh  chan struct{}
	once     sync.Once
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
	s.once.Do(func() {
		s.closeCh <- struct{}{}
	})
	return nil
}

type fakeManager struct {
	mu        sync.Mutex
	attachErr error
	attachCh  chan string
	sessions  map[string]*fakeSession
	list      []string
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		attachCh: make(chan string, 8),
		sessions: make(map[string]*fakeSession),
	}
}

func (m *fakeManager) Attach(sessionID string) (termserver.Session, error) {
	if m.attachErr != nil {
		return nil, m.attachErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = newFakeSession()
		m.sessions[sessionID] = s
	}
	m.attachCh <- sessionID
	return s, nil
}

func (m *fakeManager) ListSessions() ([]string, error) {
	out := make([]string, len(m.list))
	copy(out, m.list)
	return out, nil
}

func (m *fakeManager) session(id string) *fakeSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func TestClientRunRequiresManager(t *testing.T) {
	c := &Client{HubURL: "ws://example.test/ws/agent"}
	if err := c.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "manager required") {
		t.Fatalf("expected manager required error, got %v", err)
	}
}

func TestClientRunInvalidHubURL(t *testing.T) {
	c := &Client{
		HubURL:  "://bad-url",
		AgentID: "agent1",
		Secret:  "secret",
		Manager: newFakeManager(),
		Logger:  log.New(io.Discard, "", 0),
	}
	if err := c.Run(context.Background()); err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestClientRunHandlesAttachResizeStdinStdoutAndListSessions(t *testing.T) {
	mgr := newFakeManager()
	mgr.list = []string{"ai", "ops"}

	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverDone := make(chan error, 1)
	ts := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/agent" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("agent_id") != "agent1" || r.URL.Query().Get("agent_secret") != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))

		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: "s1"}); err != nil {
			serverDone <- err
			return
		}

		select {
		case sid := <-mgr.attachCh:
			if sid != "s1" {
				serverDone <- errors.New("unexpected attach session id")
				return
			}
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("attach timeout")
			return
		}

		sess := mgr.session("s1")
		if sess == nil {
			serverDone <- errors.New("session not attached")
			return
		}

		sess.outputCh <- []byte("hello")
		mt, data, err := conn.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if mt != websocket.BinaryMessage {
			serverDone <- errors.New("expected binary frame from agent")
			return
		}
		gotSID, payload, err := muxframe.Decode(data)
		if err != nil {
			serverDone <- err
			return
		}
		if gotSID != "s1" || string(payload) != "hello" {
			serverDone <- errors.New("unexpected stdout frame payload")
			return
		}

		if err := conn.WriteJSON(hub.ControlMessage{Type: "resize", SessionID: "s1", Cols: 120, Rows: 40}); err != nil {
			serverDone <- err
			return
		}
		select {
		case dims := <-sess.resizeCh:
			if dims != [2]int{120, 40} {
				serverDone <- errors.New("unexpected resize dims")
				return
			}
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("resize timeout")
			return
		}

		frame, err := muxframe.Encode("s1", []byte("input"))
		if err != nil {
			serverDone <- err
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			serverDone <- err
			return
		}
		select {
		case in := <-sess.writeCh:
			if string(in) != "input" {
				serverDone <- errors.New("unexpected stdin payload")
				return
			}
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("stdin timeout")
			return
		}

		if err := conn.WriteJSON(hub.ControlMessage{Type: "list_sessions", RequestID: "req-1"}); err != nil {
			serverDone <- err
			return
		}
		mt, data, err = conn.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if mt != websocket.TextMessage {
			serverDone <- errors.New("expected text response for sessions")
			return
		}
		var resp hub.ControlMessage
		if err := json.Unmarshal(data, &resp); err != nil {
			serverDone <- err
			return
		}
		if resp.Type != "sessions" || resp.RequestID != "req-1" {
			serverDone <- errors.New("unexpected sessions response")
			return
		}
		if len(resp.Sessions) != 2 || resp.Sessions[0] != "ai" || resp.Sessions[1] != "ops" {
			serverDone <- errors.New("unexpected sessions list")
			return
		}

		if err := conn.WriteJSON(hub.ControlMessage{Type: "detach", SessionID: "s1"}); err != nil {
			serverDone <- err
			return
		}
		select {
		case <-sess.closeCh:
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("detach close timeout")
			return
		}
		serverDone <- nil
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- (&Client{
			HubURL:  wsURLFromHTTP(ts.URL) + "/ws/agent",
			AgentID: "agent1",
			Secret:  "secret",
			Manager: mgr,
			Logger:  log.New(io.Discard, "", 0),
		}).Run(ctx)
	}()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server script timeout")
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit")
	}
}

func TestClientRunAttachErrorSendsControlError(t *testing.T) {
	mgr := newFakeManager()
	mgr.attachErr = errors.New("attach failed")
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

	serverDone := make(chan error, 1)
	ts := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))

		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: "s1"}); err != nil {
			serverDone <- err
			return
		}

		mt, data, err := conn.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if mt != websocket.TextMessage {
			serverDone <- errors.New("expected text error message")
			return
		}
		var msg hub.ControlMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			serverDone <- err
			return
		}
		if msg.Type != "error" || msg.Code != "attach_failed" {
			serverDone <- errors.New("unexpected error response")
			return
		}
		serverDone <- nil
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- (&Client{
			HubURL:  wsURLFromHTTP(ts.URL),
			AgentID: "agent1",
			Secret:  "secret",
			Manager: mgr,
			Logger:  log.New(io.Discard, "", 0),
		}).Run(ctx)
	}()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server timeout")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit")
	}
}

func TestRunWithRetryReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	client := &Client{
		HubURL:  "://bad-url",
		AgentID: "agent1",
		Secret:  "secret",
		Manager: newFakeManager(),
		Logger:  log.New(io.Discard, "", 0),
	}
	err := RunWithRetry(ctx, client, 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func wsURLFromHTTP(httpURL string) string {
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

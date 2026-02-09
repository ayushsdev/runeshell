package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"runeshell/internal/hub"
	"runeshell/internal/muxframe"
)

type managerNoLister struct {
	*fakeManager
}

type managerListErr struct {
	*fakeManager
}

func (m *managerListErr) ListSessions() ([]string, error) {
	return nil, errors.New("list failed")
}

func TestClientRunCoversMessageBranchesAndReadError(t *testing.T) {
	mgr := &managerNoLister{fakeManager: newFakeManager()}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverDone := make(chan error, 1)

	ts := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// invalid JSON -> continue
		if err := conn.WriteMessage(websocket.TextMessage, []byte("{")); err != nil {
			serverDone <- err
			return
		}
		// empty attach session -> continue
		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach"}); err != nil {
			serverDone <- err
			return
		}
		// real attach
		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: "s1"}); err != nil {
			serverDone <- err
			return
		}
		select {
		case sid := <-mgr.attachCh:
			if sid != "s1" {
				serverDone <- errors.New("unexpected attach id")
				return
			}
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("attach timeout")
			return
		}
		// duplicate attach -> existing session branch
		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: "s1"}); err != nil {
			serverDone <- err
			return
		}

		// resize empty -> continue
		if err := conn.WriteJSON(hub.ControlMessage{Type: "resize"}); err != nil {
			serverDone <- err
			return
		}
		// resize missing session id -> s == nil branch
		if err := conn.WriteJSON(hub.ControlMessage{Type: "resize", SessionID: "missing", Cols: 80, Rows: 24}); err != nil {
			serverDone <- err
			return
		}

		// heartbeat and default control
		if err := conn.WriteJSON(hub.ControlMessage{Type: "heartbeat"}); err != nil {
			serverDone <- err
			return
		}
		if err := conn.WriteJSON(hub.ControlMessage{Type: "unknown"}); err != nil {
			serverDone <- err
			return
		}

		// bad frame decode
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte{1}); err != nil {
			serverDone <- err
			return
		}
		// valid frame for missing session -> s == nil
		badSessFrame, err := muxframe.Encode("missing", []byte("x"))
		if err != nil {
			serverDone <- err
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, badSessFrame); err != nil {
			serverDone <- err
			return
		}

		// list_sessions with manager that is not SessionLister -> empty sessions response
		if err := conn.WriteJSON(hub.ControlMessage{Type: "list_sessions", RequestID: "req-no-lister"}); err != nil {
			serverDone <- err
			return
		}
		mt, data, err := conn.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if mt != websocket.TextMessage {
			serverDone <- errors.New("expected text sessions response")
			return
		}
		var resp hub.ControlMessage
		if err := json.Unmarshal(data, &resp); err != nil {
			serverDone <- err
			return
		}
		if resp.Type != "sessions" || resp.RequestID != "req-no-lister" || len(resp.Sessions) != 0 {
			serverDone <- errors.New("unexpected no-lister sessions response")
			return
		}

		// detach missing session -> closeSession no-op branch
		if err := conn.WriteJSON(hub.ControlMessage{Type: "detach", SessionID: "missing"}); err != nil {
			serverDone <- err
			return
		}

		// close socket without context cancel -> Run should return read error path.
		serverDone <- nil
	}))
	defer ts.Close()

	ctx := context.Background()
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

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected read error after server close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit")
	}
}

func TestClientRunCloseAllAndOutputBranches(t *testing.T) {
	mgr := newFakeManager()
	longID := strings.Repeat("x", 70000)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverDone := make(chan error, 1)

	ts := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// regular session for closeAll and output close path.
		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: "s-close"}); err != nil {
			serverDone <- err
			return
		}
		select {
		case <-mgr.attachCh:
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("attach timeout")
			return
		}
		s1 := mgr.session("s-close")
		if s1 == nil {
			serverDone <- errors.New("missing s-close")
			return
		}

		// close output channel to exercise !ok branch in output forwarder goroutine.
		close(s1.outputCh)

		// very long session id triggers muxframe.Encode error branch when output is produced.
		if err := conn.WriteJSON(hub.ControlMessage{Type: "attach", SessionID: longID}); err != nil {
			serverDone <- err
			return
		}
		select {
		case sid := <-mgr.attachCh:
			if sid != longID {
				serverDone <- errors.New("unexpected long attach id")
				return
			}
		case <-time.After(2 * time.Second):
			serverDone <- errors.New("long attach timeout")
			return
		}
		sLong := mgr.session(longID)
		if sLong == nil {
			serverDone <- errors.New("missing long session")
			return
		}
		sLong.outputCh <- []byte("trigger-encode-error")

		// list_sessions with erroring lister path
		if err := conn.WriteJSON(hub.ControlMessage{Type: "list_sessions", RequestID: "req-list-err"}); err != nil {
			serverDone <- err
			return
		}
		mt, data, err := conn.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if mt != websocket.TextMessage {
			serverDone <- errors.New("expected text sessions response")
			return
		}
		var resp hub.ControlMessage
		if err := json.Unmarshal(data, &resp); err != nil {
			serverDone <- err
			return
		}
		if resp.Type != "sessions" || resp.RequestID != "req-list-err" || len(resp.Sessions) != 0 {
			serverDone <- errors.New("unexpected list error response")
			return
		}

		serverDone <- nil
		// Keep socket open until client cancels context.
		<-time.After(4 * time.Second)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		client := &Client{
			HubURL:  wsURLFromHTTP(ts.URL),
			AgentID: "agent1",
			Secret:  "secret",
			Manager: &managerListErr{fakeManager: mgr},
			Logger:  log.New(io.Discard, "", 0),
		}
		done <- client.Run(ctx)
	}()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server timeout")
	}

	// cancel to hit ctx.Done and closeAll branch.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit")
	}

	if sess := mgr.session("s-close"); sess != nil {
		select {
		case <-sess.closeCh:
		default:
			t.Fatal("expected closeAll to close s-close")
		}
	}
}

func TestClientRunLoggerNilAndDialError(t *testing.T) {
	client := &Client{
		HubURL:  "ws://127.0.0.1:1/ws",
		AgentID: "agent1",
		Secret:  "secret",
		Manager: newFakeManager(),
	}
	err := client.Run(context.Background())
	if err == nil {
		t.Fatal("expected dial error")
	}
	if client.Logger == nil {
		t.Fatal("expected default logger to be set")
	}
}

func TestRunWithRetryDefaultRetryBranch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	client := &Client{
		HubURL:  "://bad-url",
		AgentID: "agent1",
		Secret:  "secret",
		Manager: newFakeManager(),
		Logger:  log.New(io.Discard, "", 0),
	}
	err := RunWithRetry(ctx, client, 0)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func TestClientRunContextDoneAtLoopTop(t *testing.T) {
	mgr := newFakeManager()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverDone := make(chan error, 1)

	ts := newHTTPTestServerOrSkip(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Feed a message so the loop iterates again and can observe ctx.Done before next read.
		if err := conn.WriteJSON(hub.ControlMessage{Type: "heartbeat"}); err != nil {
			serverDone <- err
			return
		}
		time.Sleep(200 * time.Millisecond)
		serverDone <- nil
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
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

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not exit")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server timeout")
	}
}

func TestRunWithRetryContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &Client{
		HubURL:  "://bad-url",
		AgentID: "agent1",
		Secret:  "secret",
		Manager: newFakeManager(),
		Logger:  log.New(io.Discard, "", 0),
	}
	err := RunWithRetry(ctx, client, 5*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}

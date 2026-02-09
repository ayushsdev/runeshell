package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"runeshell/internal/muxframe"
)

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

type blockingAgentConn struct {
	writeInProgress int32
	listStarted     chan string
	unblockList     chan struct{}
	closed          chan struct{}
}

func newBlockingAgentConn() *blockingAgentConn {
	return &blockingAgentConn{
		listStarted: make(chan string, 1),
		unblockList: make(chan struct{}),
		closed:      make(chan struct{}),
	}
}

func (c *blockingAgentConn) ReadJSON(v any) error {
	return errors.New("not implemented")
}

func (c *blockingAgentConn) WriteJSON(v any) error {
	if !atomic.CompareAndSwapInt32(&c.writeInProgress, 0, 1) {
		panic("concurrent write to websocket connection")
	}
	if msg, ok := v.(ControlMessage); ok && msg.Type == "list_sessions" {
		if msg.RequestID != "" {
			select {
			case c.listStarted <- msg.RequestID:
			default:
			}
		}
		select {
		case <-c.unblockList:
		case <-c.closed:
		}
	}
	atomic.StoreInt32(&c.writeInProgress, 0)
	return nil
}

func (c *blockingAgentConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("not implemented")
}

func (c *blockingAgentConn) WriteMessage(messageType int, data []byte) error {
	if !atomic.CompareAndSwapInt32(&c.writeInProgress, 0, 1) {
		panic("concurrent write to websocket connection")
	}
	atomic.StoreInt32(&c.writeInProgress, 0)
	return nil
}

func (c *blockingAgentConn) Close() error {
	select {
	case <-c.closed:
		return nil
	default:
		close(c.closed)
		return nil
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

func readControl(t *testing.T, c *fakeConn) ControlMessage {
	t.Helper()
	f := c.readFrame(t)
	if f.msgType != websocket.TextMessage {
		t.Fatalf("expected text message")
	}
	var msg ControlMessage
	if err := json.Unmarshal(f.data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return msg
}

func readUntilType(t *testing.T, c *fakeConn, want string) ControlMessage {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for %s", want)
			return ControlMessage{}
		default:
			msg := readControl(t, c)
			if msg.Type == want {
				return msg
			}
		}
	}
}

func readUntilBinary(t *testing.T, c *fakeConn) frame {
	t.Helper()
	for {
		f := c.readFrame(t)
		if f.msgType == websocket.BinaryMessage {
			return f
		}
	}
}

func readFromChanType(t *testing.T, ch <-chan ControlMessage, want string) ControlMessage {
	t.Helper()
	select {
	case msg := <-ch:
		if msg.Type != want {
			t.Fatalf("expected %s, got %+v", want, msg)
		}
		return msg
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", want)
		return ControlMessage{}
	}
}

func readAgentAttachAndSync(t *testing.T, agentConn *fakeConn, sessionID string, sessions []string) {
	t.Helper()
	seenAttach := false
	seenList := false
	for !(seenAttach && seenList) {
		msg := readControl(t, agentConn)
		switch msg.Type {
		case "attach":
			if msg.SessionID != sessionID {
				t.Fatalf("unexpected attach: %+v", msg)
			}
			seenAttach = true
		case "list_sessions":
			if msg.RequestID == "" {
				t.Fatalf("expected list_sessions request_id, got %+v", msg)
			}
			agentConn.sendJSON(t, ControlMessage{Type: "sessions", RequestID: msg.RequestID, Sessions: sessions})
			seenList = true
		default:
			t.Fatalf("unexpected agent msg: %+v", msg)
		}
	}
}

func readAgentDetachAndSync(t *testing.T, agentConn *fakeConn, sessionID string, sessions []string) {
	t.Helper()
	seenDetach := false
	seenList := false
	for !(seenDetach && seenList) {
		msg := readControl(t, agentConn)
		switch msg.Type {
		case "detach":
			if msg.SessionID != sessionID {
				t.Fatalf("unexpected detach: %+v", msg)
			}
			seenDetach = true
		case "list_sessions":
			if msg.RequestID == "" {
				t.Fatalf("expected list_sessions request_id, got %+v", msg)
			}
			agentConn.sendJSON(t, ControlMessage{Type: "sessions", RequestID: msg.RequestID, Sessions: sessions})
			seenList = true
		default:
			t.Fatalf("unexpected agent msg: %+v", msg)
		}
	}
}

func waitForAgent(t *testing.T, h *Hub, agentID string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.currentAgent(agentID) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent %s not registered", agentID)
}

func TestTokenManagerIssueVerify(t *testing.T) {
	mgr := NewTokenManager("secret")
	claims := Claims{AgentID: "agent1", SessionID: "ai", Write: true}
	tok, err := mgr.Issue(claims, time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	out, err := mgr.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.AgentID != "agent1" || out.SessionID != "ai" || !out.Write {
		t.Fatalf("unexpected claims: %+v", out)
	}
}

func TestHubRoutesBetweenClientAndAgent(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, map[string]string{"agent1": "agent-secret"})

	agentConn := newFakeConn()
	clientConn := newFakeConn()

	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")

	claims := Claims{AgentID: "agent1", SessionID: "ai", Write: true}
	clientDone := make(chan struct{})
	go func() {
		h.ServeClientConn(clientConn, claims)
		close(clientDone)
	}()

	clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1})

	// client receives attached
	attached := readUntilType(t, clientConn, "attached")
	if attached.Type != "attached" {
		t.Fatalf("expected attached, got %q", attached.Type)
	}
	_ = readUntilType(t, clientConn, "write_status")

	// agent receives attach
	readAgentAttachAndSync(t, agentConn, "ai", []string{"ai"})

	// request writer
	clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
	writeStatus := readUntilType(t, clientConn, "write_status")
	if writeStatus.Type != "write_status" || !writeStatus.Write {
		t.Fatalf("expected write_status true, got %+v", writeStatus)
	}

	// client -> agent binary (framed)
	clientConn.sendBinary([]byte("ls\n"))
	binToAgent := agentConn.readFrame(t)
	if binToAgent.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary to agent")
	}
	sid, payload, err := muxframe.Decode(binToAgent.data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sid != "ai" || string(payload) != "ls\n" {
		t.Fatalf("unexpected frame: sid=%q payload=%q", sid, string(payload))
	}

	// agent -> client binary (framed)
	frame, _ := muxframe.Encode("ai", []byte("out"))
	agentConn.sendBinary(frame)
	binToClient := readUntilBinary(t, clientConn)
	if binToClient.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary to client")
	}
	if string(binToClient.data) != "out" {
		t.Fatalf("unexpected binary to client: %q", string(binToClient.data))
	}

	clientConn.Close()
	agentConn.Close()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("client handler did not exit")
	}
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("agent handler did not exit")
	}
}

func TestHubSerializesAgentWrites(t *testing.T) {
	h := NewHub(NewTokenManager("secret"), map[string]string{})

	agentConn := newBlockingAgentConn()
	h.registerAgent("agent1", agentConn)

	clientConn := newFakeConn()
	claims := Claims{AgentID: "agent1", SessionID: "ai", Write: true}
	clientDone := make(chan struct{})
	go func() {
		h.ServeClientConn(clientConn, claims)
		close(clientDone)
	}()

	clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 2})
	_ = readUntilType(t, clientConn, "attached")

	var reqID string
	select {
	case reqID = <-agentConn.listStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for list_sessions")
	}

	clientConn.sendJSON(t, ControlMessage{Type: "resize", SessionID: "ai", Cols: 120, Rows: 32})

	time.Sleep(100 * time.Millisecond)
	close(agentConn.unblockList)
	h.deliverSessions(reqID, []string{"ai"})

	_ = clientConn.Close()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for client shutdown")
	}
}

func TestHubFocusGatesBinaryInput(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, map[string]string{"agent1": "agent-secret"})

	agentConn := newFakeConn()
	clientConn := newFakeConn()

	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")

	claims := Claims{AgentID: "agent1", SessionID: "ai", Write: true}
	clientDone := make(chan struct{})
	go func() {
		h.ServeClientConn(clientConn, claims)
		close(clientDone)
	}()

	clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1})
	_ = readUntilType(t, clientConn, "attached")
	_ = readUntilType(t, clientConn, "write_status")
	readAgentAttachAndSync(t, agentConn, "ai", []string{"ai"})

	clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
	writeStatus := readUntilType(t, clientConn, "write_status")
	if writeStatus.Type != "write_status" || !writeStatus.Write {
		t.Fatalf("expected write_status true, got %+v", writeStatus)
	}

	// focus off
	clientConn.sendJSON(t, ControlMessage{Type: "focus", State: "off"})
	clientConn.sendBinary([]byte("ls\n"))

	select {
	case <-agentConn.out:
		t.Fatalf("expected no binary forwarded when focus off")
	case <-time.After(200 * time.Millisecond):
		// ok
	}

	// focus on
	clientConn.sendJSON(t, ControlMessage{Type: "focus", State: "on"})
	clientConn.sendBinary([]byte("ls\n"))
	binToAgent := agentConn.readFrame(t)
	if binToAgent.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary to agent")
	}
	sid, payload, err := muxframe.Decode(binToAgent.data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sid != "ai" || string(payload) != "ls\n" {
		t.Fatalf("unexpected data: sid=%q payload=%q", sid, string(payload))
	}

	clientConn.Close()
	agentConn.Close()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("client handler did not exit")
	}
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("agent handler did not exit")
	}
}

func TestHubMultiplexRoutesFrames(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, map[string]string{"agent1": "agent-secret"})

	agentConn := newFakeConn()
	clientConn := newFakeConn()

	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")

	claims := Claims{AgentID: "agent1", Write: true}
	clientDone := make(chan struct{})
	go func() {
		h.ServeClientConn(clientConn, claims)
		close(clientDone)
	}()

	clientConn.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "s1", ProtocolVersion: 2})
	_ = readUntilType(t, clientConn, "attached")
	_ = readUntilType(t, clientConn, "write_status")
	readAgentAttachAndSync(t, agentConn, "s1", []string{"s1"})

	clientConn.sendJSON(t, ControlMessage{Type: "attach", SessionID: "s2"})
	readAgentAttachAndSync(t, agentConn, "s2", []string{"s1", "s2"})

	clientConn.sendJSON(t, ControlMessage{Type: "active", SessionID: "s2"})

	clientConn.sendJSON(t, ControlMessage{Type: "request_write"})
	writeStatus := readUntilType(t, clientConn, "write_status")
	if writeStatus.Type != "write_status" || !writeStatus.Write {
		t.Fatalf("expected write_status true, got %+v", writeStatus)
	}

	frame1, _ := muxframe.Encode("s1", []byte("out1"))
	agentConn.sendBinary(frame1)
	binToClient := readUntilBinary(t, clientConn)
	if binToClient.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary to client")
	}
	sid, payload, err := muxframe.Decode(binToClient.data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sid != "s1" || string(payload) != "out1" {
		t.Fatalf("unexpected frame: sid=%q payload=%q", sid, string(payload))
	}

	frameWrong, _ := muxframe.Encode("s1", []byte("ls\n"))
	clientConn.sendBinary(frameWrong)
	select {
	case <-agentConn.out:
		t.Fatalf("expected no binary forwarded for inactive session")
	case <-time.After(200 * time.Millisecond):
	}

	frame2, _ := muxframe.Encode("s2", []byte("pwd\n"))
	clientConn.sendBinary(frame2)
	binToAgent := agentConn.readFrame(t)
	if binToAgent.msgType != websocket.BinaryMessage {
		t.Fatalf("expected binary to agent")
	}
	sid, payload, err = muxframe.Decode(binToAgent.data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sid != "s2" || string(payload) != "pwd\n" {
		t.Fatalf("unexpected agent frame: sid=%q payload=%q", sid, string(payload))
	}

	clientConn.Close()
	agentConn.Close()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("client handler did not exit")
	}
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("agent handler did not exit")
	}
}

func TestAuthorizeClientTokenModeRequiresToken(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, nil)
	h.AuthMode = AuthModeToken

	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws/client", nil)
	req.RemoteAddr = "100.64.0.5:1234"
	if _, err := h.authorizeClient(req); err == nil {
		t.Fatalf("expected error without token in token mode")
	}
}

func TestAuthorizeClientTailnetModeAllowsNoToken(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, nil)
	h.AuthMode = AuthModeTailnet

	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws/client", nil)
	req.RemoteAddr = "100.64.0.5:1234"
	claims, err := h.authorizeClient(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.AgentID != "" || claims.SessionID != "" {
		t.Fatalf("expected empty claims in tailnet mode, got %+v", claims)
	}
}

func TestAuthorizeClientTailnetOnlyBlocksNonTailnet(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, nil)
	h.AuthMode = AuthModeTailnet
	h.TailnetOnly = true

	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws/client", nil)
	req.RemoteAddr = "203.0.113.10:2222"
	if _, err := h.authorizeClient(req); err == nil {
		t.Fatalf("expected error for non-tailnet ip")
	}
}

func TestHubMultipleClientsWriterGating(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, map[string]string{"agent1": "agent-secret"})

	agentConn := newFakeConn()
	client1 := newFakeConn()
	client2 := newFakeConn()

	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")

	claims := Claims{AgentID: "agent1", SessionID: "ai", Write: true}
	clientDone1 := make(chan struct{})
	go func() {
		h.ServeClientConn(client1, claims)
		close(clientDone1)
	}()
	clientDone2 := make(chan struct{})
	go func() {
		h.ServeClientConn(client2, claims)
		close(clientDone2)
	}()

	client1.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1})
	_ = readUntilType(t, client1, "attached")
	_ = readUntilType(t, client1, "write_status")
	readAgentAttachAndSync(t, agentConn, "ai", []string{"ai"})

	client2.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 1})
	_ = readUntilType(t, client2, "attached")
	_ = readUntilType(t, client2, "write_status")
	readAgentAttachAndSync(t, agentConn, "ai", []string{"ai"})

	client1.sendJSON(t, ControlMessage{Type: "request_write"})
	ws1 := readUntilType(t, client1, "write_status")
	if ws1.Type != "write_status" || !ws1.Write {
		t.Fatalf("expected client1 writer, got %+v", ws1)
	}
	ws2 := readUntilType(t, client2, "write_status")
	if ws2.Type != "write_status" || ws2.Write {
		t.Fatalf("expected client2 viewer, got %+v", ws2)
	}

	client2.sendBinary([]byte("ls\n"))
	select {
	case <-agentConn.out:
		t.Fatalf("unexpected input from viewer")
	case <-time.After(200 * time.Millisecond):
	}

	client1.sendBinary([]byte("ls\n"))
	_ = agentConn.readFrame(t)

	client1.sendJSON(t, ControlMessage{Type: "release_write"})
	ws1 = readUntilType(t, client1, "write_status")
	ws2 = readUntilType(t, client2, "write_status")
	if ws1.Write || ws2.Write {
		t.Fatalf("expected no writer after release")
	}

	client2.sendJSON(t, ControlMessage{Type: "request_write"})
	ws2 = readUntilType(t, client2, "write_status")
	if !ws2.Write {
		t.Fatalf("expected client2 writer, got %+v", ws2)
	}
	ws1 = readUntilType(t, client1, "write_status")
	if ws1.Write {
		t.Fatalf("expected client1 viewer, got %+v", ws1)
	}

	client1.Close()
	client2.Close()
	agentConn.Close()
	select {
	case <-clientDone1:
	case <-time.After(2 * time.Second):
		t.Fatalf("client1 handler did not exit")
	}
	select {
	case <-clientDone2:
	case <-time.After(2 * time.Second):
		t.Fatalf("client2 handler did not exit")
	}
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("agent handler did not exit")
	}
}

func TestHubBroadcastsSessionSync(t *testing.T) {
	mgr := NewTokenManager("secret")
	h := NewHub(mgr, map[string]string{"agent1": "agent-secret"})

	agentConn := newFakeConn()
	client1 := newFakeConn()
	client2 := newFakeConn()
	sessions := []string{"ai"}
	agentMsgs := make(chan ControlMessage, 16)
	stopAgent := make(chan struct{})

	agentDone := make(chan struct{})
	go func() {
		h.ServeAgentConn("agent1", agentConn)
		close(agentDone)
	}()
	waitForAgent(t, h, "agent1")
	go func() {
		for {
			select {
			case <-stopAgent:
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
					agentConn.sendJSON(t, ControlMessage{Type: "sessions", RequestID: msg.RequestID, Sessions: sessions})
					continue
				}
				agentMsgs <- msg
			}
		}
	}()

	claims := Claims{AgentID: "agent1", Write: true}
	clientDone1 := make(chan struct{})
	go func() {
		h.ServeClientConn(client1, claims)
		close(clientDone1)
	}()
	clientDone2 := make(chan struct{})
	go func() {
		h.ServeClientConn(client2, claims)
		close(clientDone2)
	}()

	client1.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 2})
	_ = readUntilType(t, client1, "attached")
	_ = readUntilType(t, client1, "write_status")
	attach := readFromChanType(t, agentMsgs, "attach")
	if attach.SessionID != "ai" {
		t.Fatalf("unexpected attach: %+v", attach)
	}
	sync1 := readUntilType(t, client1, "sessions_sync")
	if sync1.Type != "sessions_sync" || len(sync1.Sessions) != 1 || sync1.Sessions[0] != "ai" {
		t.Fatalf("unexpected sync1: %+v", sync1)
	}

	client2.sendJSON(t, ControlMessage{Type: "attach", AgentID: "agent1", SessionID: "ai", ProtocolVersion: 2})
	_ = readUntilType(t, client2, "attached")
	_ = readUntilType(t, client2, "write_status")
	attach = readFromChanType(t, agentMsgs, "attach")
	if attach.SessionID != "ai" {
		t.Fatalf("unexpected attach: %+v", attach)
	}
	_ = readUntilType(t, client2, "sessions_sync")
	_ = readUntilType(t, client1, "sessions_sync")

	// client1 attaches new session
	sessions = []string{"ai", "s2"}
	client1.sendJSON(t, ControlMessage{Type: "attach", SessionID: "s2"})
	attach = readFromChanType(t, agentMsgs, "attach")
	if attach.SessionID != "s2" {
		t.Fatalf("unexpected attach: %+v", attach)
	}
	sync2 := readUntilType(t, client2, "sessions_sync")
	if sync2.Type != "sessions_sync" || len(sync2.Sessions) != 2 {
		t.Fatalf("unexpected sync2: %+v", sync2)
	}
	_ = readUntilType(t, client1, "sessions_sync")

	// client1 detaches session, expect sync broadcast
	sessions = []string{"ai"}
	client1.sendJSON(t, ControlMessage{Type: "detach", SessionID: "s2"})
	detach := readFromChanType(t, agentMsgs, "detach")
	if detach.SessionID != "s2" {
		t.Fatalf("unexpected detach: %+v", detach)
	}
	sync3 := readUntilType(t, client2, "sessions_sync")
	if sync3.Type != "sessions_sync" || len(sync3.Sessions) != 1 || sync3.Sessions[0] != "ai" {
		t.Fatalf("unexpected sync3: %+v", sync3)
	}
	_ = readUntilType(t, client1, "sessions_sync")

	client1.Close()
	client2.Close()
	close(stopAgent)
	agentConn.Close()
	select {
	case <-clientDone1:
	case <-time.After(2 * time.Second):
		t.Fatalf("client1 handler did not exit")
	}
	select {
	case <-clientDone2:
	case <-time.After(2 * time.Second):
		t.Fatalf("client2 handler did not exit")
	}
	select {
	case <-agentDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("agent handler did not exit")
	}
}

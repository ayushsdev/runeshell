package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"runeshell/internal/muxframe"
)

type WSConn interface {
	ReadJSON(v any) error
	WriteJSON(v any) error
	ReadMessage() (int, []byte, error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type Hub struct {
	tokens       *TokenManager
	agentSecrets map[string]string
	AuthMode     string
	TailnetOnly  bool

	mu       sync.Mutex
	agents   map[string]*agentState
	reqID    uint64
	clientID uint64

	pendingMu       sync.Mutex
	pendingSessions map[string]chan []string

	upgrader websocket.Upgrader
	logger   *log.Logger
}

type agentState struct {
	id      string
	conn    WSConn
	clients map[string]*clientState
	writer  string
	write   bool
	sess    string
	writeMu sync.Mutex
}

func (a *agentState) writeJSON(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteJSON(v)
}

func (a *agentState) writeMessage(messageType int, data []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.conn.WriteMessage(messageType, data)
}

type clientState struct {
	id      string
	conn    WSConn
	proto   int
	session string
}

type tokenRequest struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Write     bool   `json:"write"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type lockRequest struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Writer    string `json:"writer"`
}

func NewHub(tokens *TokenManager, agentSecrets map[string]string) *Hub {
	if agentSecrets == nil {
		agentSecrets = map[string]string{}
	}
	return &Hub{
		tokens:          tokens,
		agentSecrets:    agentSecrets,
		AuthMode:        AuthModeToken,
		agents:          make(map[string]*agentState),
		pendingSessions: make(map[string]chan []string),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		logger: log.New(io.Discard, "", 0),
	}
}

func (h *Hub) SetLogger(logger *log.Logger) {
	if logger == nil {
		h.logger = log.New(io.Discard, "", 0)
		return
	}
	h.logger = logger
}

func (h *Hub) ServeClientWS(w http.ResponseWriter, r *http.Request) {
	claims, err := h.authorizeClient(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.ServeClientConn(conn, claims)
}

func (h *Hub) authorizeClient(r *http.Request) (Claims, error) {
	if h.TailnetOnly && !isTailnetRequest(r) {
		return Claims{}, ErrUnauthorized
	}
	if h.AuthMode == AuthModeTailnet {
		return Claims{}, nil
	}
	tok := r.URL.Query().Get("token")
	return h.tokens.Verify(tok)
}

func (h *Hub) ServeAgentWS(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	secret := r.URL.Query().Get("agent_secret")
	if !h.validateAgent(agentID, secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.ServeAgentConn(agentID, conn)
}

func (h *Hub) TokenHandler(adminToken string, ttlSeconds int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminToken == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+adminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req tokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.AgentID == "" || req.SessionID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		claims := Claims{AgentID: req.AgentID, SessionID: req.SessionID, Write: req.Write}
		token, err := h.tokens.Issue(claims, timeSeconds(ttlSeconds))
		if err != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{Token: token})
	}
}

func (h *Hub) LockHandler(adminToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminToken == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+adminToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req lockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.AgentID == "" || req.SessionID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writer := strings.ToLower(strings.TrimSpace(req.Writer))
		switch writer {
		case "", "web":
			if !h.setWebWritable(req.AgentID, req.SessionID, true) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		case "none":
			if !h.setWebWritable(req.AgentID, req.SessionID, false) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
		default:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func timeSeconds(s int) time.Duration {
	if s <= 0 {
		return 60 * time.Second
	}
	return time.Duration(s) * time.Second
}

func (h *Hub) ServeAgentConn(agentID string, conn WSConn) {
	h.registerAgent(agentID, conn)
	defer func() {
		h.unregisterAgent(agentID, conn)
		_ = conn.Close()
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			sid, payload, err := muxframe.Decode(data)
			if err != nil {
				continue
			}
			clients := h.clientSnapshot(agentID)
			if len(clients) == 0 {
				continue
			}
			for _, c := range clients {
				if c.proto < 2 {
					if c.session != "" && sid != c.session {
						continue
					}
					_ = c.conn.WriteMessage(websocket.BinaryMessage, payload)
					continue
				}
				_ = c.conn.WriteMessage(websocket.BinaryMessage, data)
			}
		case websocket.TextMessage:
			var msg ControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.Type == "sessions" && msg.RequestID != "" {
				h.deliverSessions(msg.RequestID, msg.Sessions)
			}
		default:
			// ignore
		}
	}
}

func (h *Hub) ServeClientConn(conn WSConn, claims Claims) {
	defer conn.Close()

	var attach ControlMessage
	if err := conn.ReadJSON(&attach); err != nil {
		return
	}
	if attach.Type != "attach" {
		sendError(conn, "bad_request", "first message must be attach")
		return
	}
	if attach.ProtocolVersion != 0 && attach.ProtocolVersion != 1 && attach.ProtocolVersion != 2 {
		sendError(conn, "bad_version", "unsupported protocol version")
		return
	}
	proto := attach.ProtocolVersion
	if proto == 0 {
		proto = 1
	}
	if claims.AgentID == "" && claims.SessionID == "" {
		if attach.AgentID == "" || attach.SessionID == "" {
			sendError(conn, "bad_request", "agent_id and session_id required")
			return
		}
		claims = Claims{AgentID: attach.AgentID, Write: true}
		if proto < 2 {
			claims.SessionID = attach.SessionID
		}
	}
	if attach.AgentID != "" && attach.AgentID != claims.AgentID {
		sendError(conn, "not_authorized", "agent mismatch")
		return
	}
	if proto < 2 {
		if attach.SessionID != claims.SessionID {
			sendError(conn, "not_authorized", "session mismatch")
			return
		}
	} else if claims.SessionID != "" && attach.SessionID != claims.SessionID {
		sendError(conn, "not_authorized", "session mismatch")
		return
	}

	agent := h.currentAgent(claims.AgentID)
	if agent == nil {
		sendError(conn, "agent_offline", "agent not connected")
		return
	}
	clientID := h.nextClientID()
	client := &clientState{id: clientID, conn: conn, proto: proto, session: claims.SessionID}
	if proto < 2 && client.session == "" {
		client.session = attach.SessionID
	}
	if !h.addClient(claims.AgentID, client) {
		sendError(conn, "agent_offline", "agent not connected")
		return
	}
	defer func() {
		wasWriter := h.removeClient(claims.AgentID, clientID)
		if wasWriter {
			h.broadcastWriteStatus(claims.AgentID)
		}
	}()
	h.setSession(claims.AgentID, claims.SessionID)

	firstSession := attach.SessionID
	if firstSession == "" {
		firstSession = claims.SessionID
	}
	_ = agent.writeJSON(ControlMessage{Type: "attach", SessionID: firstSession, ProtocolVersion: proto})
	if err := conn.WriteJSON(ControlMessage{Type: "attached", Write: claims.Write, Status: "ok"}); err != nil {
		return
	}
	h.sendWriteStatus(conn, h.isWriter(claims.AgentID, clientID))
	go h.syncSessions(claims.AgentID)

	focusActive := true
	activeSession := claims.SessionID
	if activeSession == "" {
		activeSession = firstSession
	}
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch msgType {
		case websocket.TextMessage:
			var msg ControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				sendError(conn, "bad_request", "invalid json")
				return
			}
			switch msg.Type {
			case "attach":
				if proto < 2 {
					break
				}
				if msg.SessionID == "" {
					sendError(conn, "bad_request", "session_id required")
					return
				}
				if claims.SessionID != "" && msg.SessionID != claims.SessionID {
					sendError(conn, "not_authorized", "session mismatch")
					return
				}
				_ = agent.writeJSON(ControlMessage{Type: "attach", SessionID: msg.SessionID, ProtocolVersion: proto})
				if activeSession == "" {
					activeSession = msg.SessionID
				}
				go h.syncSessions(claims.AgentID)
			case "request_write":
				if !claims.Write {
					_ = conn.WriteJSON(ControlMessage{Type: "write_denied", Code: "not_authorized", Message: "not authorized for write"})
					break
				}
				if !h.webWritable(claims.AgentID) {
					_ = conn.WriteJSON(ControlMessage{Type: "write_denied", Code: "locked", Message: "web input locked"})
					break
				}
				if h.hasWriter(claims.AgentID) && !h.isWriter(claims.AgentID, clientID) {
					_ = conn.WriteJSON(ControlMessage{Type: "write_denied", Code: "another_writer", Message: "another writer active"})
					break
				}
				h.setWriter(claims.AgentID, clientID)
				h.broadcastWriteStatus(claims.AgentID)
			case "release_write":
				if h.isWriter(claims.AgentID, clientID) {
					h.clearWriter(claims.AgentID)
					h.broadcastWriteStatus(claims.AgentID)
				}
			case "detach":
				if proto < 2 {
					break
				}
				if msg.SessionID == "" {
					sendError(conn, "bad_request", "session_id required")
					return
				}
				_ = agent.writeJSON(ControlMessage{Type: "detach", SessionID: msg.SessionID})
				go h.syncSessions(claims.AgentID)
			case "active":
				if proto < 2 {
					break
				}
				if msg.SessionID == "" {
					sendError(conn, "bad_request", "session_id required")
					return
				}
				activeSession = msg.SessionID
			case "resize":
				if proto >= 2 && msg.SessionID == "" {
					msg.SessionID = activeSession
				}
				_ = agent.writeJSON(ControlMessage{Type: "resize", SessionID: msg.SessionID, Cols: msg.Cols, Rows: msg.Rows})
			case "focus":
				if msg.State == "on" {
					focusActive = true
				} else if msg.State == "off" {
					focusActive = false
				}
			case "heartbeat":
				// no-op
			default:
				// ignore
			}
		case websocket.BinaryMessage:
			if !claims.Write {
				continue
			}
			if !focusActive || !h.webWritable(claims.AgentID) {
				continue
			}
			if !h.isWriter(claims.AgentID, clientID) {
				continue
			}
			if proto < 2 {
				frame, err := muxframe.Encode(claims.SessionID, data)
				if err != nil {
					continue
				}
				_ = agent.writeMessage(websocket.BinaryMessage, frame)
				continue
			}
			sid, payload, err := muxframe.Decode(data)
			if err != nil {
				continue
			}
			if sid != activeSession {
				continue
			}
			frame, err := muxframe.Encode(sid, payload)
			if err != nil {
				continue
			}
			_ = agent.writeMessage(websocket.BinaryMessage, frame)
		default:
			// ignore
		}
	}
}

func (h *Hub) validateAgent(agentID, secret string) bool {
	if agentID == "" {
		return false
	}
	allowed, ok := h.agentSecrets[agentID]
	return ok && allowed == secret
}

func (h *Hub) registerAgent(agentID string, conn WSConn) *agentState {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := &agentState{id: agentID, conn: conn, write: true, clients: make(map[string]*clientState)}
	h.agents[agentID] = state
	return state
}

func (h *Hub) unregisterAgent(agentID string, conn WSConn) {
	h.mu.Lock()
	state, ok := h.agents[agentID]
	if !ok || state.conn != conn {
		h.mu.Unlock()
		return
	}
	for _, c := range state.clients {
		_ = c.conn.Close()
	}
	delete(h.agents, agentID)
	h.mu.Unlock()
}

func (h *Hub) currentAgent(agentID string) *agentState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[agentID]
}

func (h *Hub) addClient(agentID string, client *clientState) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	if state.clients == nil {
		state.clients = make(map[string]*clientState)
	}
	state.clients[client.id] = client
	return true
}

func (h *Hub) removeClient(agentID, clientID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	delete(state.clients, clientID)
	if state.writer == clientID {
		state.writer = ""
		return true
	}
	return false
}

type clientSnapshot struct {
	id      string
	conn    WSConn
	proto   int
	session string
}

func (h *Hub) clientSnapshot(agentID string) []clientSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil || len(state.clients) == 0 {
		return nil
	}
	out := make([]clientSnapshot, 0, len(state.clients))
	for _, c := range state.clients {
		out = append(out, clientSnapshot{
			id:      c.id,
			conn:    c.conn,
			proto:   c.proto,
			session: c.session,
		})
	}
	return out
}

func (h *Hub) setSession(agentID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return
	}
	state.sess = sessionID
}

func (h *Hub) nextClientID() string {
	return fmt.Sprintf("c-%d", atomic.AddUint64(&h.clientID, 1))
}

func (h *Hub) isWriter(agentID, clientID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	return state.writer == clientID && clientID != ""
}

func (h *Hub) hasWriter(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	return state.writer != ""
}

func (h *Hub) setWriter(agentID, clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return
	}
	state.writer = clientID
}

func (h *Hub) clearWriter(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return
	}
	state.writer = ""
}

func (h *Hub) sendWriteStatus(conn WSConn, write bool) {
	_ = conn.WriteJSON(ControlMessage{Type: "write_status", Write: write})
}

func (h *Hub) broadcastWriteStatus(agentID string) {
	clients := h.clientSnapshot(agentID)
	if len(clients) == 0 {
		return
	}
	writer := ""
	h.mu.Lock()
	state := h.agents[agentID]
	if state != nil {
		writer = state.writer
	}
	h.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.WriteJSON(ControlMessage{Type: "write_status", Write: c.id == writer})
	}
}

func (h *Hub) syncSessions(agentID string) {
	sessions, err := h.requestSessions(agentID)
	if err != nil {
		return
	}
	h.broadcastSessions(agentID, sessions)
}

func (h *Hub) broadcastSessions(agentID string, sessions []string) {
	clients := h.clientSnapshot(agentID)
	if len(clients) == 0 {
		return
	}
	msg := ControlMessage{Type: "sessions_sync", Sessions: sessions}
	for _, c := range clients {
		_ = c.conn.WriteJSON(msg)
	}
}

func (h *Hub) webWritable(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	return state.write
}

func (h *Hub) setWebWritable(agentID, sessionID string, enabled bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.agents[agentID]
	if state == nil {
		return false
	}
	if state.sess != "" && state.sess != sessionID {
		return false
	}
	state.write = enabled
	return true
}

func (h *Hub) SessionsHandler(adminToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.AuthMode == AuthModeToken {
			if adminToken == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+adminToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if h.TailnetOnly && !isTailnetRequest(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		agentID := r.URL.Query().Get("agent_id")
		if agentID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sessions, err := h.requestSessions(agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"sessions": sessions})
	}
}

func (h *Hub) requestSessions(agentID string) ([]string, error) {
	agent := h.currentAgent(agentID)
	if agent == nil {
		return nil, errors.New("agent not connected")
	}
	reqID := fmt.Sprintf("req-%d", atomic.AddUint64(&h.reqID, 1))
	ch := make(chan []string, 1)
	h.pendingMu.Lock()
	h.pendingSessions[reqID] = ch
	h.pendingMu.Unlock()
	defer func() {
		h.pendingMu.Lock()
		delete(h.pendingSessions, reqID)
		h.pendingMu.Unlock()
	}()

	_ = agent.writeJSON(ControlMessage{Type: "list_sessions", RequestID: reqID})
	select {
	case sessions := <-ch:
		return sessions, nil
	case <-time.After(2 * time.Second):
		return nil, errors.New("timeout waiting for agent")
	}
}

func (h *Hub) deliverSessions(requestID string, sessions []string) {
	h.pendingMu.Lock()
	ch := h.pendingSessions[requestID]
	h.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- sessions:
	default:
	}
}

func sendError(conn WSConn, code, message string) {
	_ = conn.WriteJSON(ControlMessage{Type: "error", Code: code, Message: message})
}

const (
	AuthModeToken   = "token"
	AuthModeTailnet = "tailnet"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
)

func isTailnetRequest(r *http.Request) bool {
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	_, cidr, _ := net.ParseCIDR("100.64.0.0/10")
	return cidr.Contains(ip)
}

func remoteIP(r *http.Request) net.IP {
	addr := strings.TrimSpace(r.RemoteAddr)
	if addr == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(addr)
}

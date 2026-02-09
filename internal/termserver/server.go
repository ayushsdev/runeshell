package termserver

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type Session interface {
	Write(p []byte) error
	Resize(cols, rows int) error
	Output() <-chan []byte
	Close() error
}

type SessionManager interface {
	Attach(sessionID string) (Session, error)
}

type WSConn interface {
	ReadJSON(v any) error
	WriteJSON(v any) error
	ReadMessage() (int, []byte, error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type AuthFunc func(r *http.Request) error

type Server struct {
	manager  SessionManager
	upgrader websocket.Upgrader
	logger   *log.Logger
	Authorizer AuthFunc
}

func NewServer(manager SessionManager, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		manager: manager,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		logger: logger,
	}
}

func (s *Server) ServeWS(w http.ResponseWriter, r *http.Request) {
	if s.Authorizer != nil {
		if err := s.Authorizer(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.ServeConn(conn)
}

func (s *Server) ServeConn(conn WSConn) {
	defer conn.Close()

	var attachMsg ControlMessage
	if err := conn.ReadJSON(&attachMsg); err != nil {
		return
	}
	if attachMsg.Type != "attach" {
		sendError(conn, "bad_request", "first message must be attach")
		return
	}
	if attachMsg.ProtocolVersion != 0 && attachMsg.ProtocolVersion != 1 {
		sendError(conn, "bad_version", "unsupported protocol version")
		return
	}

	session, err := s.manager.Attach(attachMsg.SessionID)
	if err != nil {
		sendError(conn, "attach_failed", err.Error())
		return
	}
	defer session.Close()

	if err := conn.WriteJSON(ControlMessage{Type: "attached", Write: true, Status: "ok"}); err != nil {
		return
	}

	var once sync.Once
	closeConn := func() {
		once.Do(func() {
			_ = conn.Close()
		})
	}

	go func() {
		for data := range session.Output() {
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				closeConn()
				return
			}
		}
		closeConn()
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch msgType {
		case websocket.TextMessage:
			var msg ControlMessage
			if err := decodeJSON(data, &msg); err != nil {
				sendError(conn, "bad_request", "invalid json")
				return
			}
			switch msg.Type {
			case "resize":
				_ = session.Resize(msg.Cols, msg.Rows)
			case "heartbeat":
				// no-op
			default:
				// ignore unknown control messages in v1
			}
		case websocket.BinaryMessage:
			if len(data) == 0 {
				continue
			}
			_ = session.Write(data)
		default:
			// ignore
		}
	}
}

func sendError(conn WSConn, code, message string) {
	_ = conn.WriteJSON(ControlMessage{Type: "error", Code: code, Message: message})
}

func decodeJSON(data []byte, v any) error {
	if len(data) == 0 {
		return errors.New("empty")
	}
	return json.Unmarshal(data, v)
}

func TokenAuthorizer(token string) AuthFunc {
	return func(r *http.Request) error {
		if token == "" {
			return errors.New("token required")
		}
		if r.URL.Query().Get("token") != token {
			return errors.New("invalid token")
		}
		return nil
	}
}

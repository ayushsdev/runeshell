package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"sync"
	"time"

	"runeshell/internal/hub"
	"runeshell/internal/muxframe"
	"runeshell/internal/termserver"

	"github.com/gorilla/websocket"
)

type Client struct {
	HubURL  string
	AgentID string
	Secret  string
	Manager termserver.SessionManager
	Logger  *log.Logger
	writeMu sync.Mutex
}

func (c *Client) Run(ctx context.Context) error {
	if c.Manager == nil {
		return errors.New("manager required")
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}

	u, err := url.Parse(c.HubURL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("agent_id", c.AgentID)
	q.Set("agent_secret", c.Secret)
	u.RawQuery = q.Encode()

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	type agentSession struct {
		id   string
		sess termserver.Session
		done chan struct{}
	}
	sessions := make(map[string]*agentSession)
	var sessMu sync.Mutex

	closeAll := func() {
		sessMu.Lock()
		defer sessMu.Unlock()
		for id, s := range sessions {
			close(s.done)
			_ = s.sess.Close()
			delete(sessions, id)
		}
	}

	closeSession := func(id string) {
		sessMu.Lock()
		defer sessMu.Unlock()
		s := sessions[id]
		if s == nil {
			return
		}
		close(s.done)
		_ = s.sess.Close()
		delete(sessions, id)
	}

	attachSession := func(id string) error {
		sessMu.Lock()
		if _, ok := sessions[id]; ok {
			sessMu.Unlock()
			return nil
		}
		sessMu.Unlock()
		s, err := c.Manager.Attach(id)
		if err != nil {
			return err
		}
		as := &agentSession{id: id, sess: s, done: make(chan struct{})}
		sessMu.Lock()
		sessions[id] = as
		sessMu.Unlock()
		go func() {
			for {
				select {
				case data, ok := <-s.Output():
					if !ok {
						return
					}
					frame, err := muxframe.Encode(id, data)
					if err != nil {
						continue
					}
					c.writeMu.Lock()
					_ = conn.WriteMessage(websocket.BinaryMessage, frame)
					c.writeMu.Unlock()
				case <-as.done:
					return
				}
			}
		}()
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			closeAll()
			return ctx.Err()
		default:
		}

		msgType, data, err := conn.ReadMessage()
		if err != nil {
			closeAll()
			return err
		}
		switch msgType {
		case websocket.TextMessage:
			var msg hub.ControlMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "attach":
				if msg.SessionID == "" {
					continue
				}
				if err := attachSession(msg.SessionID); err != nil {
					c.writeMu.Lock()
					_ = conn.WriteJSON(hub.ControlMessage{Type: "error", Code: "attach_failed", Message: err.Error()})
					c.writeMu.Unlock()
				}
			case "detach":
				if msg.SessionID != "" {
					closeSession(msg.SessionID)
				}
			case "resize":
				if msg.SessionID == "" {
					continue
				}
				sessMu.Lock()
				s := sessions[msg.SessionID]
				sessMu.Unlock()
				if s != nil {
					_ = s.sess.Resize(msg.Cols, msg.Rows)
				}
			case "list_sessions":
				list := []string{}
				if lister, ok := c.Manager.(termserver.SessionLister); ok {
					if out, err := lister.ListSessions(); err == nil {
						list = out
					}
				}
				c.writeMu.Lock()
				_ = conn.WriteJSON(hub.ControlMessage{Type: "sessions", RequestID: msg.RequestID, Sessions: list})
				c.writeMu.Unlock()
			case "heartbeat":
				// no-op
			default:
				// ignore
			}
		case websocket.BinaryMessage:
			sid, payload, err := muxframe.Decode(data)
			if err != nil {
				continue
			}
			sessMu.Lock()
			s := sessions[sid]
			sessMu.Unlock()
			if s != nil {
				_ = s.sess.Write(payload)
			}
		default:
			// ignore
		}
	}
}

func RunWithRetry(ctx context.Context, client *Client, retry time.Duration) error {
	if retry <= 0 {
		retry = 2 * time.Second
	}
	for {
		if err := client.Run(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			client.Logger.Printf("agent disconnected: %v", err)
			select {
			case <-time.After(retry):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

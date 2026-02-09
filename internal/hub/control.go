package hub

type ControlMessage struct {
	Type            string   `json:"type"`
	AgentID         string   `json:"agent_id,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	RequestID       string   `json:"request_id,omitempty"`
	Cols            int      `json:"cols,omitempty"`
	Rows            int      `json:"rows,omitempty"`
	Write           bool     `json:"write,omitempty"`
	ProtocolVersion int      `json:"protocol_version,omitempty"`
	State           string   `json:"state,omitempty"`
	Sessions        []string `json:"sessions,omitempty"`
	Status          string   `json:"status,omitempty"`
	Code            string   `json:"code,omitempty"`
	Message         string   `json:"message,omitempty"`
}

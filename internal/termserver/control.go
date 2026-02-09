package termserver

type ControlMessage struct {
	Type            string `json:"type"`
	SessionID       string `json:"session_id,omitempty"`
	Cols            int    `json:"cols,omitempty"`
	Rows            int    `json:"rows,omitempty"`
	Write           bool   `json:"write,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	Status          string `json:"status,omitempty"`
	Code            string `json:"code,omitempty"`
	Message         string `json:"message,omitempty"`
}

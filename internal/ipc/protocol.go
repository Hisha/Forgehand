package ipc

type Request struct {
	Command string `json:"command"`
	Title   string `json:"title,omitempty"`
}

type Session struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type Response struct {
	OK       bool      `json:"ok"`
	Message  string    `json:"message,omitempty"`
	Version  string    `json:"version,omitempty"`
	PID      int       `json:"pid,omitempty"`
	Session  *Session  `json:"session,omitempty"`
	Sessions []Session `json:"sessions,omitempty"`
}

package ipc

type Request struct {
	Command string `json:"command"`
}

type Response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

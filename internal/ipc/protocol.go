package ipc

type Request struct {
	Command   string `json:"command"`
	Title     string `json:"title,omitempty"`
	SessionID int64  `json:"session_id,omitempty"`
}

type Session struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type Execution struct {
	ID          int64  `json:"id"`
	SessionID   int64  `json:"session_id"`
	Status      string `json:"status"`
	CurrentStep int    `json:"current_step"`
	TotalSteps  int    `json:"total_steps"`
}

type Response struct {
	OK        bool       `json:"ok"`
	Message   string     `json:"message,omitempty"`
	Version   string     `json:"version,omitempty"`
	PID       int        `json:"pid,omitempty"`
	Session   *Session   `json:"session,omitempty"`
	Sessions  []Session  `json:"sessions,omitempty"`
	Execution *Execution `json:"execution,omitempty"`
}

package ipc

import (
	"encoding/json"
	"testing"
)

func TestWireMessagesCarryProtocolVersion(t *testing.T) {
	for name, value := range map[string]any{
		"request":  Request{Command: "status"},
		"response": Response{OK: true},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := int(wire["protocol_version"].(float64)); got != ProtocolVersion {
			t.Fatalf("%s protocol = %d", name, got)
		}
		if name == "request" {
			if wire["client_version"] == "" || wire["client_build_identity"] == "" {
				t.Fatalf("request lacks client build identity: %s", data)
			}
		}
	}
}

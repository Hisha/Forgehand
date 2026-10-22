package ipc

import (
	"encoding/json"
	"strings"
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

func TestAccountFieldsRoundTrip(t *testing.T) {
	request := Request{
		Command:     "account-bootstrap-admin",
		Username:    "alice",
		Password:    "correct horse battery staple",
		TOTPCode:    "123456",
		ProjectID:   7,
		DisplayName: "Alice Example",
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded.Username != "alice" || decoded.Password != "correct horse battery staple" {
		t.Fatalf("decoded request = %+v", decoded)
	}
	if decoded.TOTPCode != "123456" || decoded.ProjectID != 7 {
		t.Fatalf("decoded request = %+v", decoded)
	}
}

func TestAccountResponseCarriesNoSecretMaterial(t *testing.T) {
	response := Response{
		OK:      true,
		Account: &Account{ID: 1, Username: "alice", Role: "administrator", Status: "pending"},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	encoded := string(data)
	for _, forbidden := range []string{"password_hash", "totp_secret", "password"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("the account response contains %q: %s", forbidden, encoded)
		}
	}
}

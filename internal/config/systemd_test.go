package config

import (
	"os"
	"strings"
	"testing"
)

func TestSystemdTemplateServiceContract(t *testing.T) {
	content, err := os.ReadFile("../../packaging/systemd/forgehand@.service")
	if err != nil {
		t.Fatalf("read systemd service template: %v", err)
	}
	text := string(content)
	for _, required := range []string{
		"User=%i",
		"Group=forgehand",
		"StateDirectory=forgehand",
		"RuntimeDirectory=forgehand",
		"RuntimeDirectoryMode=0770",
		"FORGEHAND_SOCKET_MODE=0660",
		"Restart=on-failure",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=\n",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("systemd template missing %q", required)
		}
	}
	for _, forbidden := range []string{"User=root", "smithkt", "network-online.target"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("systemd template contains forbidden value %q", forbidden)
		}
	}
}

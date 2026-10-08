package daemon

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func validateDaemonIdentity(realUID, effectiveUID int, effectiveCapabilities uint64) error {
	if realUID == 0 || effectiveUID == 0 {
		return fmt.Errorf("Forgehand daemon refuses to run as root (real UID %d, effective UID %d)", realUID, effectiveUID)
	}
	if effectiveCapabilities != 0 {
		return fmt.Errorf("Forgehand daemon refuses unexpected effective Linux capabilities (CapEff=%x)", effectiveCapabilities)
	}
	return nil
}

func effectiveCapabilities() (uint64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, fmt.Errorf("inspect effective Linux capabilities: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		if found && name == "CapEff" {
			capabilities, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil {
				return 0, fmt.Errorf("parse effective Linux capabilities: %w", err)
			}
			return capabilities, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read effective Linux capabilities: %w", err)
	}
	return 0, fmt.Errorf("effective Linux capabilities are unavailable")
}

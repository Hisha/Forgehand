package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// readSecret reads a secret from the controlling terminal with echo disabled.
//
// The secret is never echoed, never written to a log, and never passed on a
// command line, so it cannot leak through the process table or the shell
// history.
func readSecret(prompt string) (string, error) {
	file := os.Stdin
	fd := int(file.Fd())

	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", fmt.Errorf("inspect terminal state: %w", err)
	}

	original := *termios
	modified := *termios
	modified.Lflag &^= unix.ECHO
	modified.Lflag |= unix.ICANON | unix.ISIG
	modified.Iflag |= unix.ICRNL

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &modified); err != nil {
		return "", fmt.Errorf("disable terminal echo: %w", err)
	}
	defer func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &original)
	}()

	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(file)
	line, readErr := reader.ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if readErr != nil {
		return "", fmt.Errorf("read secret: %w", readErr)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readPassword prompts for a new password and asks for it twice.
func readPassword(prompt string) (string, error) {
	first, err := readSecret(prompt)
	if err != nil {
		return "", err
	}
	second, err := readSecret("Confirm password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("the passwords did not match")
	}
	return first, nil
}

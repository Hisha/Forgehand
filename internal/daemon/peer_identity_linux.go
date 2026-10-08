package daemon

import (
	"fmt"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

type ClientIdentity struct {
	Transport string
	PID       int32
	UID       uint32
	GID       uint32
}

type syscallConnection interface {
	SyscallConn() (syscall.RawConn, error)
}

func captureClientIdentity(conn net.Conn) (ClientIdentity, error) {
	syscallConn, ok := conn.(syscallConnection)
	if !ok {
		return ClientIdentity{}, fmt.Errorf("connection does not expose Unix peer credentials")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return ClientIdentity{}, fmt.Errorf("access Unix connection: %w", err)
	}

	var credentials *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return ClientIdentity{}, fmt.Errorf("inspect Unix peer credentials: %w", err)
	}
	if credentialErr != nil {
		return ClientIdentity{}, fmt.Errorf("inspect Unix peer credentials: %w", credentialErr)
	}

	return ClientIdentity{
		Transport: "unix-peer-credentials",
		PID:       credentials.Pid,
		UID:       credentials.Uid,
		GID:       credentials.Gid,
	}, nil
}

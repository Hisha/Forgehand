package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPeerCredentialCaptureIgnoresClaimedIdentity(t *testing.T) {
	socketPath := filepath.Join(shortSocketTestDir(t), "peer.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	result := make(chan ClientIdentity, 1)
	errors := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errors <- err
			return
		}
		defer conn.Close()
		identity, err := captureClientIdentity(conn)
		if err != nil {
			errors <- err
			return
		}
		result <- identity
	}()

	client, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := client.Write([]byte(`{"command":"status","uid":0,"gid":0,"pid":1}` + "\n")); err != nil {
		t.Fatalf("write untrusted identity fields: %v", err)
	}
	client.Close()

	select {
	case err := <-errors:
		t.Fatalf("capture peer credentials: %v", err)
	case identity := <-result:
		if identity.UID != uint32(os.Getuid()) || identity.GID != uint32(os.Getgid()) || identity.PID != int32(os.Getpid()) {
			t.Fatalf("peer identity = %#v, want pid=%d uid=%d gid=%d", identity, os.Getpid(), os.Getuid(), os.Getgid())
		}
		if identity.Transport != "unix-peer-credentials" {
			t.Fatalf("transport = %q", identity.Transport)
		}
	}
}

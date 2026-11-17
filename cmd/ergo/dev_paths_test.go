//go:build linux

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDevSocketUnderDeepRoot is the repro of the bind failure under a
// deep cache root (I19 full r4: TestDevAnswersAWarmCheck under
// TestPackageNeverWritesUnderHome's nested temporary directories, "bind:
// invalid argument", and the test hung): a daemon listens, and a client
// connects, on a socket whose path is longer than a unix socket's
// sun_path, and the socket is a file at that path, under the root.
//
// Mutant: dev-socket-path-unshortened (viaShortPath always uses the path
// as it is).
func TestDevSocketUnderDeepRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("deep/", 24))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "0123456789abcdef.sock")
	if len(socket) <= 108 {
		t.Fatalf("socket path %s is %d bytes, not past sun_path", socket, len(socket))
	}
	listener, err := devListen(socket)
	if err != nil {
		t.Fatalf("listen on a %d-byte path: %v", len(socket), err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "served")
	}()
	conn, err := devDial(socket)
	if err != nil {
		t.Fatalf("dial a %d-byte path: %v", len(socket), err)
	}
	defer conn.Close()
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "served" {
		t.Fatalf("read %q, %v; want %q", got, err, "served")
	}
	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no socket at %s: %v", socket, err)
	}
}

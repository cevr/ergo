package main

import (
	"strings"
	"testing"
)

// TestDevSocketFitsUnderDeepRoot is the repro of the bind failure under a
// deep cache root (I19 full r4: TestDevAnswersAWarmCheck under
// TestPackageNeverWritesUnderHome's nested temp directories, "bind:
// invalid argument"): every socket path devPaths gives fits a unix
// socket's sun_path, two cache roots get two sockets, and the lock stays
// under the root.
//
// Mutant: dev-socket-under-root (the socket always under the root).
func TestDevSocketFitsUnderDeepRoot(t *testing.T) {
	short := "/tmp/x"
	deep := "/tmp/deep" + strings.Repeat("/deep", 30)
	for _, root := range []string{short, deep, deep + "/other"} {
		socket, lock := devPaths(root, "release", "/work/space")
		if len(socket) > devSocketMax {
			t.Errorf("root %s: socket %s is %d bytes, more than %d", root, socket, len(socket), devSocketMax)
		}
		if !strings.HasPrefix(lock, root+"/") {
			t.Errorf("root %s: lock %s is not under the root", root, lock)
		}
	}
	if s, _ := devPaths(short, "release", "/work/space"); !strings.HasPrefix(s, short+"/") {
		t.Errorf("a short root's socket %s is not under it", s)
	}
	a, _ := devPaths(deep, "release", "/work/space")
	b, _ := devPaths(deep+"/other", "release", "/work/space")
	if a == b {
		t.Errorf("two deep roots share the socket %s", a)
	}
}

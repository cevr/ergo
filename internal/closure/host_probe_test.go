package closure

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The round-4 verifier's host-service probe
// (round-4-receipts/host-service-probe.patch), kept as a regression test. A
// test reads docs/tooling.md through testdata/away, then asks a service
// outside the sandbox (a stand-in for a host service such as the systemd
// user manager) to point away at the package's own testdata, and waits for
// its answer before it exits. Resolved after the run, the path names the
// package's testdata. It uses only what the round-4 head had, so it runs
// there too, where it fails: the sweep accepts the final spelling.

// hostServiceVariable holds the service's socket for TestHostServiceTarget.
const hostServiceVariable = "ERGO_CLOSURE_HOST_SERVICE"

// TestHostServiceTarget is the probe's test: it runs only as the target of
// TestHostServiceChangesFailClosed.
func TestHostServiceTarget(t *testing.T) {
	sock := os.Getenv(hostServiceVariable)
	if sock == "" {
		t.Skip("the target of TestHostServiceChangesFailClosed")
	}
	b, err := os.ReadFile("testdata/away/tooling.md")
	if err != nil || !strings.Contains(string(b), "test package") {
		t.Fatalf("read docs/tooling.md through testdata/away: %q %v", b, err)
	}
	// No clock bounds the exchange: the service closes the connection when
	// it fails, and the run deadline bounds the rest (needSandbox).
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("retarget owned fixture")); err != nil {
		t.Fatal(err)
	}
	var ack [2]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil || string(ack[:]) != "ok" {
		t.Fatalf("the service's answer %q: %v", ack, err)
	}
}

// TestHostServiceChangesFailClosed: the change the service made at the
// test's request is a closure violation of the package, and the entry names
// the object the test read, docs/tooling.md.
func TestHostServiceChangesFailClosed(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	sock := filepath.Join(t.TempDir(), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// The test asks the service and waits for its answer before it exits,
	// so the service accepts the test's connection before the run ends; a
	// run that failed before it asked ends the wait with the listener's
	// close.
	done := make(chan error, 1)
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		var req [22]byte
		if _, err = io.ReadFull(c, req[:]); err == nil {
			next := filepath.Join(f.pkg, "testdata/next")
			if err = os.Symlink("in", next); err == nil {
				err = os.Rename(next, filepath.Join(f.pkg, "testdata/away"))
			}
		}
		if err == nil {
			_, err = c.Write([]byte("ok"))
		}
		done <- err
	}()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(t.TempDir(), "internal_p")
	es, runErr := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{hostServiceVariable + "=" + sock}, self, "-test.run=^TestHostServiceTarget$")
	l.Close()
	serviceErr := <-done
	target, _ := os.Readlink(filepath.Join(f.pkg, "testdata/away"))
	if serviceErr != nil || target != "in" {
		t.Fatalf("the service did not point away at in: %v, %q (the run: %v)", serviceErr, target, runErr)
	}
	if runErr != nil {
		t.Fatalf("want the run classified, with the change a closure violation; got %v", runErr)
	}
	outside := newClosure("internal/p", nil, nil).check(es, map[string]bool{})
	raw, _ := os.ReadFile(stem + ".log")
	t.Logf("log %q\nentries %v; outside the closure %v", raw, es, outside)
	// The read of docs/tooling.md, named by its access-time object, and the
	// link the service changed: both fail closed.
	for _, want := range []entry{{"changed during open", "docs/tooling.md"}, {"changed during link", "internal/p/testdata/away"}} {
		if !slices.Contains(outside, want) {
			t.Errorf("the service changed what the test read, and the sweep has no %v outside the closure: %v", want, outside)
		}
	}
}

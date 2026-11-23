//go:build linux && (amd64 || arm64)

package testinputs

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestExecRunsTargetObserved needs a real process: only the kernel can
// show that the shell and the command it starts inherit the filter.
func TestExecRunsTargetObserved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input")
	const contents = "the target read this\n"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := Exec(ExecSpec{
		Argv: []string{"sh", "-c", `cat "$1"`, "sh", file},
		Env:  []string{"PATH=/usr/bin:/bin"},
		Dir:  dir, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil || result.Exit != 0 || result.Reason != "" {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; stderr: %s", result.Exit, result.Reason, err, &stderr)
	}
	if got := stdout.String(); got != contents {
		t.Fatalf("target stdout = %q, want %q", got, contents)
	}
	if !slices.ContainsFunc(result.Inputs, func(in Input) bool { return in.Path == file && in.Kind == SnapRead }) {
		t.Fatalf("target read of %s missing from %v", file, result.Inputs)
	}
}

// TestExecRefusesAnOutsidePeer requires an actual kernel notification.
// A loopback address does not make the peer part of the command tree.
func TestExecRefusesAnOutsidePeer(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Exec(ExecSpec{
		Argv:   []string{executable, "-test.run=^TestExecPeerProbe$"},
		Env:    []string{"POLY_MEMO_TEST_PEER=" + listener.Addr().String()},
		Stderr: os.Stderr,
	})
	if err != nil || result.Exit != 0 || !strings.Contains(result.Reason, "used the network") {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; want outside-peer refusal", result.Exit, result.Reason, err)
	}
}

// TestExecAllowsItsOwnLoopbackPeer proves an ephemeral port really bound
// by the command tree is different from an unobserved external service.
func TestExecAllowsItsOwnLoopbackPeer(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Exec(ExecSpec{
		Argv:   []string{executable, "-test.run=^TestExecPeerProbe$"},
		Env:    []string{"POLY_MEMO_TEST_PEER=self"},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil || result.Exit != 0 || result.Reason != "" {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; want a complete loopback run", result.Exit, result.Reason, err)
	}
}

// TestExecPeerProbe is the command that TestExecRefusesAnOutsidePeer runs
// in a separate process. It needs no test service outside that fixture.
func TestExecPeerProbe(t *testing.T) {
	if address := os.Getenv("POLY_MEMO_TEST_PEER"); address != "" {
		if address == "self" {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			address = listener.Addr().String()
		} else if listener, err := net.Listen("tcp4", address); err == nil {
			listener.Close()
			t.Fatal("the external address unexpectedly allowed a second bind")
		}
		conn, err := net.Dial("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
}

// TestExecCapturesPreWriteContent needs the real open notifications: they
// distinguish append from truncate before either one changes the file.
func TestExecCapturesPreWriteContent(t *testing.T) {
	dir := t.TempDir()
	appendTo, overwrite := filepath.Join(dir, "append"), filepath.Join(dir, "overwrite")
	for _, file := range []string{appendTo, overwrite} {
		if err := os.WriteFile(file, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Exec(ExecSpec{
		Argv: []string{"sh", "-c", `printf added >> append; printf new > overwrite; cat overwrite`, "sh"},
		Env:  []string{"PATH=/usr/bin:/bin"},
		Dir:  dir,
		Snap: func(path string, kind SnapKind) string {
			if kind == SnapRead && (path == appendTo || path == overwrite) {
				b, _ := os.ReadFile(path)
				return string(b)
			}
			return ""
		},
	})
	if err != nil || result.Exit != 0 || result.Reason != "" {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v", result.Exit, result.Reason, err)
	}
	if !slices.Contains(result.Inputs, Input{Path: appendTo, Kind: SnapRead, State: "old"}) {
		t.Fatalf("append did not capture old content: %v", result.Inputs)
	}
	if slices.ContainsFunc(result.Inputs, func(in Input) bool { return in.Path == overwrite && in.Kind == SnapRead }) {
		t.Fatalf("read after truncate was keyed to the old input: %v", result.Inputs)
	}
	if !slices.Contains(result.Written, appendTo) || !slices.Contains(result.Written, overwrite) {
		t.Fatalf("written paths missing: %v", result.Written)
	}
}

// TestExecRefusesAChangedRead edits behind the command only after a later
// syscall notification; the edit is ordered by events, not by time.
func TestExecRefusesAChangedRead(t *testing.T) {
	dir := t.TempDir()
	input, trigger := filepath.Join(dir, "input"), filepath.Join(dir, "trigger")
	if err := os.WriteFile(input, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var editErr error
	result, err := Exec(ExecSpec{
		Argv: []string{"sh", "-c", `cat input && printf trigger > trigger`, "sh"},
		Env:  []string{"PATH=/usr/bin:/bin"},
		Dir:  dir,
		Snap: func(path string, kind SnapKind) string {
			if path == trigger && kind == SnapExists {
				editErr = os.WriteFile(input, []byte("changed outside the command"), 0o600)
			}
			return ""
		},
	})
	if editErr != nil {
		t.Fatal(editErr)
	}
	if err != nil || result.Exit != 0 || !strings.Contains(result.Reason, "a read input changed") {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; want changed-input refusal", result.Exit, result.Reason, err)
	}
}

// TestExecRefusesARunningChild needs a real kernel reparenting. The child
// holds stdout and cannot finish until the test cleans it up.
func TestExecRefusesARunningChild(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := Exec(ExecSpec{
		Argv:   []string{"sh", "-c", `cat fifo & printf '%s\n' "$!"`, "sh"},
		Env:    []string{"PATH=/usr/bin:/bin"},
		Dir:    dir,
		Stdout: &stdout,
	})
	pid, parseErr := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("child pid missing from stdout %q: Exec error %v", stdout.String(), err)
	}
	defer func() {
		syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		syscall.Wait4(pid, &status, 0, nil)
	}()
	if err != nil || result.Exit != 0 || !strings.Contains(result.Reason, "is still running") {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; want descendant refusal", result.Exit, result.Reason, err)
	}
}

//go:build unix

package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// serveDaemon is one `ergo serve` the test started, in a process group
// of its own.
type serveDaemon struct {
	cmd             *exec.Cmd
	socket, release string
	exited          chan struct{}
	stdout          []string
}

// startServe starts `ergo serve` with args and waits for its first line:
// the ready line, or "already serving".
func startServe(t *testing.T, bin string, env []string, args ...string) *serveDaemon {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"serve"}, args...)...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &serveDaemon{cmd: cmd, exited: make(chan struct{})}
	// The group is killed only while its leader has not been reaped, so
	// the pid is still this test's.
	t.Cleanup(func() {
		select {
		case <-d.exited:
		default:
			if cmd.Process.Pid > 1 {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			<-d.exited
		}
	})
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			default:
			}
		}
		io.Copy(io.Discard, out)
		cmd.Wait()
		close(d.exited)
	}()
	select {
	case line := <-lines:
		d.stdout = append(d.stdout, line)
		if _, rest, ok := strings.Cut(line, " at "); ok {
			d.socket, d.release, _ = strings.Cut(strings.TrimSpace(rest), " release ")
		}
	case <-d.exited:
	case <-testwait.After(t):
		t.Fatalf("ergo serve %v: no first line", args)
	}
	return d
}

// wait waits for the daemon's exit.
func (d *serveDaemon) wait(t *testing.T, why string) {
	t.Helper()
	select {
	case <-d.exited:
	case <-testwait.After(t):
		t.Fatalf("ergo serve did not exit: %s", why)
	}
}

// TestServeAnswersAWarmCheck runs `ergo serve` on a module (models/serve):
// the daemon answers a warm check on its socket with the CLI's stdout and
// starts no process; `ergo check` as its client does no work of its own; a
// request of another environment or release is not served; a second
// daemon of the workspace and release is refused; a daemon idles out. It
// builds Go because the daemon, its clients and its lock are processes:
// one daemon per workspace and release is a property of processes, which
// no in-process check shows.
func TestServeAnswersAWarmCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := physicalTempDir(t)
	warmModule(t, root, 1, 'a')
	// The daemon and its clients share one environment (serveEnvironment);
	// GOMEMLIMIT bounds each process's heap.
	bound := []string{"GOMEMLIMIT=2GiB"}
	cold := runWarm(t, bin, root, xdg, bound, "check", "./cmd/app")
	if cold.code != 0 {
		t.Fatalf("cold check: exit %d", cold.code)
	}
	runWarm(t, bin, root, xdg, bound, "check", "./cmd/app")

	env := append(os.Environ(), "GOFLAGS=", "XDG_CACHE_HOME="+xdg, bound[0])
	d := startServe(t, bin, env, root)
	if d.socket == "" {
		t.Fatalf("no ready line: %q", d.stdout)
	}

	// The CLI asks the daemon: it reads no input itself.
	via := runWarm(t, bin, root, xdg, bound, "check", "./cmd/app")
	if via.code != 0 || via.stdout != cold.stdout || via.counts["files_read"] != 0 || via.counts["bytes_hashed"] != 0 {
		t.Fatalf("check through the daemon: exit %d, stdout %q (want %q), counts %v", via.code, via.stdout, cold.stdout, via.counts)
	}

	request := serveRequest{Release: d.release, Dir: root, Env: env, Args: []string{"check", "./cmd/app"}}
	answer, ok := ask(d.socket, request)
	if !ok || !answer.Served || string(answer.Stdout) != cold.stdout || answer.Work["processes"] != 0 || answer.Work["files_read"] != 4 {
		t.Fatalf("a request on the socket: ok %v, %+v", ok, answer)
	}
	for _, row := range []struct {
		name string
		edit func(*serveRequest)
	}{
		{"another environment", func(r *serveRequest) { r.Env = append(append([]string(nil), env...), "ERGO_BUILD=full") }},
		{"another release", func(r *serveRequest) { r.Release = "other" }},
	} {
		other := request
		row.edit(&other)
		if answer, ok := ask(d.socket, other); !ok || answer.Served {
			t.Errorf("%s: ok %v, served %+v", row.name, ok, answer)
		}
	}

	// An edit of an input is not served from the hash the daemon kept;
	// once the CLI has answered the edited source, the daemon serves it.
	warmModule(t, root, 2, 'a')
	if answer, ok := ask(d.socket, request); !ok || answer.Served {
		t.Fatalf("a request after an edit: ok %v, served %+v", ok, answer)
	}
	edited := runWarm(t, bin, root, xdg, bound, "check", "./cmd/app")
	if answer, ok := ask(d.socket, request); !ok || !answer.Served || string(answer.Stdout) != edited.stdout || edited.stdout == cold.stdout {
		t.Fatalf("a request after the edit was checked: ok %v, %+v", ok, answer)
	}

	second := startServe(t, bin, env, root, "--idle", "1ms")
	second.wait(t, "a second daemon of the release")
	if len(second.stdout) != 1 || !strings.Contains(second.stdout[0], "already serving") || second.cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("a second daemon: exit %d, %q", second.cmd.ProcessState.ExitCode(), second.stdout)
	}
	if err := syscall.Kill(d.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	d.wait(t, "SIGTERM")

	idle := startServe(t, bin, env, root, "--idle", "1ms")
	if idle.socket == "" {
		t.Fatalf("a daemon after the first exited: %q", idle.stdout)
	}
	idle.wait(t, "--idle 1ms")
	if _, err := os.Stat(idle.socket); !os.IsNotExist(err) {
		t.Fatalf("an idle daemon left its socket: %v", err)
	}
	after := runWarm(t, bin, root, xdg, bound, "check", "./cmd/app")
	if after.code != 0 || after.stdout != edited.stdout || after.counts["files_read"] != 4 {
		t.Fatalf("check with no daemon: exit %d, %v", after.code, after.counts)
	}
}

// TestServeClientAnswersInProcessWhenTheConnectionEnds: a daemon that
// exits or crashes after it accepted a request leaves the client no
// answer, and the client answers in process (models/serve,
// NoRequestLost).
func TestServeClientAnswersInProcessWhenTheConnectionEnds(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		bufio.NewReader(conn).ReadString('\n')
		conn.Close()
	}()
	if answer, ok := ask(socket, serveRequest{Release: "r", Args: []string{"check", "."}}); ok {
		t.Fatalf("a connection with no answer was taken as one: %+v", answer)
	}
}

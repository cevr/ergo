package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

const devHelp = `usage: ergo dev [DIR] [--idle DURATION]

Serves the workspace DIR (default: the working directory) until it has
had no request for DURATION (default 10m), or until SIGINT or SIGTERM.
One daemon serves a workspace for each Ergo release: a second ergo dev
of the same workspace and release prints "already running" and exits 0.

A client (ergo check, an editor, MCP) sends a request on the daemon's
Unix socket and gets the same answer as the command, without starting a
process when the daemon can answer from memory: today, a check whose
warm answer holds (docs/tooling.md, ergo dev). Any other request is
answered "not served", and the client answers it in process, as it does
when no daemon of its release serves the package's folder or an
ancestor of it, or when its connection ends with no answer. ERGO_DEV=off
makes ergo check answer in process.

The model of this lifecycle is models/dev. Exit status: 0, 1 when the
socket cannot be served, or 2 on an invalid invocation.
`

// devVariable turns the CLI's client off (ERGO_DEV=off).
const devVariable = "ERGO_DEV"

// devRequest is one request on the daemon's socket: one JSON object,
// answered by one devAnswer.
type devRequest struct {
	// Release is the client's release (producer.Identity.Qualifier).
	Release string `json:"release"`
	// Dir is the client's working directory; a relative path in Args is
	// read from it.
	Dir string `json:"dir"`
	// Env is the client's environment: a request is answered from memory
	// only in the daemon's own environment (devEnvironment).
	Env []string `json:"env"`
	// Args are the command's words: ["check", DIR, options...].
	Args []string `json:"args"`
}

// devAnswer answers a devRequest. Served false is "not served": the
// client answers in process; Reason says why.
type devAnswer struct {
	Served bool   `json:"served"`
	Stdout []byte `json:"stdout,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Work is the stage counters the daemon spent on the request.
	Work map[string]int64 `json:"work,omitempty"`
}

// errRunning is a second daemon's refusal: the lock is held.
var errRunning = errors.New("already running")

// devRelease is the release a daemon serves and a client asks for;
// "" when this compiler has no shared identity (sketchfront.WarmKey), so
// no warm answer, and no daemon, applies.
func devRelease() string {
	if sketchfront.WarmKey("dev") == "" {
		return ""
	}
	return producer.Current().Qualifier
}

// devPaths are the socket and the lock file of a workspace and a
// release, under the cache root: two releases serve one workspace with
// two daemons.
func devPaths(root, release, workspace string) (socket, lock string) {
	sum := sha256.Sum256([]byte(release + "\x00" + workspace))
	base := filepath.Join(root, "dev", hex.EncodeToString(sum[:8]))
	return base + ".sock", base + ".lock"
}

// devEnvironment is env without the variables a shell sets for itself
// and ERGO_STAGE_REPORT (it names where a process writes its own
// counters), sorted: the environment a request's answer depends on. A
// name given twice keeps its last value, as a process started with env
// sees it (os/exec).
func devEnvironment(env []string) []string {
	last := map[string]string{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PWD", "OLDPWD", "SHLVL", "_", sketchfront.StageReportVariable:
			continue
		}
		last[name] = kv
	}
	out := make([]string, 0, len(last))
	for _, kv := range last {
		out = append(out, kv)
	}
	slices.Sort(out)
	return out
}

func devCommand(args []string) error {
	flags := flag.NewFlagSet("dev", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	idle := flags.Duration("idle", 10*time.Minute, "")
	var positional []string
	for len(args) > 0 {
		if err := flags.Parse(args); err != nil {
			return invalidInvocation(err)
		}
		args = flags.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}
	if len(positional) > 1 || *idle <= 0 {
		return usageError{message: "usage: ergo dev [DIR] [--idle DURATION]"}
	}
	dir := "."
	if len(positional) == 1 {
		dir = positional[0]
	}
	workspace, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return fmt.Errorf("ergo dev: %s is not a folder", dir)
	}
	release := devRelease()
	if release == "" {
		return fmt.Errorf("ergo dev: this ergo has no shared identity, so nothing can be served")
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	d := &daemon{release: release, env: devEnvironment(os.Environ())}
	d.socket, d.lock = devPaths(root, release, workspace)
	// The ready line names the release a client's request carries.
	ready := func() { fmt.Printf("ergo dev: serving %s at %s release %s\n", workspace, d.socket, release) }
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	err = d.serve(*idle, ready, stop)
	if errors.Is(err, errRunning) {
		fmt.Printf("ergo dev: already running %s at %s\n", workspace, d.socket)
		return nil
	}
	return err
}

// daemon is one `ergo dev`.
type daemon struct {
	release      string
	env          []string
	socket, lock string
	// mu runs one request at a time, in its client's working directory.
	mu sync.Mutex
	// sums keeps the inputs' hashes between requests: a file whose stat
	// has not changed is not read again (sketchfront.StatSums).
	sums sketchfront.StatSums
}

// serve takes the lock, listens, and answers until idle passes with no
// request or stop: the order of models/dev. The socket file is removed
// and the listener closed before the requests in flight are answered and
// the lock is given back, so a client that connects late finds no
// listener and answers in process.
func (d *daemon) serve(idle time.Duration, ready func(), stop <-chan os.Signal) error {
	if err := os.MkdirAll(filepath.Dir(d.socket), 0o755); err != nil {
		return err
	}
	unlock, err := lockDev(d.lock)
	if err != nil {
		return err
	}
	defer unlock()
	// Holding the lock, a socket file is a dead daemon's.
	if err := os.Remove(d.socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := devListen(d.socket)
	if err != nil {
		return fmt.Errorf("ergo dev: %w", err)
	}
	ready()
	var flight sync.WaitGroup
	activity := make(chan struct{}, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			flight.Add(1)
			go func() {
				defer flight.Done()
				d.handle(conn)
				select {
				case activity <- struct{}{}:
				default:
				}
			}()
		}
	}()
	timer := time.NewTimer(idle)
	defer timer.Stop()
loop:
	for {
		select {
		case <-activity:
			timer.Reset(idle)
		case <-timer.C:
			break loop
		case <-stop:
			break loop
		case <-closed:
			break loop
		}
	}
	_ = os.Remove(d.socket)
	listener.Close()
	<-closed
	flight.Wait()
	return nil
}

// handle answers one connection's request.
func (d *daemon) handle(conn net.Conn) {
	defer conn.Close()
	var req devRequest
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(conn).Encode(d.answer(req))
}

// answer is the request's answer from memory, or "not served".
func (d *daemon) answer(req devRequest) devAnswer {
	if req.Release != d.release {
		return devAnswer{Reason: "another release"}
	}
	if !slices.Equal(devEnvironment(req.Env), d.env) {
		return devAnswer{Reason: "another environment"}
	}
	if len(req.Args) == 0 || req.Args[0] != "check" || !filepath.IsAbs(req.Dir) {
		return devAnswer{Reason: "not served"}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// The request is read from the client's working directory, as the
	// command reads it; d.mu holds it for the request.
	if err := os.Chdir(req.Dir); err != nil {
		return devAnswer{Reason: "no working directory"}
	}
	before := stage.Snapshot()
	opts, err := parseFileOptions("check", req.Args[1:])
	if err != nil || recursivePattern(opts.file) {
		return devAnswer{Reason: "not served"}
	}
	w, valid := prepareWarm(opts, nil)
	if w == nil {
		return devAnswer{Reason: "no warm answer"}
	}
	answer, hit := sketchfront.WarmHit(w.cache, w.key, valid, &d.sums)
	if !hit {
		return devAnswer{Reason: "no warm answer"}
	}
	work := map[string]int64{}
	for name, n := range stage.Snapshot() {
		if n != before[name] {
			work[name] = n - before[name]
		}
	}
	return devAnswer{Served: true, Stdout: answer.Stdout, Work: work}
}

// askDev is the CLI's client: it sends args, read from the working
// directory dir, to the daemon of this release serving the package folder
// pkg or its nearest ancestor that has one, and returns the answer's
// stdout when the daemon served it. Otherwise the caller answers in
// process.
func askDev(dir, pkg string, args []string) ([]byte, bool) {
	if os.Getenv(devVariable) == "off" {
		return nil, false
	}
	release := devRelease()
	root, err := build.DefaultRoot()
	if release == "" || err != nil || !filepath.IsAbs(dir) {
		return nil, false
	}
	if !filepath.IsAbs(pkg) {
		pkg = filepath.Join(dir, pkg)
	}
	for at := filepath.Clean(pkg); ; {
		socket, _ := devPaths(root, release, at)
		if _, err := os.Stat(socket); err == nil {
			answer, ok := ask(socket, devRequest{Release: release, Dir: dir, Env: os.Environ(), Args: args})
			return answer.Stdout, ok && answer.Served
		}
		parent := filepath.Dir(at)
		if parent == at {
			return nil, false
		}
		at = parent
	}
}

// ask sends one request on socket. ok is false when the connection fails
// or ends with no answer: the daemon exited or crashed, and the request,
// a pure function of its inputs, is answered in process.
func ask(socket string, req devRequest) (answer devAnswer, ok bool) {
	conn, err := devDial(socket)
	if err != nil {
		return devAnswer{}, false
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return devAnswer{}, false
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&answer); err != nil {
		return devAnswer{}, false
	}
	return answer, true
}

// devListen listens on the unix socket at path and devDial connects to
// it. A socket's path is at most devSocketPathMax bytes, and a deep cache
// root goes past that, where bind fails with EINVAL (I19 full r4: the
// nested temporary directories of TestPackageNeverWritesUnderHome). Then
// both reach the socket through a shorter name of the same path
// (viaShortPath), so the socket stays under the cache root, the only
// place the daemon writes.
func devListen(path string) (net.Listener, error) {
	var listener *net.UnixListener
	err := viaShortPath(path, func(name string) error {
		addr, err := net.ResolveUnixAddr("unix", name)
		if err != nil {
			return err
		}
		listener, err = net.ListenUnix("unix", addr)
		return err
	})
	if err != nil {
		return nil, err
	}
	// serve removes the socket by path: the short name means another
	// folder, or none, once its descriptor is closed.
	listener.SetUnlinkOnClose(false)
	return listener, nil
}

// devDial connects to the unix socket at path (devListen).
func devDial(path string) (net.Conn, error) {
	var conn net.Conn
	err := viaShortPath(path, func(name string) (err error) {
		conn, err = net.Dial("unix", name)
		return err
	})
	return conn, err
}

// devSocketPathMax is the longest socket path every unix host binds:
// darwin's sun_path of 104 bytes less its NUL (Linux's is 108).
const devSocketPathMax = 103

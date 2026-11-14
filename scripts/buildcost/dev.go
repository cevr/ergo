package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// devRequests is the number of requests a counted daemon answers.
const devRequests = 200

// devCost counts `ergo dev` (compile-speed plan §3.1, CS9) on the
// program at dir: the CLI's cold and warm check, then two daemons, each
// started with --idle 2s and measured from start to idle exit: one sent
// no request, one sent devRequests warm checks on its socket by this
// process (no ergo process per request). The difference of their
// instructions, over devRequests, is the daemon's work for one request;
// the daemon's own counters for the last request and the requests' wall
// times are printed beside it. Last, `ergo check` as the daemon's client.
func (r runner) devCost(label, dir, main string) {
	xdg := filepath.Join(r.work, "xdg-dev-"+label)
	pkg := filepath.Join(dir, main)
	r.counted(label+" check-1", xdg, "check", pkg)
	r.counted(label+" check-2", xdg, "check", pkg)
	env := append(os.Environ(), "XDG_CACHE_HOME="+xdg)
	idle := r.daemon(label+" dev-0-requests", dir, env, 0, pkg)
	busy := r.daemon(fmt.Sprintf("%s dev-%d-requests", label, devRequests), dir, env, devRequests, pkg)
	if idle.instructions > 0 && busy.instructions > idle.instructions {
		fmt.Printf("%-28s instr_M_per_request=%.3f\n", label+" dev-request", float64(busy.instructions-idle.instructions)/1e6/devRequests)
	}
	// The CLI as the daemon's client: a daemon that idles out after it.
	ready := make(chan string, 1)
	cmd := exec.Command(r.ergo, "dev", dir, "--idle", "2s")
	cmd.Env = env
	cmd.Stdout = &readyLine{ch: ready}
	if err := cmd.Start(); err != nil {
		fmt.Println("    dev:", err)
		return
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	select {
	case <-ready:
		r.counted(label+" check-via-dev", xdg, "check", pkg)
	case <-exited:
		fmt.Println("    dev: exited before its ready line")
	}
	<-exited
}

// daemon measures one `ergo dev` from start to idle exit, sending n
// requests once it is ready.
func (r runner) daemon(label, dir string, env []string, n int, pkg string) result {
	ready := make(chan string, 1)
	cmd := exec.Command(r.ergo, "dev", dir, "--idle", "2s")
	cmd.Env = env
	cmd.Stdout = &readyLine{ch: ready}
	var walls []time.Duration
	var last map[string]any
	var failed string
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		var line string
		select {
		case line = <-ready:
		case <-done:
			failed = "the daemon exited before its ready line"
			return
		}
		socket, release, ok := parseReady(line)
		if !ok {
			failed = "ready line " + line
			return
		}
		req, _ := json.Marshal(map[string]any{"release": release, "dir": dir, "env": env, "args": []string{"check", pkg}})
		for range n {
			start := time.Now()
			answer, err := request(socket, req)
			walls = append(walls, time.Since(start))
			if err != nil || answer["served"] != true {
				failed = fmt.Sprintf("not served: %v %v", err, answer["reason"])
				return
			}
			last = answer
		}
	}()
	res := measure(cmd)
	close(done)
	wg.Wait()
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s exit=%d", label, res.exit)
	if res.instructions > 0 {
		fmt.Fprintf(&b, " instr_M=%.1f", float64(res.instructions)/1e6)
	}
	if last != nil {
		work, _ := json.Marshal(last["work"])
		fmt.Fprintf(&b, " last_request_work=%s", work)
	}
	if len(walls) > 0 {
		slices.Sort(walls)
		fmt.Fprintf(&b, " | request_wall_us median=%d p90=%d max=%d", walls[len(walls)/2].Microseconds(), walls[len(walls)*9/10].Microseconds(), walls[len(walls)-1].Microseconds())
	}
	fmt.Fprintf(&b, " user_s=%.2f sys_s=%.2f", res.user.Seconds(), res.sys.Seconds())
	fmt.Println(b.String())
	if failed != "" {
		fmt.Println("    " + failed)
	}
	return res
}

// parseReady reads the socket and the release from the ready line
// ("ergo dev: serving DIR at SOCKET release RELEASE").
func parseReady(line string) (socket, release string, ok bool) {
	_, rest, ok := strings.Cut(line, " at ")
	if !ok {
		return "", "", false
	}
	socket, release, ok = strings.Cut(strings.TrimSpace(rest), " release ")
	return socket, release, ok
}

// request sends one request and reads its answer.
func request(socket string, req []byte) (map[string]any, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	var answer map[string]any
	err = json.NewDecoder(bufio.NewReader(conn)).Decode(&answer)
	return answer, err
}

// readyLine sends the first line written to it.
type readyLine struct {
	buf  bytes.Buffer
	sent bool
	ch   chan string
}

func (w *readyLine) Write(p []byte) (int, error) {
	if !w.sent {
		w.buf.Write(p)
		if line, _, ok := strings.Cut(w.buf.String(), "\n"); ok {
			w.sent = true
			w.ch <- line
		}
	}
	return len(p), nil
}

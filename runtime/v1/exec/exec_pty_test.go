//go:build linux || darwin

package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

func TestPTYOpenResizeEchoAndClose(t *testing.T) {
	pty, err := ExecOpenPTY()
	if err != nil {
		t.Fatalf("ExecOpenPTY: %v", err)
	}
	defer pty.Close()

	if pty.Fd() < 0 {
		t.Fatalf("expected valid fd, got %d", pty.Fd())
	}
	if pty.SlavePath() == "" {
		t.Fatalf("expected non-empty slave path")
	}

	ws := ExecWinsize{Rows: 40, Cols: 120, XPixels: 960, YPixels: 640}
	if err := pty.Resize(ws); err != nil {
		t.Fatalf("Resize valid: %v", err)
	}
	got, err := pty.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if got != ws {
		t.Fatalf("Size = %+v, want %+v", got, ws)
	}

	for _, bad := range []ExecWinsize{
		{Rows: 0, Cols: 80},
		{Rows: 24, Cols: 0},
		{Rows: -1, Cols: 80},
		{Rows: 70000, Cols: 80},
		{Rows: 24, Cols: 80, XPixels: -1},
		{Rows: 24, Cols: 80, YPixels: 65536},
	} {
		if err := pty.Resize(bad); err == nil {
			t.Fatalf("Resize(%+v) unexpectedly succeeded", bad)
		}
	}
	afterBad, err := pty.Size()
	if err != nil || afterBad != ws {
		t.Fatalf("Size after invalid Resize = (%+v, %v), want %+v", afterBad, err, ws)
	}

	// Child starts with slave PTY as controlling tty and inherits initial 40x120 size.
	root := rt.Root("pty-echo")
	env := map[string]string{"PATH": "/bin:/usr/bin"}
	r, err := ExecStartWithPTY(root, "sh", []string{"-c", `stty size; exec cat`}, env, "", "", 200*time.Millisecond, pty)
	if err != nil {
		t.Fatalf("ExecStartWithPTY: %v", err)
	}
	if r.Pid() <= 0 {
		t.Fatalf("expected positive child pid, got %d", r.Pid())
	}

	stopWatch := testwait.AfterFunc(t, func() { pty.Close() })
	var out strings.Builder
	for !strings.Contains(out.String(), "40 120") {
		chunk, err := pty.Read(root)
		if err != nil {
			stopWatch()
			t.Fatalf("pty.Read waiting for stty size (got %q): %v", out.String(), err)
		}
		out.WriteString(chunk)
	}

	if err := pty.Write("hello-pty\n"); err != nil {
		stopWatch()
		t.Fatalf("pty.Write: %v", err)
	}
	out.Reset()
	for !strings.Contains(out.String(), "hello-pty") {
		chunk, err := pty.Read(root)
		if err != nil {
			stopWatch()
			t.Fatalf("pty.Read waiting for echo (got %q): %v", out.String(), err)
		}
		out.WriteString(chunk)
	}
	stopWatch()

	status := r.Stop(root)
	if status == 0 {
		t.Logf("child stopped cleanly with status %d", status)
	}

	pty.Close()
	if pty.Fd() != -1 {
		t.Fatalf("closed pty.Fd() = %d, want -1", pty.Fd())
	}
	if _, err := pty.Size(); err == nil {
		t.Fatal("closed Size succeeded")
	}
	if err := pty.Resize(ws); err == nil {
		t.Fatal("closed Resize succeeded")
	}
	if _, err := pty.DupFD(); err == nil {
		t.Fatal("closed DupFD succeeded")
	}
	if err := pty.Write("x"); err == nil {
		t.Fatal("closed Write succeeded")
	}
	if _, err := pty.Read(root); err == nil {
		t.Fatal("closed Read succeeded")
	}
}

func TestRunningInteractiveAndLiveDetachPreservesChild(t *testing.T) {
	root := rt.Root("detach")
	env := map[string]string{"PATH": "/bin:/usr/bin"}

	r, err := ExecStartInteractive(root, "sh", []string{"-c", `printf "early\n"; cat`}, env, "", "", 200*time.Millisecond, execToCaller, "", false, execToCaller, "", false)
	if err != nil {
		t.Fatalf("ExecStartInteractive: %v", err)
	}
	pid := r.Pid()
	if pid <= 0 {
		t.Fatalf("expected positive pid, got %d", pid)
	}

	// Read first line through Running.Lines before detaching.
	var firstLine string
	for l := range r.Lines(root) {
		firstLine = l.Text
		break
	}
	if firstLine != "early" {
		t.Fatalf("first line = %q, want %q", firstLine, "early")
	}

	if err := r.WriteStdin("middle\n"); err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}

	dr, err := r.Detach()
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	// Closing consumed Running must not kill detached child or transferred pipes.
	r.Close(root)
	if st := r.Stop(root); st != 0 {
		t.Fatalf("post-detach Stop status = %d, want 0", st)
	}

	info := dr.Info()
	if info.Pid != pid || info.Name != "sh" {
		t.Fatalf("Detached Info = %+v, want pid=%d", info, pid)
	}
	if !ExecAlive(info.Pid, info.Start) {
		t.Fatalf("detached child pid=%d not alive after Running.Close/Stop", info.Pid)
	}

	inP, ok := dr.TakeStdin()
	if !ok || inP == nil {
		t.Fatal("TakeStdin returned missing pipe")
	}
	if second, ok2 := dr.TakeStdin(); ok2 || second != nil {
		t.Fatal("second TakeStdin must return false")
	}
	defer inP.Close()

	outP, ok := dr.TakeStdout()
	if !ok || outP == nil {
		t.Fatal("TakeStdout returned missing pipe")
	}
	defer outP.Close()

	errP, ok := dr.TakeStderr()
	if !ok || errP == nil {
		t.Fatal("TakeStderr returned missing pipe")
	}
	errP.Close()
	dr.Close()

	if err := inP.Write("final\n"); err != nil {
		t.Fatalf("extracted stdin Write: %v", err)
	}
	inP.Close()

	var collected strings.Builder
	collected.WriteString(dr.BufferedStdout())
	for {
		chunk, err := outP.Read(root)
		if err != nil {
			t.Fatalf("extracted stdout Read: %v", err)
		}
		if chunk == "" {
			break
		}
		collected.WriteString(chunk)
	}
	got := collected.String()
	if !strings.Contains(got, "middle\n") || !strings.Contains(got, "final\n") {
		t.Fatalf("collected stdout = %q, want both middle and final", got)
	}

	kind, st, sig, notChild, reason, err := ExecWaitDetached(root, info.Pid, info.Start)
	if err != nil {
		t.Fatalf("ExecWaitDetached: %v", err)
	}
	if kind != execOutcomeExited || st != 0 || sig != 0 || notChild {
		t.Fatalf("ExecWaitDetached = (%d, %d, %d, %v, %q), want Exited(0, 0)", kind, st, sig, notChild, reason)
	}

	// Second PollDetached/WaitDetached sees cached exact status cleanly.
	pkind, pst, psig, _, _, perr := ExecPollDetached(info.Pid, info.Start)
	if perr != nil || pkind != execOutcomeExited || pst != 0 || psig != 0 {
		t.Fatalf("second ExecPollDetached = (%d, %d, %d, %v), want Exited(0, 0)", pkind, pst, psig, perr)
	}

	// Already-ended Replay or waited Running refuses Detach.
	replay := ExecReplay([]ExecChunk{{Text: "ok"}}, 0, true)
	if _, err := replay.Detach(); err == nil {
		t.Fatal("Replay.Detach unexpectedly succeeded")
	}
	replay.Close(root)
}

func TestReattachValidationRefusals(t *testing.T) {
	if _, err := ExecReattachPTY(-1); err == nil {
		t.Fatal("ExecReattachPTY(-1) succeeded")
	}
	if _, err := ExecReattachPipe(-1); err == nil {
		t.Fatal("ExecReattachPipe(-1) succeeded")
	}
	pr, pw, err := ExecOpenPipe()
	if err != nil {
		t.Fatalf("ExecOpenPipe: %v", err)
	}
	// Passing a non-TTY pipe FD to ReattachPTY must fail cleanly.
	if _, err := ExecReattachPTY(pr.Fd()); err == nil {
		t.Fatal("ExecReattachPTY(pipeFD) succeeded")
	}
	closedFD := pr.Fd()
	pr.Close()
	pw.Close()
	if _, err := ExecReattachPipe(closedFD); err == nil {
		t.Fatal("ExecReattachPipe(closedFD) succeeded")
	}
	if _, err := ExecReattachPTY(closedFD); err == nil {
		t.Fatal("ExecReattachPTY(closedFD) succeeded")
	}
}

func TestPTYAndPipeStopAndMaskLaws(t *testing.T) {
	// 1. Pre-stopped task stops ExecStartWithPTY, ExecSpawnWithPTY, ExecStartInteractive, ExecWaitDetached before acting.
	pty, err := ExecOpenPTY()
	if err != nil {
		t.Fatalf("ExecOpenPTY: %v", err)
	}
	defer pty.Close()
	env := map[string]string{"PATH": "/bin:/usr/bin"}

	stoppedExpect := func(name string, fn func(*rt.Task)) {
		t.Helper()
		task := rt.Root(name)
		task.Interrupt()
		defer func() {
			r := recover()
			if r != rt.Interrupted {
				t.Fatalf("%s: expected panic(rt.Interrupted), got %v", name, r)
			}
		}()
		fn(task)
		t.Fatalf("%s did not interrupt pre-stopped task", name)
	}
	stoppedExpect("ExecStartWithPTY", func(tk *rt.Task) { _, _ = ExecStartWithPTY(tk, "true", nil, env, "", "", 0, pty) })
	stoppedExpect("ExecSpawnWithPTY", func(tk *rt.Task) { _, _ = ExecSpawnWithPTY(tk, "true", nil, env, "", "", pty) })
	stoppedExpect("ExecStartInteractive", func(tk *rt.Task) {
		_, _ = ExecStartInteractive(tk, "true", nil, env, "", "", 0, execDiscard, "", false, execDiscard, "", false)
	})
	stoppedExpect("ExecWaitDetached", func(tk *rt.Task) { _, _, _, _, _, _ = ExecWaitDetached(tk, 1, 1) })

	// 2. Active PTY.Read interrupted when task is stopped while slave writes nothing.
	root := rt.Root("pty-read-stop")
	r, err := ExecStartWithPTY(root, "sh", []string{"-c", "sleep 20"}, env, "", "", 0, pty)
	if err != nil {
		t.Fatalf("ExecStartWithPTY sleep: %v", err)
	}
	defer r.Close(root)

	stopTask := rt.Root("pty-reader")
	ptyWaiting := make(chan struct{})
	readDone := make(chan any, 1)
	go func() {
		defer func() {
			readDone <- recover()
		}()
		cancel := rt.WhenWaiting(stopTask, func() { close(ptyWaiting) })
		defer cancel()
		_, err := pty.Read(stopTask)
		readDone <- err
	}()
	select {
	case <-ptyWaiting:
	case <-testwait.After(t):
		t.Fatal("PTY.Read did not reach wait")
	}
	stopTask.Interrupt()
	select {
	case res := <-readDone:
		if res != rt.Interrupted {
			t.Fatalf("PTY.Read after Interrupt = %v, want rt.Interrupted", res)
		}
	case <-testwait.After(t):
		t.Fatal("PTY.Read did not unblock on task stop")
	}

	// 3. Active Pipe.Read interrupted cleanly on task stop.
	pr, pw, err := ExecOpenPipe()
	if err != nil {
		t.Fatalf("ExecOpenPipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	pipeTask := rt.Root("pipe-reader")
	pipeWaiting := make(chan struct{})
	pipeDone := make(chan any, 1)
	go func() {
		defer func() {
			pipeDone <- recover()
		}()
		cancel := rt.WhenWaiting(pipeTask, func() { close(pipeWaiting) })
		defer cancel()
		_, err := pr.Read(pipeTask)
		pipeDone <- err
	}()
	select {
	case <-pipeWaiting:
	case <-testwait.After(t):
		t.Fatal("Pipe.Read did not reach wait")
	}
	pipeTask.Interrupt()
	select {
	case res := <-pipeDone:
		if res != rt.Interrupted {
			t.Fatalf("Pipe.Read after Interrupt = %v, want rt.Interrupted", res)
		}
	case <-testwait.After(t):
		t.Fatal("Pipe.Read did not unblock on task stop")
	}

	// 4. Active ExecWaitDetached interrupted on stop, and completes when masked.
	d, err := ExecSpawn(root, "sh", []string{"-c", "sleep 20"}, env, "", "", execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatalf("ExecSpawn: %v", err)
	}
	defer ExecKill(rt.Root("cleanup"), d.Pid, d.Start, 0)

	waitTask := rt.Root("wait-detached-stop")
	waitWaiting := make(chan struct{})
	waitDone := make(chan any, 1)
	go func() {
		defer func() {
			waitDone <- recover()
		}()
		cancel := rt.WhenWaiting(waitTask, func() { close(waitWaiting) })
		defer cancel()
		_, _, _, _, _, err := ExecWaitDetached(waitTask, d.Pid, d.Start)
		waitDone <- err
	}()
	select {
	case <-waitWaiting:
	case <-testwait.After(t):
		t.Fatal("ExecWaitDetached did not reach wait")
	}
	waitTask.Interrupt()
	select {
	case res := <-waitDone:
		if res != rt.Interrupted {
			t.Fatalf("ExecWaitDetached after Interrupt = %v, want rt.Interrupted", res)
		}
	case <-testwait.After(t):
		t.Fatal("ExecWaitDetached did not unblock on task stop")
	}

	// Masked ExecWaitDetached ignores task interruption and waits to completion.
	d2, err := ExecSpawn(root, "sh", []string{"-c", "sleep 0.05; exit 7"}, env, "", "", execDiscard, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatalf("ExecSpawn d2: %v", err)
	}
	maskedTask := rt.Root("wait-masked")
	maskedTask.Mask()
	maskedTask.Interrupt()
	kind, st, sig, _, _, werr := ExecWaitDetached(maskedTask, d2.Pid, d2.Start)
	if werr != nil || kind != execOutcomeExited || st != 7 || sig != 0 {
		t.Fatalf("masked ExecWaitDetached = (%d, %d, %d, %v), want Exited(7, 0)", kind, st, sig, werr)
	}
}

type cutoverPayload struct {
	PTYPid      int    `json:"pty_pid"`
	PTYStart    int64  `json:"pty_start"`
	PTYName     string `json:"pty_name"`
	PipePid     int    `json:"pipe_pid"`
	PipeStart   int64  `json:"pipe_start"`
	PipeName    string `json:"pipe_name"`
	BufferedOut string `json:"buffered_out"`
}

func TestCutoverHelperServer1(t *testing.T) {
	sock := os.Getenv("ERGO_CUTOVER_SOCK")
	if sock == "" {
		t.Skip("helper for TestServerCutoverPTYAndPipeReattachCrossProcess")
	}
	root := rt.Root("server1")
	env := map[string]string{"PATH": "/bin:/usr/bin"}

	pty, err := ExecOpenPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	if err := pty.Resize(ExecWinsize{Rows: 33, Cols: 99, XPixels: 400, YPixels: 300}); err != nil {
		t.Fatal(err)
	}
	ptyRun, err := ExecStartWithPTY(root, "sh", []string{"-c", `trap "" HUP; exec cat`}, env, "", "", 0, pty)
	if err != nil {
		t.Fatal(err)
	}
	drPTY, err := ptyRun.Detach()
	if err != nil {
		t.Fatal(err)
	}
	ptyInfo := drPTY.IntoDetached()

	pipeRun, err := ExecStartInteractive(root, "sh", []string{"-c", `trap "" HUP; exec cat`}, env, "", "", 0, execToCaller, "", false, execDiscard, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeRun.WriteStdin("from-server1\n"); err != nil {
		t.Fatal(err)
	}
	drPipe, err := pipeRun.Detach()
	if err != nil {
		t.Fatal(err)
	}
	pipeInfo := drPipe.Info()
	inP, ok := drPipe.TakeStdin()
	if !ok {
		t.Fatal("missing stdin pipe")
	}
	defer inP.Close()
	outP, ok := drPipe.TakeStdout()
	if !ok {
		t.Fatal("missing stdout pipe")
	}
	defer outP.Close()

	ptyFD, err := pty.DupFD()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(ptyFD) }()
	inFD, err := inP.DupFD()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(inFD) }()
	outFD, err := outP.DupFD()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(outFD) }()

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload, err := json.Marshal(cutoverPayload{
		PTYPid:      ptyInfo.Pid,
		PTYStart:    ptyInfo.Start,
		PTYName:     ptyInfo.Name,
		PipePid:     pipeInfo.Pid,
		PipeStart:   pipeInfo.Start,
		PipeName:    pipeInfo.Name,
		BufferedOut: drPipe.BufferedStdout(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rights := syscall.UnixRights(ptyFD, inFD, outFD)
	if _, _, err := conn.WriteMsgUnix(payload, rights, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverHelperServer2(t *testing.T) {
	sock := os.Getenv("ERGO_CUTOVER_SERVER2_SOCK")
	if sock == "" {
		t.Skip("helper server2 for TestServerCutoverPTYAndPipeReattachCrossProcess")
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	conn, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	buf := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(3*4))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		t.Fatal(err)
	}
	var p cutoverPayload
	if err := json.Unmarshal(buf[:n], &p); err != nil {
		t.Fatal(err)
	}
	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		t.Fatal(err)
	}
	fds, err := syscall.ParseUnixRights(&scms[0])
	if err != nil || len(fds) != 3 {
		t.Fatalf("ParseUnixRights = (%v, %v), want 3 FDs", fds, err)
	}
	recvPTYFD, recvInFD, recvOutFD := fds[0], fds[1], fds[2]
	// Close received FDs right after reattaching to verify Dup/CLOEXEC independence.
	pty, err := ExecReattachPTY(recvPTYFD)
	_ = syscall.Close(recvPTYFD)
	if err != nil {
		t.Fatalf("ExecReattachPTY: %v", err)
	}
	defer pty.Close()

	inPipe, err := ExecReattachPipe(recvInFD)
	_ = syscall.Close(recvInFD)
	if err != nil {
		t.Fatalf("ExecReattachPipe stdin: %v", err)
	}
	defer inPipe.Close()

	outPipe, err := ExecReattachPipe(recvOutFD)
	_ = syscall.Close(recvOutFD)
	if err != nil {
		t.Fatalf("ExecReattachPipe stdout: %v", err)
	}
	defer outPipe.Close()

	// Wait until Server 1 process has completely exited (polling marker file).
	waitFile := os.Getenv("ERGO_CUTOVER_WAIT_FILE")
	if waitFile != "" {
		if !testwait.Until(t, func() bool { _, serr := os.Stat(waitFile); return serr == nil }) {
			t.Fatalf("timed out waiting for server1 marker %s", waitFile)
		}
	}

	root := rt.Root("server2")

	// Verify reattached PTY preserved size and still talks to live reparented process.
	ws, err := pty.Size()
	if err != nil || ws != (ExecWinsize{Rows: 33, Cols: 99, XPixels: 400, YPixels: 300}) {
		t.Fatalf("reattached Size = (%+v, %v), want 33x99", ws, err)
	}
	if !ExecAlive(p.PTYPid, p.PTYStart) {
		t.Fatalf("PTY child pid=%d died on Server 1 exit", p.PTYPid)
	}
	if err := pty.Write("from-server2-pty\n"); err != nil {
		t.Fatal(err)
	}
	stopPTYRead := testwait.AfterFunc(t, func() { pty.Close() })
	var ptyOut strings.Builder
	for !strings.Contains(ptyOut.String(), "from-server2-pty") {
		chunk, err := pty.Read(root)
		if err != nil {
			stopPTYRead()
			t.Fatalf("pty.Read waiting for reattached PTY echo (got %q): %v", ptyOut.String(), err)
		}
		ptyOut.WriteString(chunk)
	}
	stopPTYRead()

	// Verify reattached stdin/stdout pipes talk to live reparented process and collect exit!
	if err := inPipe.Write("from-server2-pipe\n"); err != nil {
		t.Fatal(err)
	}
	inPipe.Close()
	var pipeOut strings.Builder
	pipeOut.WriteString(p.BufferedOut)
	for {
		chunk, err := outPipe.Read(root)
		if err != nil {
			t.Fatal(err)
		}
		if chunk == "" {
			break
		}
		pipeOut.WriteString(chunk)
	}
	if !strings.Contains(pipeOut.String(), "from-server1\n") || !strings.Contains(pipeOut.String(), "from-server2-pipe\n") {
		t.Fatalf("pipe output after cutover = %q", pipeOut.String())
	}

	kind, st, sig, notChild, reason, err := ExecWaitDetached(root, p.PipePid, p.PipeStart)
	if err != nil {
		t.Fatalf("WaitDetached non-child pipe pid: %v", err)
	}
	if kind != execOutcomeExited && !(kind == execOutcomeUnavailable && notChild) {
		t.Fatalf("unexpected WaitDetached outcome (%d, %d, %d, %v, %q)", kind, st, sig, notChild, reason)
	}

	ExecKill(root, p.PTYPid, p.PTYStart, 100*time.Millisecond)
	pkind, pst, psig, pnotChild, preason, perr := ExecWaitDetached(root, p.PTYPid, p.PTYStart)
	if perr != nil {
		t.Fatalf("WaitDetached PTY pid after Kill: %v", perr)
	}
	if pkind == execOutcomeRunning {
		t.Fatalf("PTY pid still reported Running after Kill + WaitDetached")
	}
	if pkind == execOutcomeExited && pst == 0 && psig == 0 {
		t.Logf("observed exact exited status %d sig %d", pst, psig)
	}
	_ = preason
	_ = pnotChild
}

func TestServerCutoverPTYAndPipeReattachCrossProcess(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "cutover.sock")
	waitFile := filepath.Join(dir, "server1.gone")

	server2 := exec.Command(os.Args[0], "-test.run=^TestCutoverHelperServer2$", "-test.count=1")
	server2.Env = append(os.Environ(), "ERGO_CUTOVER_SERVER2_SOCK="+sock, "ERGO_CUTOVER_WAIT_FILE="+waitFile)
	s2Out := &strings.Builder{}
	server2.Stdout = s2Out
	server2.Stderr = s2Out
	if err := server2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if server2.Process != nil {
			_ = server2.Process.Kill()
			_, _ = server2.Process.Wait()
		}
	}()

	// Wait for Server 2 socket listener.
	if !testwait.Until(t, func() bool { _, err := os.Stat(sock); return err == nil }) {
		t.Fatalf("timed out waiting for server2 socket %s", sock)
	}

	server1 := exec.Command(os.Args[0], "-test.run=^TestCutoverHelperServer1$", "-test.count=1")
	server1.Env = append(os.Environ(), "ERGO_CUTOVER_SOCK="+sock)
	if out, err := server1.CombinedOutput(); err != nil {
		t.Fatalf("server1 failed: %v\n%s", err, out)
	}
	// Signal Server 2 that Server 1 completely exited!
	if err := os.WriteFile(waitFile, []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := server2.Wait(); err != nil {
		t.Fatalf("server2 failed: %v\n%s", err, s2Out.String())
	}
	server2.Process = nil
}

func TestSubreaperCollectsExactExitStatusAfterParentExits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PR_SET_CHILD_SUBREAPER is Linux-specific")
	}
	if mode := os.Getenv("ERGO_SUBREAPER_CHILD"); mode != "" {
		root := rt.Root("subreaper-spawner")
		env := map[string]string{"PATH": "/bin:/usr/bin"}
		cmdStr := "sleep 0.15; exit 42"
		if mode == "sleep" {
			cmdStr = "sleep 30"
		}
		d1, err := ExecSpawn(root, "sh", []string{"-c", cmdStr}, env, "", "", execDiscard, "", false, execDiscard, "", false)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(d1)
		_, _ = os.Stdout.Write(append(b, '\n'))
		return
	}
	if os.Getenv("ERGO_SUBREAPER_MAIN") == "1" {
		const prSetChildSubreaper = 36
		if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
			t.Fatalf("prctl PR_SET_CHILD_SUBREAPER: %v", errno)
		}
		spawner := exec.Command(os.Args[0], "-test.run=^TestSubreaperCollectsExactExitStatusAfterParentExits$", "-test.count=1")
		spawner.Env = append(os.Environ(), "ERGO_SUBREAPER_CHILD=1")
		out, err := spawner.Output()
		if err != nil {
			t.Fatalf("spawner: %v", err)
		}
		var d ExecDetached
		if err := json.NewDecoder(bytes.NewReader(out)).Decode(&d); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, out)
		}
		// Original parent (spawner) already exited; orphaned child reparented to THIS subreaper!
		kind, st, sig, notChild, reason, err := ExecWaitDetached(rt.Root("subreaper"), d.Pid, d.Start)
		if err != nil {
			t.Fatalf("ExecWaitDetached in subreaper: %v", err)
		}
		if kind != execOutcomeExited || st != 42 || sig != 0 || notChild {
			t.Fatalf("subreaper WaitDetached = (%d, %d, %d, %v, %q), want Exited(42, 0)", kind, st, sig, notChild, reason)
		}

		// Also verify subreaper signal collection when detached child is killed by SIGKILL.
		spawner2 := exec.Command(os.Args[0], "-test.run=^TestSubreaperCollectsExactExitStatusAfterParentExits$", "-test.count=1")
		spawner2.Env = append(os.Environ(), "ERGO_SUBREAPER_CHILD=sleep")
		out2, err := spawner2.Output()
		if err != nil {
			t.Fatalf("spawner2: %v", err)
		}
		var d2 ExecDetached
		if err := json.NewDecoder(bytes.NewReader(out2)).Decode(&d2); err != nil {
			t.Fatalf("unmarshal d2: %v (%s)", err, out2)
		}
		ExecKill(rt.Root("subreaper-kill"), d2.Pid, d2.Start, 0)
		k2, st2, sg2, nc2, r2, e2 := ExecWaitDetached(rt.Root("subreaper2"), d2.Pid, d2.Start)
		if e2 != nil {
			t.Fatalf("subreaper WaitDetached killed: %v", e2)
		}
		if k2 != execOutcomeExited || st2 != 128+int(syscall.SIGKILL) || sg2 != int(syscall.SIGKILL) || nc2 {
			t.Fatalf("subreaper killed WaitDetached = (%d, %d, %d, %v, %q), want Exited(%d, %d)", k2, st2, sg2, nc2, r2, 128+int(syscall.SIGKILL), int(syscall.SIGKILL))
		}
		return
	}

	sub := exec.Command(os.Args[0], "-test.run=^TestSubreaperCollectsExactExitStatusAfterParentExits$", "-test.count=1")
	sub.Env = append(os.Environ(), "ERGO_SUBREAPER_MAIN=1")
	if out, err := sub.CombinedOutput(); err != nil {
		t.Fatalf("subreaper test failed: %v\n%s", err, out)
	}
}

func TestConfinedInteractiveRefusesDetach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sandbox confinement requires Linux user namespaces")
	}
	dir := t.TempDir()
	root := rt.Root("confined-interactive")
	env := map[string]string{"PATH": "/bin:/usr/bin", "HOME": dir, "TMPDIR": dir}
	r, err := ExecStartInteractiveIn(root, dir, "sh", []string{"-c", "cat"}, env, "", "", 100*time.Millisecond, execToCaller, "", false, execDiscard, "", false)
	if err != nil {
		var ef ExecFailed
		if errors.As(err, &ef) && strings.Contains(ef.Reason, "user namespace") {
			t.Skipf("user namespace unavailable: %s", ef.Reason)
		}
		t.Fatalf("ExecStartInteractiveIn: %v", err)
	}
	defer r.Close(root)

	if _, derr := r.Detach(); derr == nil {
		t.Fatal("confined Running.Detach unexpectedly succeeded")
	}
	if err := r.WriteStdin("sandboxed\n"); err != nil {
		t.Fatalf("confined WriteStdin: %v", err)
	}
	if err := r.CloseStdin(); err != nil {
		t.Fatalf("confined CloseStdin: %v", err)
	}
	var got strings.Builder
	for l := range r.Lines(root) {
		got.WriteString(l.Text)
	}
	if st := r.Wait(root); st != 0 {
		t.Fatalf("confined Wait status = %d, want 0", st)
	}
	if got.String() != "sandboxed" {
		t.Fatalf("confined output = %q, want %q", got.String(), "sandboxed")
	}
}

package exec

import (
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

func TestMemSessionsPTYDetachReattachAndWait(t *testing.T) {
	root := rt.Root("mem-sessions")
	m := ExecNewMemSessions([]string{"cat", "worker"})

	pty, err := m.OpenPTY()
	if err != nil {
		t.Fatalf("OpenPTY: %v", err)
	}
	defer pty.Close()

	ws := ExecWinsize{Rows: 50, Cols: 140}
	if err := pty.Resize(ws); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	r, err := m.StartWithPTY("cat", "", pty)
	if err != nil {
		t.Fatalf("StartWithPTY: %v", err)
	}
	dr, err := r.Detach()
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	info := dr.IntoDetached()
	dup, err := pty.DupFD()
	if err != nil {
		t.Fatalf("DupFD: %v", err)
	}
	pty.Close()

	pty2, err := m.ReattachPTY(dup)
	if err != nil {
		t.Fatalf("ReattachPTY: %v", err)
	}
	defer pty2.Close()

	if got, err := pty2.Size(); err != nil || got != ws {
		t.Fatalf("reattached Size = (%+v, %v), want %+v", got, err, ws)
	}
	if err := pty2.Write("ping"); err != nil {
		t.Fatalf("pty2.Write: %v", err)
	}
	if got, err := pty2.Read(root); err != nil || got != "ping" {
		t.Fatalf("pty2.Read = (%q, %v), want ping", got, err)
	}

	m.Complete(info.Pid, info.Start, 17, 0)
	kind, st, sig, notChild, reason := m.PollDetached(info.Pid, info.Start)
	if kind != execOutcomeExited || st != 17 || sig != 0 || notChild {
		t.Fatalf("PollDetached = (%d, %d, %d, %v, %q), want Exited(17, 0)", kind, st, sig, notChild, reason)
	}

	// Verify WaitDetached unblocks on task stop and completes when masked.
	d2, err := m.Spawn("worker", execDiscard, execDiscard)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	wt := rt.Root("mem-wait-stop")
	waiting := make(chan struct{})
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		cancel := rt.WhenWaiting(wt, func() { close(waiting) })
		defer cancel()
		_, _, _, _, _ = ExecMemWaitDetached(wt, m, d2.Pid, d2.Start)
		done <- nil
	}()
	select {
	case <-waiting:
	case <-testwait.After(t):
		t.Fatal("MemSessions.WaitDetached did not reach wait")
	}
	wt.Interrupt()
	select {
	case res := <-done:
		if res != rt.Interrupted {
			t.Fatalf("MemSessions.WaitDetached after Interrupt = %v, want rt.Interrupted", res)
		}
	case <-testwait.After(t):
		t.Fatal("MemSessions.WaitDetached did not unblock on task stop")
	}
}

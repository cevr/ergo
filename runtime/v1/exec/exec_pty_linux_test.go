//go:build linux

package exec

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

func TestSubreaperCollectsExactExitStatusAfterParentExits(t *testing.T) {
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

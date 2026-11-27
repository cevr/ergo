//go:build unix

package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
)

func TestStdSignalContractAndHostParity(t *testing.T) {
	pkgs, outside := stdSurface(t)
	var sigPkg *check.Package
	for _, p := range pkgs {
		if p.Name == "signal" {
			sigPkg = p
			break
		}
	}
	if sigPkg == nil {
		t.Fatal("signal package missing from stdSurface")
	}

	var stopType *check.TypeName
	for _, tn := range sigPkg.Types {
		if tn.Name == "Stop" {
			stopType = tn
			break
		}
	}
	if stopType == nil {
		t.Fatal("signal.Stop type missing")
	}
	u, ok := stopType.Type.(*check.Named).Underlying().(*check.Union)
	if !ok {
		t.Fatalf("signal.Stop underlying = %T; want *check.Union", stopType.Type.(*check.Named).Underlying())
	}
	var variants []string
	for _, v := range u.Variants {
		n, ok := v.(*check.Named)
		if !ok {
			t.Fatalf("signal.Stop variant = %T; want *check.Named", v)
		}
		variants = append(variants, n.Obj.Name)
	}
	wantVariants := []string{"Interrupt", "Terminate", "Hangup", "User1", "User2"}
	if !slices.Equal(variants, wantVariants) {
		t.Errorf("signal.Stop variants = %v; want %v", variants, wantVariants)
	}

	var statusOf *check.Func
	for _, f := range sigPkg.Funcs {
		if f.Name == "StatusOf" {
			statusOf = f
			break
		}
	}
	if statusOf == nil || statusOf.Sig == nil {
		t.Fatal("signal.StatusOf function missing")
	}
	if len(statusOf.Sig.Params) != 1 || len(statusOf.Sig.Results) != 1 {
		t.Fatalf("signal.StatusOf signature = %+v; want 1 param, 1 result", statusOf.Sig)
	}
	if !statusOf.Sig.Uses.Empty() || !statusOf.Sig.Errors.Empty() || statusOf.Sig.Fallible {
		t.Errorf("signal.StatusOf has unexpected effects uses=%v errors=%v fallible=%v", statusOf.Sig.Uses, statusOf.Sig.Errors, statusOf.Sig.Fallible)
	}

	held := heldTypes(pkgs, outside)
	for _, v := range externCallerViolations([]*check.Package{sigPkg}, held, hostExtern) {
		t.Errorf("authority violation in signal: %s", v)
	}

	var laws []string
	for _, l := range sigPkg.Laws {
		laws = append(laws, l.Name)
	}
	for _, wantLaw := range []string{"RaisedInOrder", "PollTakesOnce", "QuietRunsBody", "StopEndsBody", "StatusIsTheShells"} {
		if !slices.Contains(laws, wantLaw) {
			t.Errorf("signal missing law %s (have %v)", wantLaw, laws)
		}
	}

	for _, tc := range []struct {
		code int
		sig  syscall.Signal
	}{
		{1, syscall.SIGINT},
		{2, syscall.SIGTERM},
		{3, syscall.SIGHUP},
		{4, syscall.SIGUSR1},
		{5, syscall.SIGUSR2},
	} {
		got := v1.SignalNumber(tc.code)
		if got != int(tc.sig) {
			t.Errorf("v1.SignalNumber(%d) = %d; want %d (%v)", tc.code, got, int(tc.sig), tc.sig)
		}
		exitStatus := 128 + got
		if exitStatus < 129 || exitStatus > 192 {
			t.Errorf("v1.SignalNumber(%d) exit status %d outside os.Code signal range 129..192", tc.code, exitStatus)
		}
	}
}

func TestStdSignalLawsNative(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: compiles and runs std:signal laws natively")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ergo")
	build := ergobuild.Command(bin, "./cmd/ergo", "-p", "2")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	run := exec.Command(bin, "test", "std:signal", "--seed", "1")
	run.Dir = root
	run.Env = append(os.Environ(), "ERGO_FALLBACK=off", "ERGO_CACHE="+filepath.Join(tmp, "cache"))
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("ergo test std:signal failed: %v\n%s", err, out)
	}
	text := string(out)
	for _, wantLaw := range []string{"RaisedInOrder", "PollTakesOnce", "QuietRunsBody", "StopEndsBody", "StatusIsTheShells"} {
		if !strings.Contains(text, "ok law "+wantLaw) {
			t.Errorf("missing passing line for %s in:\n%s", wantLaw, text)
		}
	}
}

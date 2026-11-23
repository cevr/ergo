package irspike

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/testwait"
)

// TestIRCopiesMatchSource is the seeded property over the value rule's
// copies (F7 "value copy", GenerateCopy): each program prints the same
// under both lowerings, every copying function has an IR body, and the IR
// text calls each Go form of the copy. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because a copy left out shows only when the compiled
// program's append or Put writes the caller's array or map.
func TestIRCopiesMatchSource(t *testing.T) {
	// No ergoCloneMap: under the entry rule (V8) a Map of slices whose
	// entries are not written copies by maps.Clone.
	lyProperty(t, "TestIRCopiesMatchSource", GenerateCopy, []string{"slices.Clone", "maps.Clone", lower.HelperSpelling("", "ergoCloneSlice"), lower.HelperSpelling("", "ergoCloneBox")},
		"copySlice", "copyMap", "takeMap", "copyBoxes", "copyLists", "Box.copyBox", "copyAny")
}

// lyProperty runs the programs gen draws at ERGO_IRSPIKE_SEEDS seeds from
// ERGO_IRSPIKE_SEED under both lowerings: each prints the same, each
// function of funcs has an IR body, and the IR calls each of texts.
// A name in both lists can name a suffixed instance: its body is checked
// under the full name from the call. Callers state why they build Go.
func lyProperty(t *testing.T, name string, gen func(uint64) string, texts []string, funcs ...string) {
	t.Helper()
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := gen(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run " + name + " ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := lyRun(t, sb, replay, text), lyRun(t, ib, replay, text)
		if a != b {
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		got := map[string]bool{}
		for _, f := range rep.IR {
			got[f] = true
		}
		var calls map[string]bool
		if len(texts) > 0 {
			calls = lyIRCalls(t, filepath.Join(dir, "ir", lower.IRFileName))
		}
		for _, f := range funcs {
			found := got[f]
			if !found && calls[f] {
				for _, body := range rep.IR {
					if lyCallBase(body) == f && calls[body] {
						found = true
						break
					}
				}
			}
			if !found {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v, fallback %v)\n%s\n%s", seed, f, rep.IR, rep.Fallback, replay, text)
			}
		}
		for _, s := range texts {
			if !calls[s] {
				t.Errorf("seed %d: the IR calls no %s (it calls %v)\n%s\n%s", seed, s, calls, replay, text)
			}
		}
	}
}

// lyRun is run, in a process group of its own, bounded by the test's
// deadline (testwait): a binary still running then is killed with its
// group and named as hung, with the program and its replay command. The
// deadline only stops a hung run; a run that ends passes at any speed.
// GenerateKnownCallback's program ends only when its stops end the loops
// they interrupt (LY-POLL), so a loop that no longer polls hangs here.
func lyRun(t *testing.T, bin, replay, text string) outcome {
	t.Helper()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	_, err := MeasureBounded(cmd, programMemory)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // anything the program left
	}
	if ctx.Err() != nil {
		t.Fatalf("%s hung until the test's deadline (killed): a stop did not end what it interrupts (stdout so far %q)\n%s\n%s", filepath.Base(bin), out.String(), replay, text)
	}
	o := outcome{stdout: out.String(), stderr: firstLine(errb.String())}
	if ee, ok := err.(*exec.ExitError); ok {
		o.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return o
}

// lyIRCalls lists exact IR callees and their forms without a uniqueness
// suffix, and "pkg.Name" and "Name" for Go package calls.
func lyIRCalls(t *testing.T, file string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			name, _ := v["name"].(string)
			switch v["k"] {
			case "call":
				out[name] = true
				out[lyCallBase(name)] = true
			case "pcall":
				pkg, _ := v["t"].(string)
				out[pkg+"."+name] = true
				out[name] = true // a runtime/v1 body, by its name alone
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}

func lyCallBase(name string) string {
	return strings.TrimRightFunc(name, func(r rune) bool { return r == '_' || r >= '0' && r <= '9' })
}

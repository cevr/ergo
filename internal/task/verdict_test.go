package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// verdictModel is a workspace of files (path -> content) and steps over
// them: step i reads files[i] and passes while none of them holds "bad";
// a flaky step fails its first run of each run and passes its retry.
type verdictModel struct {
	files map[string]string
	reads [][]string
	flaky []bool
	// runs counts each step's runs in the current Verdicts call.
	runs []int
}

func (m *verdictModel) steps() []Step {
	var out []Step
	for i := range m.reads {
		out = append(out, Step{
			Task: "gate", Verb: fmt.Sprintf("v%d", i), Member: ".", Class: Derived,
			Retry: m.flaky[i],
			Key: func(context.Context) (Key, error) {
				var k Key
				for _, f := range m.reads[i] {
					sum := sha256.Sum256([]byte(m.files[f]))
					k.Parts = append(k.Parts, Part{Name: f, Digest: hex.EncodeToString(sum[:])})
					k.Paths = append(k.Paths, f)
				}
				return k, nil
			},
			Run: func(context.Context) (bool, []byte, error) {
				m.runs[i]++
				if m.flaky[i] && m.runs[i] == 1 {
					return false, []byte("failed at seed 42"), nil
				}
				return m.pass(i), []byte("ran"), nil
			},
		})
	}
	return out
}

func (m *verdictModel) pass(i int) bool {
	for _, f := range m.reads[i] {
		if strings.Contains(m.files[f], "bad") {
			return false
		}
	}
	return true
}

// TestVerdictsAgreeWithRunningEverything: over seeded steps reading
// seeded files and seeded edit scripts, each run's verdicts equal the
// reference "run every step" (a flaky step passes a plain run and fails a
// --fresh one); the steps that run are exactly those with no kept pass for
// their inputs now (the reference keeps a pass by step and inputs, never
// a failure or a flaky pass); --affected selects exactly the steps
// reading a changed file; and a --fresh run of a kept pass whose step now
// fails is a contradiction. It kills verdict-flaky-kept,
// verdict-failure-kept, verdict-affected-all and verdict-why-dropped.
func TestVerdictsAgreeWithRunningEverything(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		nf, ns := 2+r.IntN(5), 2+r.IntN(5)
		m := &verdictModel{files: map[string]string{}}
		for f := range nf {
			m.files[fmt.Sprintf("f%d", f)] = "ok"
		}
		for range ns {
			var reads []string
			for f := range nf {
				if r.IntN(3) == 0 {
					reads = append(reads, fmt.Sprintf("f%d", f))
				}
			}
			m.reads = append(m.reads, reads)
			m.flaky = append(m.flaky, r.IntN(4) == 0)
		}
		store := lawcache.Memory(StoreIdentity)
		runner := &Runner{Root: "/model", Store: store, Logs: t.TempDir(), Flaky: filepath.Join(t.TempDir(), "flaky.txt")}
		kept := map[string]bool{} // the reference's kept passes, by step and inputs
		refKey := func(i int) string {
			k := fmt.Sprint(i)
			for _, f := range m.reads[i] {
				k += "|" + f + "=" + m.files[f]
			}
			return k
		}
		var script []string
		for step := range 8 {
			// An edit: a file goes bad, comes back, or changes harmlessly.
			changed := []string{}
			if step > 0 {
				f := fmt.Sprintf("f%d", r.IntN(nf))
				switch r.IntN(3) {
				case 0:
					m.files[f] = "bad"
				case 1:
					m.files[f] = "ok"
				default:
					m.files[f] = fmt.Sprintf("ok %d", r.IntN(3))
				}
				changed = append(changed, f)
				script = append(script, f+"="+m.files[f])
			}
			fresh := r.IntN(5) == 0
			affected := !fresh && r.IntN(3) == 0
			m.runs = make([]int, ns)
			rep, err := runner.Verdicts(context.Background(), m.steps(), VerdictMode{Fresh: fresh, Affected: affected, Changed: changed})
			if err != nil {
				t.Fatal(err)
			}
			where := fmt.Sprintf("seed %d, reads %v, flaky %v, edits %v, fresh %v, affected %v", seed, m.reads, m.flaky, script, fresh, affected)
			for i, res := range rep.Steps {
				reads := false
				for _, f := range m.reads[i] {
					reads = reads || slices.Contains(changed, f)
				}
				if affected && !reads {
					if res.Verdict != Unaffected || m.runs[i] != 0 {
						t.Fatalf("%s: step %d reads no changed file but was %s and ran %d times", where, i, res.Verdict, m.runs[i])
					}
					continue
				}
				wantRun := fresh || !kept[refKey(i)]
				if (m.runs[i] > 0) != wantRun {
					t.Fatalf("%s: step %d ran %d times, want a run %v (kept passes %v)", where, i, m.runs[i], wantRun, kept)
				}
				if wantRun && res.Why == "" {
					t.Fatalf("%s: step %d missed with no reason", where, i)
				}
				want := Fail
				if m.pass(i) {
					want = Pass
					if m.flaky[i] && m.runs[i] > 0 {
						want = Flaky
					}
				}
				if res.Verdict != want {
					t.Fatalf("%s: step %d is %s, want %s", where, i, res.Verdict, want)
				}
				if want == Flaky && (res.Seed != "42" || res.Log == "") {
					t.Fatalf("%s: flaky step %d names seed %q and log %q", where, i, res.Seed, res.Log)
				}
				if fresh && kept[refKey(i)] && want == Fail != res.Contradicted {
					t.Fatalf("%s: step %d kept a pass and fresh is %s: contradicted %v", where, i, want, res.Contradicted)
				}
				if want == Pass {
					kept[refKey(i)] = true
				}
			}
			wantFail := false
			for _, res := range rep.Steps {
				wantFail = wantFail || res.Verdict == Fail || fresh && res.Verdict == Flaky || res.Contradicted
			}
			if rep.Failed(fresh) != wantFail {
				t.Fatalf("%s: the run failed %v, want %v", where, rep.Failed(fresh), wantFail)
			}
		}
	}
}

// TestVerdictsRunAStepOnce: two tasks that plan one step run it once,
// and each reports it.
func TestVerdictsRunAStepOnce(t *testing.T) {
	m := &verdictModel{files: map[string]string{"f": "ok"}, reads: [][]string{{"f"}}, flaky: []bool{false}, runs: []int{0}}
	steps := m.steps()
	second := steps[0]
	second.Task = "ci"
	r := &Runner{Root: "/model", Store: lawcache.Memory(StoreIdentity)}
	rep, err := r.Verdicts(context.Background(), append(steps, second), VerdictMode{})
	if err != nil {
		t.Fatal(err)
	}
	if m.runs[0] != 1 || len(rep.Steps) != 2 || rep.Steps[1].Task != "ci" || rep.Steps[1].Verdict != Pass {
		t.Errorf("runs %d, steps %+v", m.runs[0], rep.Steps)
	}
}

// TestStatedEnvIsTheWholeEnvironment: a child started through command
// sees exactly the stated environment, and a host variable no env line
// names never reaches it nor its key; a named one does, and its value
// moves the key. The command is found only on the child's PATH.
// It kills child-env-inherits and child-env-host-path.
func TestStatedEnvIsTheWholeEnvironment(t *testing.T) {
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env program")
	}
	dir := t.TempDir()
	const child = "ergo-task-env-fixture"
	if err := os.Symlink(env, filepath.Join(dir, child)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	host := []string{"PATH=" + dir, "HOME=/h", "GOFLAGS=-mod=mod", "SECRET_NOT_NAMED=1", "LANG=C"}
	stated := statedFrom(host, []string{"LANG", "MISSING"}, []string{"GOMAXPROCS=2"})
	t.Setenv("ERGO_PARENT_ONLY", "x")
	cmd, err := command(context.Background(), stated, "", child)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	slices.Sort(got)
	want := []string{"GOFLAGS=-mod=mod", "GOMAXPROCS=2", "HOME=/h", "LANG=C", "PATH=" + dir}
	if !slices.Equal(got, want) {
		t.Errorf("the child saw %v, want exactly %v", got, want)
	}
	if strings.Contains(stated.Key(), "SECRET") || !strings.Contains(stated.Key(), "unset MISSING") {
		t.Errorf("key %q: an unnamed variable is in it, or a missing named one is not", stated.Key())
	}
	other := statedFrom(append(host[:4:4], "LANG=fr"), []string{"LANG", "MISSING"}, []string{"GOMAXPROCS=2"})
	if other.Key() == stated.Key() {
		t.Error("a named variable's value is not in the key")
	}
}

// TestChildrenStartInOnePlace: the runner starts a child only through
// command (child.go), so every child's environment is the stated one.
func TestChildrenStartInOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "child.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"exec.Command(", "exec.CommandContext(", "os.StartProcess(", "os.Environ()"} {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s calls %s: start children through command (child.go)", f, bad)
			}
		}
	}
}

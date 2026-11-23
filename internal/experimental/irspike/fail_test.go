package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFailuresMatchSource is IRG2's agreement property: for generated
// failure programs (GenerateFail), the IR path and the source path print
// the same, exit the same and fail with the same message. Each run must
// reach every failure-ABI IR kind and give IR bodies to fallible
// functions. A failing seed prints the Ergo source, both outcomes and its
// replay command. It builds Go for the reason needsBuild gives.
func TestIRFailuresMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 16)
	seen := map[string]bool{}
	fallibleIR, defects, polled, tailIR := 0, 0, 0, 0
	var flat flatReach
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateFail(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRFailuresMatchSource ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
		// gate's wrapper failed with its worker's failure (gateCalls): a
		// run where it did not reached no wrapper's failure, so a wrapper
		// that drops it (irf-wrapper-drops-failure) would pass unseen.
		if !strings.Contains(a.stdout, gateFailed) {
			t.Fatalf("seed %d: the generator's call of gate through a function value printed no %q: no wrapper failed\n%s", seed, gateFailed, text)
		}
		if a.exit != 0 {
			defects++
		}
		// main's total catches: each default is the unreachable defect,
		// so a failure no arm reads does not escape (FAIL-ELIDE).
		if lower.IRCatchDefectOn() && rep.CatchDefects == 0 {
			t.Errorf("seed %d: no full catch's default named the unreachable defect\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRFailuresMatchSource$' ./internal/experimental/irspike", seed, seed)
		}
		irKinds(t, filepath.Join(dir, "ir", lower.IRFileName), seen)
		polled += backEdgePolls(t, filepath.Join(dir, "ir", lower.IRFileName))
		// Each program reaches the flat convention (IRF-2) on its own: a
		// function with a worker, a catch that reads a worker's failure
		// and a wrapper's call (gate's, gateCalls), whatever the seed
		// count. A program without one is a generator error.
		var own flatReach
		own.add(t, filepath.Join(dir, "ir", lower.IRFileName))
		if lower.FlatOn() && (own.workers == 0 || own.caught == 0 || own.wrapped == 0) {
			t.Fatalf("seed %d: the program did not reach the flat convention (IRF-2): workers %d, catches that read a worker's failure %d, wrappers called %d\n%s", seed, own.workers, own.caught, own.wrapped, text)
		}
		flat.chained += own.chained
		for _, name := range rep.IR {
			if strings.Contains(text, "func "+name+"(") && fallibleHead(text, name) {
				fallibleIR++
			}
			if errorTail(text, name) {
				tailIR++
			}
		}
	}
	if count < 16 {
		return // a replay: the coverage needs the whole run
	}
	var missing []string
	for _, k := range []string{"fail", "def2", "nil", "zero", "tswitch", "assert", "panic", "mcall", "while", "range"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || fallibleIR == 0 || defects == 0 || polled == 0 || tailIR == 0 {
		t.Fatalf("the run did not reach: kinds %v, fallible IR bodies %d, failing seeds %d, loops ending in their poll %d, IR bodies with no value ending in an error value %d", missing, fallibleIR, defects, polled, tailIR)
	}
	if lower.FlatOn() && flat.chained == 0 {
		t.Fatalf("the run did not reach the flat convention (IRF-2): no worker tries a worker")
	}
}

// flatReach counts, in an IR file, the flat convention's forms (IR-FORMS
// IRF-2): a worker whose body tries another worker (a failure through two
// tries), a catch that binds a worker's failure to read it, and a call of
// a wrapper (a flat function called with Go's error); workers counts the
// functions that have a worker.
type flatReach struct{ workers, chained, caught, wrapped int }

func (r *flatReach) add(t *testing.T, file string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Funcs []struct {
			Name string
			Body any
		}
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	workers := map[string]bool{}
	for _, f := range m.Funcs {
		if strings.HasSuffix(f.Name, "Flat") {
			workers[f.Name] = true
		}
	}
	r.workers += len(workers)
	isWorker := func(x any) bool {
		c, _ := x.(map[string]any)
		name, _ := c["name"].(string)
		return c["k"] == "call" && workers[name]
	}
	for _, f := range m.Funcs {
		worker := workers[f.Name]
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if v["k"] == "call" {
					if name, _ := v["name"].(string); workers[name+"Flat"] && f.Name != name {
						r.wrapped++
					}
				}
				if v["k"] == "if" && isWorker(v["x"]) {
					then, _ := v["then"].([]any)
					first, _ := firstOf(then).(map[string]any)
					x, _ := first["x"].(map[string]any)
					switch {
					case worker:
						r.chained++
					case first["k"] == "def" && x["k"] == "field" && x["name"] == "Err":
						r.caught++
					}
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
		walk(f.Body)
	}
}

func firstOf(list []any) any {
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

// errorTail reports that name's body in text has no value and ends in an
// error value (GenerateFail's unit tail), which fails the function.
func errorTail(text, name string) bool {
	_, rest, ok := strings.Cut(text, "func "+name+"(")
	if !ok {
		return false
	}
	head, body, _ := strings.Cut(rest, "{\n")
	body, _, _ = strings.Cut(body, "\n}\n")
	lines := strings.Split(body, "\n")
	lastLine := lines[len(lines)-1]
	return strings.Contains(head, ") fails ") && (strings.HasPrefix(lastLine, "\tE1{") || strings.HasPrefix(lastLine, "\tE2{"))
}

// backEdgePolls counts the loops of an IR file whose body ends in the
// task's Check: the back-edge poll of a polling frame (runtime.md, I1).
// No output shows a poll (it acts only on a stop), so the property counts
// the one promised fact of its placement.
func backEdgePolls(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	n := 0
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k := v["k"]; k == "while" || k == "range" {
				if body, _ := v["body"].([]any); len(body) > 0 && isPoll(body[len(body)-1]) {
					n++ // isPoll (f7g_test.go): a Check, or a quiet loop's amortized poll
				}
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
	return n
}

// fallibleHead reports that name's declaration in text has a fails row.
func fallibleHead(text, name string) bool {
	_, rest, ok := strings.Cut(text, "func "+name+"(")
	head, _, _ := strings.Cut(rest, "{")
	return ok && strings.Contains(head, " fails ")
}

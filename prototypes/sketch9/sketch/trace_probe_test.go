package sketch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// probeProgram is a hand-written kernel program, the second review's
// equal-failure probes (round-3 M1): two failures of calls.Missing enter one
// rt.Cause in order, either from two traced calls of calls.Store.Get or from
// two children's Waits. In the "untraced" mode (the fourth review's m1) the
// body adds both failures itself, and no traced point raises them. It
// records the run with the real recorder and writer and prints the
// occurrence each reason came from, as the program saw it (0 when no event
// raised it). mk(i) makes the i-th failure. The definitions and sites are
// the checker's, filled in by the test, and the imports name KernelModule.
const probeProgram = `package main

import (
	"fmt"
	"os"

	"%[1]s/runtime/rt"
	"%[1]s/runtime/rt/rttrace"
)

%s

func (Missing) Error() string { return "Missing" }

const (
	mainDef  = %q
	mainSite = %q
	getDef   = %q
)

var getSites = [2]string{%q, %q}

func main() {
	mode, path := os.Args[1], os.Args[2]
	rec := rttrace.New(64)
	root := rt.TracedRoot(mainDef, rec)
	var from [2]uint64
	var tasks [2]uint64
	_, err := rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
		var c rt.Cause
		if mode == "untraced" {
			c.Add(mk(0))
			c.Add(mk(1))
			return struct{}{}, c.Err()
		}
		if mode == "calls" {
			for i, site := range getSites {
				call := t0.TraceCall(getDef, site)
				e := mk(i)
				t0.TraceCallExit(call, getDef, site, e)
				recs, _ := rec.Records()
				for _, r := range recs {
					if r.Kind == rt.TraceCallExit && r.Link == call {
						from[i] = r.ID
					}
				}
				c.Add(e)
			}
			return struct{}{}, c.Err()
		}
		return rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
			var hs [2]*rt.Handle[struct{}]
			for i := range hs {
				hs[i] = rt.Go(s, mainDef, func(ct *rt.Task) (struct{}, error) {
					tasks[i] = ct.ID()
					return struct{}{}, mk(i)
				})
			}
			for _, h := range hs {
				_, e := h.Wait()
				c.Add(e)
			}
			return struct{}{}, c.Err()
		})
	})
	if mode == "children" {
		recs, _ := rec.Records()
		for i, task := range tasks {
			for _, r := range recs {
				if r.Kind == rt.TraceBodyExit && r.Task == task {
					from[i] = r.ID
				}
			}
		}
	}
	if err := rttrace.WriteFile(path, rec, %q, "trace-probe", map[string]string{mainDef: mainSite}); err != nil {
		panic(err)
	}
	fmt.Println(from[0], from[1], rt.ExitCode(err) != 0)
}
`

// The third review's equal-failure probes, through the real kernel,
// recorder, writer and query: no reason names another reason's occurrence.
// Equal zero-size failures cannot be told apart, so the answer states no
// origin and lists the occurrences as candidates by type; failures stored
// on their own keep their identity, and each reason names its own
// occurrence. The recording names failures by type only, so the answer
// never calls two reasons equal: two distinct values that no traced point
// raised get the same answer as equal ones (the fourth review's m1).
func TestKernelEqualFailureProbes(t *testing.T) {
	root := kernelRoot(t)
	c, src := loadFile(t, "../testdata/trace/two-calls.sketch")
	m := NewModel(c)
	var sites []string
	for _, p := range m.Points {
		if p.Kind == "call" && p.Def == "calls.Store.Get" {
			sites = append(sites, p.Site)
		}
	}
	main, ok := m.Def("calls.main")
	if len(sites) != 2 || !ok {
		t.Fatalf("model: Store.Get sites %v, main %v", sites, ok)
	}
	for _, v := range []struct {
		name, typ, mk string
		linked        bool
	}{
		{"zero-size", "type Missing struct{}", "func mk(int) error { return Missing{} }", false},
		{"stored", "type Missing struct{ key string }", "func mk(int) error { return Missing{key: string([]byte(\"k\"))} }", true},
		{"distinct", "type Missing struct{ key string }", "func mk(i int) error { return Missing{key: fmt.Sprint(\"value-\", i)} }", true},
	} {
		dir := t.TempDir()
		code := fmt.Sprintf(probeProgram, KernelModule, v.typ+"\n\n"+v.mk, "calls.main", main.Site, "calls.Store.Get", sites[0], sites[1], Revision(src))
		for name, body := range map[string]string{"go.mod": KernelGoMod(root), "main.go": code} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		bin := filepath.Join(dir, "probe.bin")
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Dir = dir
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("%s: go build: %v\n%s", v.name, err, out)
		}
		for _, mode := range []string{"calls", "children", "untraced"} {
			name := v.name + "/" + mode
			path := filepath.Join(dir, mode+".jsonl")
			out, err := exec.Command(bin, mode, path).Output()
			if err != nil {
				t.Fatalf("%s: run: %v", name, err)
			}
			var from [2]uint64
			var failed bool
			raised := mode != "untraced" // a traced point raised each failure
			if _, err := fmt.Sscan(string(out), &from[0], &from[1], &failed); err != nil || !failed || raised != (from[0] != 0) || raised != (from[1] != 0) {
				t.Fatalf("%s: program printed %q (%v)", name, out, err)
			}
			linked := v.linked && raised
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ev, err := QueryTrace(m, Revision(src), data)
			if err != nil {
				t.Fatalf("%s: refused: %v", name, err)
			}
			if ev.Root == nil || len(ev.Root.Exit) != 2 {
				t.Fatalf("%s: root exit %+v; want two reasons", name, ev.Root)
			}
			for i, r := range ev.Root.Exit {
				if r.Origin != 0 && r.Origin != from[i] {
					t.Fatalf("%s: reason %d came from occurrence %d; its recorded origin %d belongs to another reason", name, i, from[i], r.Origin)
				}
				if linked != (r.Origin != 0 && r.Origin == from[i]) {
					t.Fatalf("%s: reason %d origin %d, source %d; want linked=%v", name, i, r.Origin, from[i], linked)
				}
			}
			text := strings.Join(ev.CauseTrace, "\n")
			// The recording names failures by type only: the answer never
			// claims the values were equal.
			if strings.Contains(text, "equal") {
				t.Errorf("%s: cause trace claims equality the recording does not hold:\n%s", name, text)
			}
			if linked {
				continue
			}
			want := "origin not recorded: 2 reasons of this exit have the same error type calls.Missing, and no event links this one"
			if strings.Count(text, want) != 2 {
				t.Errorf("%s: cause trace lacks the shared type, twice:\n%s", name, text)
			}
			if !raised {
				if strings.Count(text, "no candidate in the trace") != 2 {
					t.Errorf("%s: cause trace names a candidate for failures no event raised:\n%s", name, text)
				}
				continue
			}
			for _, id := range from {
				seq := ev.Events[slices.IndexFunc(ev.Events, func(e TraceEvent) bool { return e.ID == id })].Seq
				if !strings.Contains(text, "candidate by type only: ") || !strings.Contains(text, "seq "+strconv.FormatUint(seq, 10)+")") {
					t.Errorf("%s: cause trace does not name occurrence %d (seq %d) as a candidate:\n%s", name, id, seq, text)
				}
			}
		}
	}
}

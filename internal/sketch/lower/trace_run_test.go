package lower

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/machinegraph"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestErgoRunTracesMachineArms runs listen.ergo through the CLI with
// ERGO_TRACE (MU15; runtime.md, Machines and actors 15): each decide line
// of the trace names the arm that the machine's graph, read from the same
// source, gives its state and signal; and each work started ends (returned,
// failed or interrupted) in the same entry. It builds Go because the
// arms the runner names are the ones the lowering wrote into the program
// (v1.RegisterMachine), which only the lowered program holds.
func TestErgoRunTracesMachineArms(t *testing.T) {
	needsBuild(t)
	src, err := os.ReadFile(filepath.Join("testdata", "work", "listen.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("listen.ergo: %v", diags)
	}
	graphs := machinegraph.Machines(pkg, func(_ syntax.Node, pos syntax.Pos) machinegraph.Site {
		return machinegraph.Site{File: "main.ergo", Line: file.Line(pos)}
	})
	if len(graphs) != 1 {
		t.Fatalf("%d machines", len(graphs))
	}
	g := graphs[0]

	dir := t.TempDir()
	path := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace.jsonl")
	cmd := exec.Command(ergoCLI(t), "run", path)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ERGO_TRACE="+trace)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "ticks 9\nunavailable\n" {
		t.Fatalf("ergo run: %v\n%s", err, out)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	decides, works := 0, map[string]int{}
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n")[1:] {
		var l struct{ Machine string }
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatal(err)
		}
		f := fields(l.Machine)
		switch f["what"] {
		case "decide":
			decides++
			arm, ok := g.ArmOf(f["state"], f["signal"])
			if !ok || !strings.HasSuffix(f["arm"], ":"+strconv.Itoa(arm.Site.Line)) {
				t.Errorf("%s: the graph's arm of (%s, %s) is %v", l.Machine, f["state"], f["signal"], arm.Site)
			}
		case "work-started":
			works[f["epoch"]]++
		case "work-ended", "work-failed", "work-interrupted":
			works[f["epoch"]]--
		}
	}
	// Two actors run, one after the other, so an epoch's count is 0 when
	// every work started in it ended.
	for epoch, n := range works {
		if n != 0 {
			t.Errorf("entry %s: %d works started and not ended", epoch, n)
		}
	}
	if decides == 0 || len(works) == 0 {
		t.Fatalf("decides %d, works %d:\n%s", decides, len(works), data)
	}
}

// fields reads a machine line, "decide epoch=1 state=A …", into its
// fields, the event's name under "what".
func fields(line string) map[string]string {
	out := map[string]string{}
	for i, w := range strings.Fields(line) {
		if i == 0 {
			out["what"] = w
			continue
		}
		if k, v, ok := strings.Cut(w, "="); ok {
			out[k] = v
		}
	}
	return out
}

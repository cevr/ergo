package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInspectMachineJSON is the smallest repro of a fixed bug: `ergo
// inspect machine --json`, documented in tooling.md and the command's help,
// was refused by the option parser ("--json is only supported by
// diagnostics, run and test"; MX-P3 finding F5). It checks that the JSON
// carries the graph and the reachable pairs the text form prints, timer
// facts included. It checks in process; no generated Go needs to run.
func TestInspectMachineJSON(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m.ergo")
	const body = `type Phase = PA{} | PB{}

type Due {}

func swap(s Phase, e Due) Decision[Phase, int] {
	match s {
	case PA:
		Enter[Phase]{Next: PB}
	case PB:
		Enter[Phase]{Next: PA}
	}
}

func second(s Phase) Option[Timeout[Due]] {
	Some[Timeout[Due]]{Value: Timeout[Due]{In: 1s, Signal: Due}}
}

type M machine[Phase, Due] { initial: PA, step: swap, after: second }
`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := func(args ...string) string {
		t.Helper()
		f, err := os.CreateTemp(dir, "stdout-")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		saved := os.Stdout
		os.Stdout = f
		err = inspectCommand(args)
		os.Stdout = saved
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var graphs []struct {
		Machine   string
		Initial   string
		Timers    map[string]struct{ Signal string }
		Reachable []struct{ State, Signal string }
	}
	out := capture("machine", src, "--json")
	if err := json.Unmarshal([]byte(out), &graphs); err != nil || len(graphs) != 1 {
		t.Fatalf("json: %v\n%s", err, out)
	}
	g := graphs[0]
	if g.Machine != "M" || g.Initial != "PA" || g.Timers["PA"].Signal != "Due" || g.Timers["PB"].Signal != "Due" {
		t.Fatalf("graph %+v\n%s", g, out)
	}
	var fromJSON, fromText []string
	for _, p := range g.Reachable {
		fromJSON = append(fromJSON, fmt.Sprintf("(%s, %s)", p.State, p.Signal))
	}
	for _, line := range strings.Split(capture("machine", src), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "reach "); ok {
			pair, _, _ := strings.Cut(rest, " by ")
			fromText = append(fromText, pair)
		}
	}
	if len(fromText) == 0 || !slices.Equal(fromJSON, fromText) {
		t.Fatalf("reachable pairs: json %v, text %v", fromJSON, fromText)
	}
}

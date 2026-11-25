package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeModelsConform runs std explore.Conform (via conformCommand)
// directly against models/children (Design) and models/baton (Design), proving
// that valid runtime traces conform (exit 0), invariant/precondition-violating
// traces produce a mismatch (exit 20), and events the model has no signal for
// are rejected as trace errors (exit 21), never silently ignored.
func TestRuntimeModelsConform(t *testing.T) {
	t.Setenv("ERGO_LAWCACHE", "off")
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	runCase := func(t *testing.T, modelDir, machine, trace string, wantCode int, wantSubs []string) {
		t.Helper()
		tr := filepath.Join(dir, t.Name()+".jsonl")
		if err := os.MkdirAll(filepath.Dir(tr), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tr, []byte(trace+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.CreateTemp(dir, "stdout-")
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdout
		os.Stdout = f
		err = conformCommand([]string{filepath.Join(repoRoot, modelDir), "--machine", machine, "--trace", tr})
		os.Stdout = saved
		if _, serr := f.Seek(0, 0); serr != nil {
			t.Fatal(serr)
		}
		out, _ := io.ReadAll(f)
		f.Close()
		if code := exitCode(err); code != wantCode {
			t.Fatalf("exit %d (%v), want %d; stdout:\n%s", code, err, wantCode, out)
		}
		for _, sub := range wantSubs {
			if !strings.Contains(string(out), sub) {
				t.Fatalf("stdout does not hold %q:\n%s", sub, out)
			}
		}
	}

	childrenValid := strings.Join([]string{
		`{"signal":"Start","args":{"p":{"_tag":"N0"},"c":{"_tag":"N1"}},"state":{"alive":[true,true,false]}}`,
		`{"signal":"Start","args":{"p":{"_tag":"N0"},"c":{"_tag":"N2"}},"state":{"alive":[true,true,true]}}`,
		`{"signal":"Emit","args":{"c":{"_tag":"N1"}}}`,
		`{"signal":"Take","args":{"p":{"_tag":"N0"}},"state":{"late":false}}`,
		`{"signal":"Reenter","args":{"p":{"_tag":"N0"}},"state":{"closing":[1,0,0]}}`,
		`{"signal":"StopNext","args":{"p":{"_tag":"N0"}}}`,
		`{"signal":"EndNode","args":{"p":{"_tag":"N2"}},"state":{"alive":[true,true,false],"orderBad":false}}`,
		`{"signal":"StopNext","args":{"p":{"_tag":"N0"}}}`,
		`{"signal":"EndNode","args":{"p":{"_tag":"N1"}},"state":{"alive":[true,false,false],"orderBad":false}}`,
		`{"signal":"Open","args":{"p":{"_tag":"N0"}},"state":{"epoch":[1,0,0],"closing":[0,2,2]}}`,
	}, "\n")

	childrenOpenBeforeStop := strings.Join([]string{
		`{"signal":"Start","args":{"p":{"_tag":"N0"},"c":{"_tag":"N1"}},"state":{"alive":[true,true,false]}}`,
		`{"signal":"Reenter","args":{"p":{"_tag":"N0"}},"state":{"closing":[1,0,0]}}`,
		`{"signal":"Open","args":{"p":{"_tag":"N0"}},"state":{"epoch":[1,0,0]}}`,
	}, "\n")

	childrenParentEndsFirst := strings.Join([]string{
		`{"signal":"Start","args":{"p":{"_tag":"N0"},"c":{"_tag":"N1"}},"state":{"alive":[true,true,false]}}`,
		`{"signal":"SelfEnd","args":{"p":{"_tag":"N0"}},"state":{"closing":[2,0,0]}}`,
		`{"signal":"EndNode","args":{"p":{"_tag":"N0"}},"state":{"alive":[false,true,false]}}`,
	}, "\n")

	batonValid := strings.Join([]string{
		`{"signal":"Choose","args":{"action":{"_tag":"Spawn"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"started":2,"holder":0}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Spawn"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"started":3,"holder":0}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Point"},"pick":{"_tag":"First"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":1}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Park"},"pick":{"_tag":"Second"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":2}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Wake"},"pick":{"_tag":"Root"},"target":{"_tag":"First"}},"state":{"_tag":"Live","w":{"holder":2}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Point"},"pick":{"_tag":"First"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":1}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"End"},"pick":{"_tag":"Second"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":2}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"End"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":0}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"End"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"over":true,"deadlock":false}}}`,
	}, "\n")

	batonUnspawnedPick := strings.Join([]string{
		`{"signal":"Choose","args":{"action":{"_tag":"Spawn"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Point"},"pick":{"_tag":"Second"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"holder":2}}}`,
	}, "\n")

	batonTwoHolders := strings.Join([]string{
		`{"signal":"Choose","args":{"action":{"_tag":"Spawn"},"pick":{"_tag":"Root"},"target":{"_tag":"Root"}}}`,
		`{"signal":"Choose","args":{"action":{"_tag":"Point"},"pick":{"_tag":"First"},"target":{"_tag":"Root"}},"state":{"_tag":"Live","w":{"status":[2,2,0],"holder":1}}}`,
	}, "\n")

	t.Run("children_valid", func(t *testing.T) {
		runCase(t, "models/children", "Design", childrenValid, 0, []string{"conforms: 10 lines"})
	})
	t.Run("children_open_before_stop", func(t *testing.T) {
		runCase(t, "models/children", "Design", childrenOpenBeforeStop, 20, []string{"mismatch at line 3:"})
	})
	t.Run("children_parent_ends_first", func(t *testing.T) {
		runCase(t, "models/children", "Design", childrenParentEndsFirst, 20, []string{"mismatch at line 3:"})
	})
	t.Run("children_unmapped_signal", func(t *testing.T) {
		runCase(t, "models/children", "Design", `{"signal":"UnmappedEvent"}`, 21, []string{"the model has no signal UnmappedEvent"})
	})
	t.Run("baton_valid", func(t *testing.T) {
		runCase(t, "models/baton", "Design", batonValid, 0, []string{"conforms: 9 lines"})
	})
	t.Run("baton_unspawned_pick", func(t *testing.T) {
		runCase(t, "models/baton", "Design", batonUnspawnedPick, 20, []string{"mismatch at line 2:"})
	})
	t.Run("baton_two_holders", func(t *testing.T) {
		runCase(t, "models/baton", "Design", batonTwoHolders, 20, []string{"mismatch at line 2:"})
	})
	t.Run("baton_unmapped_signal", func(t *testing.T) {
		runCase(t, "models/baton", "Design", `{"signal":"GhostOp"}`, 21, []string{"the model has no signal GhostOp"})
	})
}

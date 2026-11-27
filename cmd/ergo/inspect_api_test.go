package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/api"
)

// TestInspectAPIRoute is the smallest repro of the missing inspect api
// route and JSON flag: the projection property cannot catch a disconnected
// command. It checks in process; no generated Go needs to run.
func TestInspectAPIRoute(t *testing.T) {
	dir := t.TempDir()
	old, next := filepath.Join(dir, "old.ergo"), filepath.Join(dir, "new.ergo")
	for path, src := range map[string]string{
		old:  "func Inspect[T own](x T) int { 1 }\n",
		next: "func Inspect[T](x T) int { 1 }\n",
	} {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
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
	text := capture("api", old)
	if !strings.Contains(text, "own T (a resource may bind it)") {
		t.Fatal(text)
	}
	var fs []api.Function
	if err := json.Unmarshal([]byte(capture("api", next, "--json")), &fs); err != nil || len(fs) == 0 {
		t.Fatalf("json: %v, %+v", err, fs)
	}
	var changes []api.Change
	if err := json.Unmarshal([]byte(capture("api", "--diff", old, next, "--json")), &changes); err != nil || len(changes) != 1 || !changes[0].BreaksResourceCallers {
		t.Fatalf("diff: %v, %+v", err, changes)
	}
	if !strings.Contains(capture("api", "--diff", old, next), "breaks callers that pass a resource") {
		t.Fatal("diff lost warning")
	}
	// A derived type shows its type, mapper identity, media kind, steps, and
	// declarations (content-mappers.md; mutant rc4b-inspect-mapper-missing).
	date := filepath.Join(dir, "date.ergo")
	if err := os.WriteFile(date, []byte("import regex\n\ntype Date derive regex.Pattern(`(?<Year>[0-9]{4})-(?<Month>[0-9]{2})`)\n\nfunc Year(s string) string fails NoMatch { try ParseDate(s).Year }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Derived []struct {
			Type      string
			Plan      string
			Kind      string
			MediaKind string
			Steps     int
			Decls     []string
		}
	}
	if err := json.Unmarshal([]byte(capture(date)), &report); err != nil || len(report.Derived) != 1 {
		t.Fatalf("inspect: %v, %+v", err, report)
	}
	if d := report.Derived[0]; d.Type != "Date" || d.Plan != "regex.MapPattern" || d.Kind != "inline" || d.MediaKind != "regex" || d.Steps == 0 || len(d.Decls) == 0 {
		t.Fatalf("derived: %+v", d)
	}
	// A derived foreign fact shows at each Go call operand it names, a
	// method's receiver counted first (LAWFFI LF0; mutants
	// ffi-check-reads-unread, ffi-check-receiver-index).
	goCall := filepath.Join(dir, "gocall.ergo")
	if err := os.WriteFile(goCall, []byte("import go sort\n\nfunc find(xs []int, k int) int {\n\tlet s = sort.IntSlice(xs)\n\tsort.SearchInts(xs, k) + s.Search(k) + len(xs) + len(s)\n}\n\nfunc main() {\n\tlet _ = find([]int{1, 2, 3}, 2)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	facts := capture("facts", goCall)
	for _, want := range []string{"gocall.ergo:5:18  fact-go-reads  sort.SearchInts  reads a  (checked: go escape analysis)", "gocall.ergo:5:27  fact-go-reads  sort.IntSlice.Search  reads p  (checked: go escape analysis)"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("inspect facts lacks %q:\n%s", want, facts)
		}
	}
}

// TestInspectMachineJSON is the smallest repro of a dead flag: the option
// parser refused --json for inspect machine, though its help offered it.
// It checks in process; no generated Go needs to run.
func TestInspectMachineJSON(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "sketch", "lower", "testdata", "work", "listen.ergo")
	f, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	err = inspectCommand([]string{"machine", path, "--json"})
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
	var views []struct {
		Graph struct {
			Name string
			Arms []struct{ At struct{ File string } }
		}
		Reachable []struct{ State, Signal string }
	}
	if err := json.Unmarshal(b, &views); err != nil || len(views) != 1 || views[0].Graph.Name != "Finder" || len(views[0].Reachable) == 0 || len(views[0].Graph.Arms) == 0 || views[0].Graph.Arms[0].At.File != path {
		t.Fatalf("inspect machine --json: %v\n%s", err, b)
	}
}

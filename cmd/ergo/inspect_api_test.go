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
		old:  "func Read[T](x T) int { 1 }\nfunc Inspect[T](x T) int { 1 }\n",
		next: "func Read[T](x T) int { 1 }\nfunc Inspect[T](x T) int { let y = x; Read(y) }\n",
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
	text := capture("api", next)
	if !strings.Contains(text, "copies T at "+next+":") || !strings.Contains(text, "plain data only") {
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
	// A derived type shows its type, parse engine, reason and counts
	// (patterns.md, Derivation 7; mutant rc4b-inspect-engine-missing).
	date := filepath.Join(dir, "date.ergo")
	if err := os.WriteFile(date, []byte("type Date derive Pattern(`(?<Year>[0-9]{4})-(?<Month>[0-9]{2})`)\n\nfunc Year(s string) string fails NoMatch { try ParseDate(s).Year }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Derived []struct {
			Type, Engine string
			Reason       string
			States       int
			Allocates    bool
		}
	}
	if err := json.Unmarshal([]byte(capture(date)), &report); err != nil || len(report.Derived) != 1 {
		t.Fatalf("inspect: %v, %+v", err, report)
	}
	if d := report.Derived[0]; d.Type != "Date" || d.Engine != "direct" || d.Reason == "" || d.States == 0 || d.Allocates {
		t.Fatalf("derived: %+v", d)
	}
}

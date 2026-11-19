package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInspectFactsListsEvidence: ergo inspect facts on cborpack lists, after
// its facts, one rewrite row for each rewrite ergo inspect rewrites says
// the build applies (EVIDENCE-REPORT), each placed at its codec function's
// type, and its reason names the ergo.laws line that holds its entry. It
// lowers in process; no generated Go runs.
//
// Mutants (lower's ledger): evidence-rewrites-dropped (InspectFacts
// returns the facts alone), evidence-ledger-line-off (the line before the
// entry's), evidence-windows-unplaced (codec windows left at :0:0).
func TestInspectFactsListsEvidence(t *testing.T) {
	program := filepath.Join("..", "..", "examples", "cborpack", "cmd", "cborpack")
	capture := func(args ...string) []byte {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "stdout-")
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
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var sites []struct {
		Law, Provider, Instance string
		Applied                 bool
	}
	if err := json.Unmarshal(capture("rewrites", program, "--json"), &sites); err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, s := range sites {
		if s.Applied {
			applied++
		}
	}
	if applied == 0 {
		t.Fatal("cborpack applies no rewrite: the test has nothing to list")
	}
	var facts []struct{ Kind, At, Holds, Evidence, Reason string }
	if err := json.Unmarshal(capture("facts", program, "--json"), &facts); err != nil {
		t.Fatal(err)
	}
	laws, err := os.ReadFile(filepath.Join("..", "..", "examples", "cborpack", "ergo.laws"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(laws), "\n")
	rows := 0
	for _, f := range facts {
		if f.Kind != "rewrite" {
			continue
		}
		rows++
		if strings.HasPrefix(f.At, ":0:0") || strings.HasPrefix(f.At, ":") {
			t.Errorf("rewrite row %q has no position", f.Holds)
		}
		var n int
		if _, err := fmt.Sscanf(f.Reason, "ergo.laws:%d:", &n); err != nil || n < 1 || n > len(lines) {
			t.Errorf("rewrite row %q names no ergo.laws line: %q", f.Holds, f.Reason)
			continue
		}
		if !strings.HasPrefix(lines[n-1], "law "+f.Holds+" "+f.Evidence+" ") {
			t.Errorf("rewrite row %q (%s) names ergo.laws:%d, which holds %q", f.Holds, f.Evidence, n, lines[n-1])
		}
	}
	if rows != applied {
		t.Errorf("ergo inspect facts lists %d rewrite rows, and the build applies %d rewrites", rows, applied)
	}
}

// TestInspectFactsRoute holds the route of ergo inspect facts (FACT0):
// TestAssumptionsAgree holds the export itself, and cannot see a command
// that does not reach it. It checks in process; no generated Go runs.
//
// Mutant it alone catches: inspect-facts-unrouted (cmd/ergo's ledger).
func TestInspectFactsRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "port.ergo")
	src := "type Port int where 1 <= it && it <= 65535\n\nfunc Next(p Port) int { int(p) + 1 }\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(Next(80))\")\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
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
	if text := capture("facts", path); !strings.Contains(text, "fact-range  Next  1 <= p && p <= 65535  (proved") {
		t.Fatalf("ergo inspect facts prints no range fact of Next:\n%s", text)
	}
	var facts []struct{ Kind, Lo, Hi string }
	if err := json.Unmarshal([]byte(capture("facts", path, "--json")), &facts); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range facts {
		found = found || f.Kind == "fact-range" && f.Lo == "1" && f.Hi == "65535"
	}
	if !found {
		t.Fatalf("ergo inspect facts --json has no range fact of Next: %+v", facts)
	}
}

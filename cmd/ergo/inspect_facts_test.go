package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

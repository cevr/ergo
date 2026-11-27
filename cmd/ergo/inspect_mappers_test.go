package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

func captureInspectCommand(t *testing.T, args []string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "inspect-stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	runErr := inspectCommand(args)
	os.Stdout = saved
	data, rErr := os.ReadFile(f.Name())
	if rErr != nil {
		t.Fatal(rErr)
	}
	return string(data), runErr
}

func TestInspectMappers(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "ergo.mod")
	if err := os.WriteFile(mod, []byte("module inspectdemo\nmap queries/*.re regex.MapPattern\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qdir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qdir, "Date.re"), []byte("(?<Year>[0-9]{4})-(?<Month>[0-9]{2})"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainSrc := `import ./queries/Date.re
import regex

type Host derive regex.Pattern("(?<Sub>[a-z]+)")

func main() needs Console {
	let _ = (ParseHost("abc") catch { case NoMatch: Host{Sub: "x"} }).Sub
	let _ = Date.ParseDate("2026-11") catch { case NoMatch: Date.Date{Year: "0", Month: "0"} }
	Console.Println("ok")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.ergo"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	jsonOut, err := captureInspectCommand(t, []string{"mappers", "--json", dir})
	if err != nil {
		t.Fatalf("inspect mappers --json failed: %v\n%s", err, jsonOut)
	}
	var parsed struct {
		Package string               `json:"package"`
		Mappers []check.MapperRecord `json:"mappers"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("invalid inspect mappers JSON: %v\n%s", err, jsonOut)
	}
	if len(parsed.Mappers) != 2 {
		t.Fatalf("expected 2 mapper records (file + inline), got %d:\n%s", len(parsed.Mappers), jsonOut)
	}
	var sawFile, sawInline bool
	for _, m := range parsed.Mappers {
		if m.Kind == "file" && m.AssetPath == "queries/Date.re" && strings.Contains(m.Generated, "type Date") && len(m.Spans) > 0 {
			sawFile = true
		}
		if m.Kind == "inline" && m.Target == "Host" && strings.Contains(m.Generated, "func ParseHost") && len(m.Spans) > 0 {
			sawInline = true
		}
	}
	if !sawFile || !sawInline {
		t.Fatalf("missing expected file or inline mapper record:\n%s", jsonOut)
	}

	textOut, err := captureInspectCommand(t, []string{"mapped", dir})
	if err != nil {
		t.Fatalf("inspect mapped text failed: %v\n%s", err, textOut)
	}
	for _, want := range []string{"mapper Date (file)", "mapper Host (inline)", "generated:", "spans ("} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("text output missing %q:\n%s", want, textOut)
		}
	}
}

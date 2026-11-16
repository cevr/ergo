package residue_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestFastTierExamplesAreLedgerKillers checks the gate's fast-tier example
// list (scripts/examples/fast-tier.txt, gate-speed GS3) against the mutant
// ledgers: a listed program is a default program that some ledger row names
// as a killer (examples:PROG, examples:PROG/case, example:PROG test …), and
// a default program a ledger row names is listed, so the fast tier kills
// what it killed before it was narrowed. A program no row names runs in the
// full tier only. It lives in residue, which reads every ledger by design.
func TestFastTierExamplesAreLedgerKillers(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts", "examples", "fast-tier.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			listed = append(listed, line)
		}
	}
	named := map[string][]string{} // program -> ledger rows naming it
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var ledger struct {
			Mutants []struct {
				Name    string   `json:"name"`
				Killers []string `json:"killers"`
			} `json:"mutants"`
		}
		if err := json.Unmarshal(data, &ledger); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		for _, m := range ledger.Mutants {
			for _, killer := range m.Killers {
				if program := exampleKiller(killer); program != "" {
					named[program] = append(named[program], m.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	isDefault := func(program string) bool {
		tags, err := os.ReadFile(filepath.Join(root, "examples", program, "tags"))
		return err == nil && slices.Contains(strings.Fields(string(tags)), "default")
	}
	for _, program := range listed {
		switch {
		case !isDefault(program):
			t.Errorf("fast-tier.txt lists %q, which is not a default example program", program)
		case len(named[program]) == 0:
			t.Errorf("fast-tier.txt lists %q, which no mutant ledger row names as a killer: it belongs to the full tier only", program)
		}
	}
	for program, rows := range named {
		if isDefault(program) && !slices.Contains(listed, program) {
			t.Errorf("ledger rows %v name the default example %q as a killer; list it in scripts/examples/fast-tier.txt", rows, program)
		}
	}
	if len(listed) == 0 {
		t.Error("fast-tier.txt lists no program")
	}
}

// exampleKiller is the example program a killer names (examples:PROG,
// examples:PROG/case, example:PROG test …), or "" for a Go test of the
// examples runner (examples:TestX, scripts/examples:TestX).
func exampleKiller(killer string) string {
	rest, ok := strings.CutPrefix(killer, "examples:")
	if !ok {
		if rest, ok = strings.CutPrefix(killer, "example:"); !ok {
			return ""
		}
	}
	program, _, _ := strings.Cut(rest, "/")
	program, _, _ = strings.Cut(program, " ")
	if strings.HasPrefix(program, "Test") {
		return ""
	}
	return program
}

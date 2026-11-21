package lint_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// TestUnbuiltVariantAcrossPackages is the smallest repro of a false
// finding on examples/vt: a field-less variant built in another package
// by its qualified name, vt.Single, was reported unbuilt. In a module
// whose binary builds Single and Head that way, only Mark's variants are
// unbuilt.
// Mutant: lh2a-qualified-variant-unbuilt.
func TestUnbuiltVariantAcrossPackages(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ergo.mod": "module app\n",
		"part/part.ergo": `// Part is the place of a cell in its character.
type Part = Single{} | Head{}

type Mark = Tail{} | Stop{}
`,
		"cell/cell.ergo": `import app/part

// Cell is one cell.
type Cell { C rune, Part part.Part }

// Blank is the empty cell.
func Blank() Cell { Cell{C: rune(0), Part: part.Single} }

// Wide is the head of a wide character.
func Wide(c rune) Cell { Cell{C: c, Part: part.Head} }
`,
		"cmd/app/main.ergo": `import app/cell

func main() needs Console {
	Console.Println("\(cell.Blank().C) \(cell.Wide(65).C)")
}
`,
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings["unbuilt-variant"] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"part/part.ergo:4 unbuilt-variant"}
	got := layoutFindings(t, filepath.Join(dir, "cmd", "app"), conf)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

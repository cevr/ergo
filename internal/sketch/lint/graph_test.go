package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketchfront"
)

// TestGraphRowsAreTheExport is the one producer of the graph rules: a
// run lints a module whose package imports a required module (a local
// replacement) that holds files at the same module-relative names, re-
// exports one of its own declarations and has a file cycle of its own.
// The run's rows are the rows `ergo graph export` writes for the same
// program (sketchfront.GraphOf) less its tasks, each package under its
// module; and the graph rules report the linted module's findings only,
// at their own files. Mutants: layout-graph-module-identity (a unit
// keeps no module), layout-reexport-own-lets (a required module's re-
// export is taken for the linted module's).
func TestGraphRowsAreTheExport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		"dep/ergo.mod":          "module dep\n",
		"dep/go.mod":            "module dep\n\ngo 1.27\n",
		"dep/lib/lib.ergo":      "func Base() int { 7 }\n",
		"dep/re/re.ergo":        "import dep/lib\n\nlet Base = lib.Base\n",
		"dep/low/low.ergo":      "func D() int { half() }\n\nfunc whole() int { 2 }\n",
		"dep/low/half.ergo":     "func half() int { whole() }\n",
		"app/ergo.mod":          "module app\n",
		"app/go.mod":            "module app\n\ngo 1.27\n\nrequire dep v0.0.0\n\nreplace dep => ../dep\n",
		"app/lib/lib.ergo":      "func Base() int { 7 }\n",
		"app/re/re.ergo":        "import app/lib\n\nlet Base = lib.Base\n",
		"app/low/low.ergo":      "import app/re\nimport dre dep/re\nimport dep/low\n\nfunc F() int { re.Base() + dre.Base() + low.D() + half() }\n\nfunc whole() int { 2 }\n",
		"app/low/half.ergo":     "func half() int { whole() }\n",
		"app/cmd/app/main.ergo": "import app/low\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(low.F())\")\n}\n",
	} {
		write(name, text)
	}
	a, err := sketchfront.CheckDir(filepath.Join(dir, "app", "low"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) != 0 {
		t.Fatalf("the module does not check: %v", a.Diagnostics)
	}
	in := InputOf(a)
	if got, want := rowsText(programGraphOf(in.Units).t), rowsText(a.Graph()); !slices.Equal(got, want) {
		t.Fatalf("lint's rows differ from the export's\nlint:\n%s\nexport:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	conf, err := Builtin().Configure(Config{Version: ConfigVersion, Rules: map[string]Setting{"file-cycle": {Severity: Warning, HasSeverity: true}}}, []string{Layout})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range conf.Run(in).Findings {
		if f.Code >= "L155" && f.Code <= "L158" {
			got = append(got, fmt.Sprintf("%s:%d:%d %s", f.File, f.Line, f.Column, f.Rule))
		}
	}
	slices.Sort(got)
	want := []string{
		"low/half.ergo:1:19 file-cycle",
		"low/low.ergo:5:19 hidden-dependency",
		"low/low.ergo:5:51 file-cycle",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("graph rules: got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// rowsText is t's rows less its tasks, each node by the names of its
// holders from its module down, sorted.
func rowsText(t *graphrows.Table) []string {
	name := func(n uint32) string {
		var parts []string
		for ; n != graphrows.NoNode; n = t.Nodes[n].Parent {
			parts = append(parts, t.Nodes[n].Kind.String()+":"+t.Str(t.Nodes[n].Name))
		}
		slices.Reverse(parts)
		return strings.Join(parts, "/")
	}
	at := func(p graphrows.Pos) string {
		if p.File == graphrows.NoNode {
			return "-"
		}
		return fmt.Sprintf("%s@%d:%d:%d", name(p.File), p.Off, p.Line, p.Col)
	}
	var out []string
	for i, n := range t.Nodes {
		if n.Kind != graphrows.Task {
			out = append(out, fmt.Sprintf("node %s sub=%d digest=%s %s", name(uint32(i)), n.Sub, t.Str(n.Digest), at(t.NodeAt[i])))
		}
	}
	for i, e := range t.Edges {
		if e.Kind != graphrows.Input {
			out = append(out, fmt.Sprintf("edge %s %d %s -> %s %s", e.Kind, e.How, name(e.From), name(e.To), at(t.EdgeAt[i])))
		}
	}
	slices.Sort(out)
	return out
}

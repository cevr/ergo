package residue_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestCompilerMapKeyRule runs the map-key rule of
// TestCompilerHasNoPointerKeyedMaps on a small module, since compiler/ does
// not exist yet: every refused key in one package and across an import,
// each next to a key the rule keeps. It kills cs11-map-borrow-allowed,
// cs11-map-record-allowed, cs11-map-union-allowed, cs11-map-id-allowed,
// cs11-map-import-unresolved, cs11-map-string-refused,
// s3a-map-dense-id-allowed and s3a-map-dense-opt-allowed.
func TestCompilerMapKeyRule(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ergo.mod", "module compiler\n")
	write("types/types.ergo", "// TypeID is a type's dense id.\ntype TypeID uint32\n\n// Type is an interned type.\ntype Type { ID TypeID, Name string }\n")
	write("syntax/node.ergo", strings.Join([]string{
		"import compiler/types",
		"import dense",
		"",
		"type NodeID int",
		"type Path string",
		"type Node { ID NodeID }",
		"type Expr = Num{V int} | Name{Text string}",
		"",
		"type tables {",
		"\tbyNode Map[Node, int],",
		"\tbyExpr Map[Expr, int],",
		"\tbyBorrow Map[*Node, int],",
		"\tbyID Map[NodeID, int],",
		"\tbyType Map[types.TypeID, int],",
		"\tbyDenseID Map[dense.ID[Node], int],",
		"\tbyDenseOpt Map[dense.Opt[types.Type], int],",
		"\tbyRun Map[dense.Run[int], int],",
		"\tbyName Map[string, Node],",
		"\tbyPath Map[Path, Node],",
		"\tcounts Map[int, int],",
		"}",
		"",
	}, "\n"))
	findings, err := compilerMapKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		_, rest, _ := strings.Cut(f, ": Map keyed by ")
		got = append(got, strings.TrimSuffix(rest, ": use a dense side table indexed by the id"))
	}
	want := []string{
		"*Node (a borrow)",
		"Expr (a union)",
		"Node (a record)",
		"NodeID (a dense id)",
		"dense.ID[Node] (a dense id)",
		"dense.Opt[types.Type] (a dense id)",
		"types.TypeID (a dense id)",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("refused keys:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(findings) > 0 && !strings.Contains(findings[0], filepath.Join("syntax", "node.ergo")+":") {
		t.Errorf("a finding names its file, line and column: %s", findings[0])
	}
}

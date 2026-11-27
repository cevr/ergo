package residue_test

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/scripts/mutants/anchor"
)

// TestErgoDeclSpansMatchTheParser holds the mutant runner's Ergo
// declarations, read line by line (scripts/mutants/anchor.ErgoDecls,
// which keeps the parser out of the runner's and the gate's closures) to
// the parser, on
// every .ergo file a ledger row anchors in by declaration (MN0
// DECL-ANCHOR): the named declarations start where the parser's top-level
// declarations start (their doc comments included), in the same order,
// and each span holds its declaration and stops before the next. So a row
// located inside a declaration's span is inside that declaration's text.
// Mutant: anchor-ergo-doc-across-blank (a span that takes in the comments
// and blank lines above a declaration's doc comment).
func TestErgoDeclSpansMatchTheParser(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
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
		var l struct {
			Mutants []struct{ File, Decl string } `json:"mutants"`
		}
		if err := json.Unmarshal(data, &l); err != nil {
			return nil // TestLedgerEditsApplyOnce reports it
		}
		for _, m := range l.Mutants {
			if m.Decl != "" && strings.HasSuffix(m.File, ".ergo") {
				files[m.File] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if why := declSpansDiffer(src); why != "" {
			t.Errorf("%s: %s", name, why)
		}
	}
}

// declSpansDiffer says how anchor.ErgoDecls's named spans differ from the
// parser's top-level declarations of src, or "".
func declSpansDiffer(src []byte) string {
	f, err := syntax.Parse(string(src))
	if err != nil {
		return "does not parse: " + err.Error()
	}
	type span struct{ start, end int }
	var parsed []span
	for _, d := range f.Decls {
		if _, ok := d.(*syntax.ImportDecl); ok {
			continue
		}
		if fd, ok := d.(*syntax.FuncDecl); ok && fd.Derived != nil {
			continue
		}
		if ld, ok := d.(*syntax.LawDecl); ok && (ld.Derived != nil || ld.Pair != nil) {
			continue
		}
		start := int(d.Pos())
		if doc := declDoc(d); doc != nil && len(doc.List) > 0 {
			start = int(doc.List[0].Pos())
		}
		parsed = append(parsed, span{start, int(d.End())})
	}
	var lined []anchor.Decl
	for _, d := range anchor.ErgoDecls(src) {
		if d.Name != "" {
			lined = append(lined, d)
		}
	}
	if len(lined) != len(parsed) {
		return "the lines give " + strconv.Itoa(len(lined)) + " named declarations, the parser " + strconv.Itoa(len(parsed))
	}
	for i, p := range parsed {
		l := lined[i]
		switch {
		case l.Start != p.start:
			return l.Name + " starts at byte " + strconv.Itoa(l.Start) + ", the parser's declaration at " + strconv.Itoa(p.start)
		case l.End < p.end:
			return l.Name + " ends at byte " + strconv.Itoa(l.End) + ", before the parser's end " + strconv.Itoa(p.end)
		case i+1 < len(parsed) && l.End > parsed[i+1].start:
			return l.Name + " runs into the next declaration"
		}
	}
	return ""
}

// declDoc is a top-level declaration's doc comment, or nil.
func declDoc(d syntax.Decl) *syntax.CommentGroup {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Doc
	case *syntax.TypeDecl:
		return d.Doc
	case *syntax.VarDecl:
		return d.Doc
	case *syntax.ContextDecl:
		return d.Doc
	case *syntax.LawDecl:
		return d.Doc
	case *syntax.TestDecl:
		return d.Doc
	}
	return nil
}

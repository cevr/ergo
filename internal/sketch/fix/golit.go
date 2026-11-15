package fix

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A goLit is an Ergo program held in a Go string literal: the tests and
// generators of the compiler write their programs this way. Its virtual
// name is the Go file's path, '#' and the literal's byte offset, so the
// editor reads and edits it as a file of its own.
type goLit struct {
	file     string // the Go file
	from, to int    // the literal's bytes, quotes included
	raw      bool   // a `raw` literal, else an "interpreted" one
}

// goLits are the string literals of a Go file that parse as Ergo programs
// declaring a type: the programs the fix reads. A literal built by
// concatenation or by fmt is not one program, and is not read.
func goLits(file string) ([]goLit, []string, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, nil
	}
	var lits []goLit
	var texts []string
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(text, "type ") || !strings.Contains(text, "\n") {
			return true
		}
		if _, err := syntax.Parse(text); err != nil {
			return true
		}
		from := fset.Position(bl.ValuePos).Offset
		lits = append(lits, goLit{file: file, from: from, to: from + len(bl.Value), raw: bl.Value[0] == '`'})
		texts = append(texts, text)
		return true
	})
	return lits, texts, nil
}

// walkGo calls file for each .go file under root; a folder named gostd or
// testdata, or starting with a dot, is skipped.
func walkGo(root string, file func(string) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "gostd" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			return file(path)
		}
		return nil
	})
}

func (l goLit) name() string { return fmt.Sprintf("%s#%d", l.file, l.from) }

// quote is text as a literal of l's kind; a raw literal whose text gained
// a backquote becomes an interpreted one.
func (l goLit) quote(text string) string {
	if l.raw && !strings.Contains(text, "`") {
		return "`" + text + "`"
	}
	return strconv.Quote(text)
}

// spliceGo writes each edited literal's text back into its Go file, last
// literal first, and returns the Go files' new texts.
func spliceGo(lits map[string]goLit, out map[string]string) (map[string]string, error) {
	byFile := map[string][]goLit{}
	for name := range out {
		if l, ok := lits[name]; ok {
			byFile[l.file] = append(byFile[l.file], l)
		}
	}
	res := map[string]string{}
	for file, ls := range byFile {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		text := string(src)
		sort.Slice(ls, func(i, j int) bool { return ls[i].from > ls[j].from })
		for _, l := range ls {
			text = text[:l.from] + l.quote(out[l.name()]) + text[l.to:]
		}
		res[file] = text
	}
	return res, nil
}

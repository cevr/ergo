// Package parsed states std laws as the parser reads them, for the laws
// whose statement exists only in the parser: a refined type's round trip
// and a by-name pair's (docs/spec/stdlib.md, Std laws 4). Their runners
// call Run, which hands these statements to lawledger.RunWith. It is a
// package of its own because the parser imports the runtime, whose law
// runners import lawledger.
package parsed

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

var spaces = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s+`) })

// Statement is the source text of d from its name to its closing brace,
// with each run of white space made one space, as lawledger.Statement cuts
// it. The law the parser derives for a refined type (d.Derived) or for a
// by-name pair (d.Pair) has no source text: the parser builds it after the
// file's declarations, at the type's or the Wire's position (refine.go,
// pair.go). Its statement is the law as the parser builds it, with the
// names the file spells.
func Statement(f *syntax.File, d *syntax.LawDecl) string {
	if td := d.Derived; td != nil {
		// Derived nodes retain the type's source positions, not a source
		// slice of the generated law. State the parser's derived law.
		base := spaces().ReplaceAllString(f.Src[td.Spec.Pos():td.Spec.End()], " ")
		binding := "let it " + base + " = v"
		switch td.Spec.(type) {
		case *syntax.Ident, *syntax.SelectorExpr:
			binding = "let it = " + base + "(v)"
		}
		return fmt.Sprintf("%s(v %s) { %s (%s) && try %s(it) == v }", d.Name.Name, td.Name.Name, binding, syntax.WhereText(f.Src, td.Where), syntax.ParseName(td.Name.Name))
	}
	if w := d.Pair; w != nil {
		return pairStatement(d, w)
	}
	return spaces().ReplaceAllString(f.Src[d.Name.Pos():d.Body.End()], " ")
}

// pairStatement is the round-trip law the parser derives for the by-name
// pair whose Wire is w (types.md, Codecs 7), as source: its nodes carry
// Wire's position, not a source slice of their own. It states the form
// syntax/pair.go builds: over a drawn x, with an empty buffer for the
// append form, and with try when ParseX fails.
func pairStatement(d *syntax.LawDecl, w *syntax.FuncDecl) string {
	x := w.Type.Params.List[0].Type.(*syntax.Ident).Name
	into := ""
	if len(w.Type.Params.List) == 2 {
		into = "[]byte{}"
	}
	held := fmt.Sprintf("%s(x.Wire(%s)) == x", syntax.ParseName(x), into)
	if _, fails := d.Body.List[0].(*syntax.ExprStmt).X.(*syntax.TryExpr); fails {
		held = "try " + held
	}
	return fmt.Sprintf("%s(x %s) { %s }", d.Name.Name, x, held)
}

// Laws are the laws of the parsed files of the std package path by ledger
// key, "path Law" for a top-level law and "path Type.Law" for a member,
// with their statements, and the problems of the files: a law declared
// twice, and a std test, which no step runs.
func Laws(path string, files []*syntax.File) (map[string]string, []string) {
	out := map[string]string{}
	var problems []string
	add := func(key, statement string) {
		if _, dup := out[key]; dup {
			problems = append(problems, fmt.Sprintf("std law %s is declared twice", key))
		}
		out[key] = statement
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.LawDecl:
				add(path+" "+d.Name.Name, Statement(f, d))
			case *syntax.TestDecl:
				// No step runs a std test: ergo test and check-time
				// evaluation run the root package's tests, and no gate
				// step makes a std package the root. A std law runs
				// through its runner; a std test would pass unrun (FK9
				// g1b dropped one for that reason).
				problems = append(problems, fmt.Sprintf("std %s declares test %s, which nothing runs: state it as a law with a runner, or as a Go test", path, d.Name.Name))
			case *syntax.TypeDecl:
				if it, ok := d.Spec.(*syntax.InterfaceType); ok {
					for _, l := range it.Laws {
						add(path+" "+d.Name.Name+"."+l.Name.Name, Statement(f, l))
					}
				}
			}
		}
	}
	return out, problems
}

// Parse parses the source files of the std package path (the prelude for
// stdlib.PreludePath), in file-name order.
func Parse(path string) ([]*syntax.File, error) {
	var sources map[string]string
	if path == stdlib.PreludePath {
		sources = stdlib.Prelude()
	} else if sources, _ = stdlib.Package(path); sources == nil {
		return nil, fmt.Errorf("no std package %s", path)
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %v", path, name, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// Run is lawledger.Run with each law's statement as the parser states it
// (Statement), so a runner of a derived law runs it.
func Run(t *testing.T, laws map[string]lawledger.Law) {
	t.Helper()
	statements := map[string]string{}
	seen := map[string]bool{}
	for key := range laws {
		path, _, _ := strings.Cut(key, " ")
		if seen[path] {
			continue
		}
		seen[path] = true
		files, err := Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		of, _ := Laws(path, files)
		for k, s := range of {
			statements[k] = s
		}
	}
	lawledger.RunWith(t, statements, laws)
}

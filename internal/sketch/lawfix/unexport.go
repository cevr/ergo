package lawfix

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Unexport renames each function of names, an exported law helper
// candidate (check.Package.LawExportedHelpers) that no other package names,
// to its unexported spelling: its first letter lowered. Its declaration,
// every use in the package and the first word of its doc comment, when that
// is its name, change; nothing else does. A function whose new name is
// already declared in the package keeps its name. It returns the new text
// of each changed file by name, and the renames made ("Old -> new").
func Unexport(pkg *check.Package, files []*syntax.File, decls []*syntax.FuncDecl) (map[string]string, []string) {
	declared := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			for _, n := range declNames(d) {
				declared[n] = true
			}
		}
	}
	funcs := map[*syntax.FuncDecl]*check.Func{}
	for _, fn := range pkg.Funcs {
		if fn.Decl != nil {
			funcs[fn.Decl] = fn
		}
	}
	type rename struct {
		fn       *check.Func
		from, to string
	}
	byObj := map[check.Object]rename{}
	var done []string
	for _, d := range decls {
		fn := funcs[d]
		if fn == nil {
			continue
		}
		to := lower(d.Name.Name)
		if to == d.Name.Name || declared[to] || syntax.IsKeyword(to) {
			continue
		}
		declared[to] = true
		byObj[fn] = rename{fn, d.Name.Name, to}
		done = append(done, d.Name.Name+" -> "+to)
	}
	out := map[string]string{}
	if len(byObj) == 0 {
		return out, nil
	}
	for _, f := range files {
		type at struct {
			from, to int
			text     string
		}
		var edits []at
		syntax.Inspect(f, func(n syntax.Node) bool {
			id, ok := n.(*syntax.Ident)
			if !ok {
				return true
			}
			o := pkg.Info.Uses[id]
			if o == nil {
				o = pkg.Info.Defs[id]
			}
			if r, ok := byObj[o]; ok && id.Name == r.from {
				edits = append(edits, at{int(id.Pos()), int(id.End()), r.to})
			}
			return true
		})
		for _, d := range f.Decls {
			fd, ok := d.(*syntax.FuncDecl)
			if !ok || fd.Doc == nil {
				continue
			}
			r, ok := byObj[funcs[fd]]
			if !ok {
				continue
			}
			c := fd.Doc.List[0]
			text := f.Src[int(c.Pos()):int(c.End())]
			if i := strings.Index(text, r.from); i >= 0 && strings.TrimSpace(strings.TrimPrefix(text[:i], "//")) == "" {
				end := i + len(r.from)
				if end == len(text) || !isIdent(text[end]) {
					edits = append(edits, at{int(c.Pos()) + i, int(c.Pos()) + end, r.to})
				}
			}
		}
		if len(edits) == 0 {
			continue
		}
		sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
		src := f.Src
		last := -1
		for _, e := range edits {
			if e.from == last {
				continue
			}
			last = e.from
			src = src[:e.from] + e.text + src[e.to:]
		}
		out[f.Name] = src
	}
	sort.Strings(done)
	return out, done
}

func isIdent(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func lower(name string) string {
	r, n := utf8.DecodeRuneInString(name)
	return string(unicode.ToLower(r)) + name[n:]
}

func declNames(d syntax.Decl) []string {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return []string{d.Name.Name}
	case *syntax.TypeDecl:
		return []string{d.Name.Name}
	case *syntax.ContextDecl:
		return []string{d.Name.Name}
	case *syntax.VarDecl:
		var out []string
		for _, s := range d.Specs {
			for _, n := range s.Names {
				out = append(out, n.Name)
			}
		}
		return out
	case *syntax.LawDecl:
		return []string{d.Name.Name}
	case *syntax.TestDecl:
		return []string{d.Name.Name}
	}
	return nil
}

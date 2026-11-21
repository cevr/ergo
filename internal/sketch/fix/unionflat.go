package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// UnionFlat is the union pass of ergo fix (DECL-VALUES §6 unit 2): a
// package written before a union could list a union of its package
// (types.md, Unions 1) re-lists a sub-union's variants and rebuilds them
// one arm at a time. It rewrites three spellings, each into one the
// checker gives the same verdict, and nothing else:
//
//   - Re-listing. A union B whose entries hold, one after another and in
//     the same order, every variant of another union A of the package
//     lists A there instead: `type Quiz = Calculating{…} | Asking{…} |
//     Showing{…}` with `type Settled = Asking | Showing` becomes `type
//     Quiz = Calculating{…} | Settled`, and each variant B declared in
//     place moves, as written, to the entry of A that names it. The
//     variants keep their order, so B is the same set in the same order.
//   - Identity arms. In the match that ends a function whose result is a
//     union R, the arms `case V as v: v`, one after another (in any order),
//     whose V are exactly the variants of a union A of the package that R
//     holds, become the one arm `case A as v: v`: each arm handed its value
//     to R, and A widens into R.
//   - Identity widening. A function of one parameter `s A` whose body is
//     only `match s { case A as v: v }` and whose result is a union that
//     holds A's variants becomes `{ s }`.
//
// Each rewrite needs every name it reads to be a union or a record of the
// package, declared in its files, without type parameters: a generic
// union, an entry of another package or an entry the pass cannot read
// leaves the union as it is. Where two unions could stand for the same
// entries (an equal set, or two sets that overlap), it writes neither:
// that choice is the author's. A variant in the function's failure row
// keeps its arm. The package's own wrappers (`case V as v: W{V: v}`) are a
// choice of meaning and are never rewritten.
//
// One rewrite is made per round and the files are parsed again, so each
// rewrite reads the result of the one before; every rewrite removes an
// entry or an arm, so the rounds are at most the package's union entries
// and case arms. The pass is idempotent: its output holds none of the
// three spellings.
func UnionFlat(texts map[string]string) (out map[string]string, changes []string, err error) {
	out = map[string]string{}
	for k, v := range texts {
		out[k] = v
	}
	bound := 1
	for _, src := range texts {
		f, err := syntax.Parse(src)
		if err != nil {
			return texts, nil, err
		}
		bound += flatSize(f)
	}
	for round := 0; ; round++ {
		if round > bound {
			return texts, nil, fmt.Errorf("the union pass did not settle in %d rounds", bound)
		}
		p, err := readFlat(out)
		if err != nil {
			return texts, nil, err
		}
		r, ok := p.next()
		if !ok {
			return out, changes, nil
		}
		for file, eds := range r.edits {
			text := out[file]
			slices.SortStableFunc(eds, func(a, b edit) int { return b.from - a.from })
			for _, e := range eds {
				text = text[:e.from] + e.text + text[e.to:]
			}
			if _, err := syntax.Parse(text); err != nil {
				return texts, nil, fmt.Errorf("%s: the rewritten source does not parse: %w", file, err)
			}
			out[file] = text
		}
		changes = append(changes, r.what)
	}
}

// flatSize is the count of union entries and case arms of f: a bound on
// the rewrites, each of which removes one or more.
func flatSize(f *syntax.File) int {
	n := 0
	syntax.Inspect(f, func(x syntax.Node) bool {
		switch x := x.(type) {
		case *syntax.InterfaceType:
			n += len(x.Union)
		case *syntax.CaseClause:
			n++
		}
		return true
	})
	return n
}

// flatUnion is a union declaration of the package.
type flatUnion struct {
	file  string
	decl  *syntax.TypeDecl
	it    *syntax.InterfaceType
	names []string // each entry's name
	// readable is false for a union the pass leaves alone: generic, or
	// with an entry that is no name of the package.
	readable bool
}

type flatPkg struct {
	texts   map[string]string
	files   []string // in name order
	parsed  map[string]*syntax.File
	unions  map[string]*flatUnion
	records map[string]bool
}

type flatRewrite struct {
	edits map[string][]edit
	what  string
}

func readFlat(texts map[string]string) (*flatPkg, error) {
	p := &flatPkg{texts: texts, parsed: map[string]*syntax.File{}, unions: map[string]*flatUnion{}, records: map[string]bool{}}
	for k := range texts {
		p.files = append(p.files, k)
	}
	slices.Sort(p.files)
	for _, file := range p.files {
		f, err := syntax.Parse(texts[file])
		if err != nil {
			return nil, err
		}
		p.parsed[file] = f
		for _, d := range f.Decls {
			td, ok := d.(*syntax.TypeDecl)
			if !ok || td.Origin != nil {
				continue
			}
			switch s := td.Spec.(type) {
			case *syntax.StructType:
				p.records[td.Name.Name] = true
			case *syntax.InterfaceType:
				if len(s.Union) < 2 {
					continue
				}
				u := &flatUnion{file: file, decl: td, it: s, readable: td.TypeParams == nil && td.Extern == nil}
				for _, x := range s.Union {
					switch x := x.(type) {
					case *syntax.Ident:
						u.names = append(u.names, x.Name)
					case *syntax.VariantSpec:
						u.names = append(u.names, x.Name.Name)
						p.records[x.Name.Name] = true
						if x.TypeArgs != nil {
							u.readable = false
						}
					default:
						u.names = append(u.names, "")
						u.readable = false
					}
				}
				p.unions[td.Name.Name] = u
			}
		}
	}
	for _, u := range p.unions {
		for _, n := range u.names {
			if n == "" || !p.records[n] && p.unions[n] == nil {
				u.readable = false
			}
		}
	}
	return p, nil
}

// variants is the variant set of the union name, in its order, through
// the unions it lists; ok is false for a name that is no readable union
// or a union on a listing cycle.
func (p *flatPkg) variants(name string) ([]string, bool) {
	var out []string
	var visit func(string, map[string]bool) bool
	visit = func(n string, on map[string]bool) bool {
		u := p.unions[n]
		if u == nil || !u.readable || on[n] {
			return false
		}
		on[n] = true
		defer delete(on, n)
		for _, e := range u.names {
			if p.unions[e] != nil {
				if !visit(e, on) {
					return false
				}
				continue
			}
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
		return true
	}
	if !visit(name, map[string]bool{}) {
		return nil, false
	}
	return out, true
}

// next is the package's first rewrite, in file and source order: a
// re-listing first, then identity arms, then an identity widening.
func (p *flatPkg) next() (flatRewrite, bool) {
	if r, ok := p.relisting(); ok {
		return r, true
	}
	if r, ok := p.identityArms(); ok {
		return r, true
	}
	return p.identityWidening()
}

// unionNames are the package's unions in file and source order.
func (p *flatPkg) unionNames() []string {
	var out []string
	for _, file := range p.files {
		for _, d := range p.parsed[file].Decls {
			if td, ok := d.(*syntax.TypeDecl); ok && p.unions[td.Name.Name] != nil && p.unions[td.Name.Name].decl == td {
				out = append(out, td.Name.Name)
			}
		}
	}
	return out
}

// pick is the one candidate of cands (index sets into a sequence) that no
// other candidate contains, overlaps or equals, the first such in order;
// a candidate inside another is dropped for it.
func pick(cands [][]string) int {
	contains := func(a, b []string) bool { // b ⊆ a
		for _, x := range b {
			if !slices.Contains(a, x) {
				return false
			}
		}
		return true
	}
	for i, a := range cands {
		ok := true
		for j, b := range cands {
			if i == j {
				continue
			}
			switch {
			case contains(b, a) && contains(a, b): // equal: two names for one set
				ok = false
			case contains(b, a): // a inside b: b is the larger listing
				ok = false
			case contains(a, b): // b inside a: a stands for it
			default:
				for _, x := range a {
					if slices.Contains(b, x) { // overlap
						ok = false
					}
				}
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// subUnions are the readable unions of two or more variants whose set is
// inside set (strictly when strict), other than skip, with their sets.
func (p *flatPkg) subUnions(set []string, skip string, strict bool) (names []string, sets [][]string) {
	for _, a := range p.unionNames() {
		if a == skip {
			continue
		}
		va, ok := p.variants(a)
		if !ok || len(va) < 2 || strict && len(va) >= len(set) {
			continue
		}
		inside := true
		for _, v := range va {
			if !slices.Contains(set, v) {
				inside = false
			}
		}
		if inside {
			names, sets = append(names, a), append(sets, va)
		}
	}
	return names, sets
}

// relisting is the first union B that writes out, in a row and in order,
// the variants of another union A of the package: B lists A instead.
func (p *flatPkg) relisting() (flatRewrite, bool) {
	for _, b := range p.unionNames() {
		ub := p.unions[b]
		vb, ok := p.variants(b)
		if !ok || slices.ContainsFunc(ub.names, func(n string) bool { return p.unions[n] != nil }) {
			continue // B already lists a union: its entries are no row of variants
		}
		names, sets := p.subUnions(vb, b, true)
		// A stands only where B writes its variants in a row, in A's order.
		var rowNames []string
		var rowSets [][]string
		var rowAt []int
		for i, a := range names {
			if at := runAt(ub.names, sets[i]); at >= 0 {
				rowNames, rowSets, rowAt = append(rowNames, a), append(rowSets, sets[i]), append(rowAt, at)
			}
		}
		k := pick(rowSets)
		if k < 0 {
			continue
		}
		a, set, at := rowNames[k], rowSets[k], rowAt[k]
		src := p.texts[ub.file]
		first, last := ub.it.Union[at], ub.it.Union[at+len(set)-1]
		eds := map[string][]edit{ub.file: {{from: int(first.Pos()), to: int(last.End()), text: a}}}
		// Each variant B declares in place moves to the entry of A, or of
		// a union A lists, that names it bare.
		moved := true
		for _, x := range ub.it.Union[at : at+len(set)] {
			vs, ok := x.(*syntax.VariantSpec)
			if !ok {
				continue
			}
			file, id := p.bareEntry(a, vs.Name.Name, map[string]bool{})
			if id == nil {
				moved = false
				break
			}
			eds[file] = append(eds[file], edit{from: int(id.Pos()), to: int(id.End()), text: src[vs.Pos():vs.End()]})
		}
		if !moved {
			continue
		}
		return flatRewrite{edits: eds, what: fmt.Sprintf("%s: %s lists %s", ub.file, b, a)}, true
	}
	return flatRewrite{}, false
}

// runAt is where names holds set in a row, in set's order, or -1.
func runAt(names, set []string) int {
	for i := 0; i+len(set) <= len(names); i++ {
		if slices.Equal(names[i:i+len(set)], set) {
			return i
		}
	}
	return -1
}

// bareEntry is the entry of the union u, or of a union it lists, that
// names the variant v bare.
func (p *flatPkg) bareEntry(u, v string, seen map[string]bool) (string, *syntax.Ident) {
	if seen[u] {
		return "", nil
	}
	seen[u] = true
	fu := p.unions[u]
	for _, x := range fu.it.Union {
		id, ok := x.(*syntax.Ident)
		if !ok {
			continue
		}
		if id.Name == v {
			return fu.file, id
		}
		if p.unions[id.Name] != nil {
			if file, found := p.bareEntry(id.Name, v, seen); found != nil {
				return file, found
			}
		}
	}
	return "", nil
}

// identityArm is the variant and binder of an arm `case V as v: v`.
func identityArm(c *syntax.CaseClause) (variant, binder string, ok bool) {
	if len(c.List) != 1 || c.Written != nil || len(c.Body) != 1 {
		return "", "", false
	}
	as, ok := c.List[0].(*syntax.AsPattern)
	if !ok || as.Name == nil || as.Name.Name == "_" {
		return "", "", false
	}
	v, ok := as.X.(*syntax.Ident)
	if !ok {
		return "", "", false
	}
	es, ok := c.Body[0].(*syntax.ExprStmt)
	if !ok {
		return "", "", false
	}
	if id, ok := es.X.(*syntax.Ident); !ok || id.Name != as.Name.Name {
		return "", "", false
	}
	return v.Name, as.Name.Name, true
}

// resultUnion is the union a function declares as its result, and the
// names of its failure row; ok is false for no readable union.
func (p *flatPkg) resultUnion(fn *syntax.FuncDecl) (string, []string, bool) {
	if fn.Body == nil || fn.Derived != nil || fn.Extern != nil || fn.TypeParams != nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return "", nil, false
	}
	id, ok := fn.Type.Results.List[0].Type.(*syntax.Ident)
	if !ok {
		return "", nil, false
	}
	if _, ok := p.variants(id.Name); !ok {
		return "", nil, false
	}
	var fails []string
	for _, f := range fn.Type.Results.List[1:] {
		if row, ok := f.Type.(*syntax.ErrorRow); ok {
			for _, t := range row.Types {
				if n, ok := t.(*syntax.Ident); ok {
					fails = append(fails, n.Name)
				}
			}
		}
	}
	return id.Name, fails, true
}

// funcs are the package's functions with a body, in file and source
// order, with their files.
func (p *flatPkg) funcs(each func(file string, fn *syntax.FuncDecl) (flatRewrite, bool)) (flatRewrite, bool) {
	for _, file := range p.files {
		for _, d := range p.parsed[file].Decls {
			if fn, ok := d.(*syntax.FuncDecl); ok {
				if r, ok := each(file, fn); ok {
					return r, true
				}
			}
		}
	}
	return flatRewrite{}, false
}

// tailMatch is the match of one subject that ends fn's body, or nil.
func tailMatch(fn *syntax.FuncDecl) *syntax.TypeSwitchStmt {
	if len(fn.Body.List) == 0 {
		return nil
	}
	m, ok := fn.Body.List[len(fn.Body.List)-1].(*syntax.TypeSwitchStmt)
	if !ok || len(m.Subjects) != 1 {
		return nil
	}
	return m
}

// identityArms is the first tail match whose identity arms, in a row,
// hand out exactly the variants of a union A the result holds: they
// become one arm of A.
func (p *flatPkg) identityArms() (flatRewrite, bool) {
	return p.funcs(func(file string, fn *syntax.FuncDecl) (flatRewrite, bool) {
		r, fails, ok := p.resultUnion(fn)
		if !ok {
			return flatRewrite{}, false
		}
		m := tailMatch(fn)
		if m == nil {
			return flatRewrite{}, false
		}
		vr, _ := p.variants(r)
		names, sets := p.subUnions(vr, "", false)
		var rowNames []string
		var rowSets [][]string
		var rowAt []int
		for i, a := range names {
			if slices.ContainsFunc(sets[i], func(v string) bool { return slices.Contains(fails, v) }) {
				continue
			}
			if at := armRun(m.Clauses, sets[i]); at >= 0 {
				rowNames, rowSets, rowAt = append(rowNames, a), append(rowSets, sets[i]), append(rowAt, at)
			}
		}
		k := pick(rowSets)
		if k < 0 {
			return flatRewrite{}, false
		}
		a, n, at := rowNames[k], len(rowSets[k]), rowAt[k]
		first, last := m.Clauses[at], m.Clauses[at+n-1]
		_, binder, _ := identityArm(first)
		src := p.texts[file]
		sep := src[first.Colon+1 : first.Body[0].Pos()]
		text := "case " + a + " as " + binder + ":" + sep + binder
		return flatRewrite{
			edits: map[string][]edit{file: {{from: int(first.Pos()), to: int(last.End()), text: text}}},
			what:  fmt.Sprintf("%s: %s's arms of %s are one arm", file, fn.Name.Name, a),
		}, true
	})
}

// armRun is where clauses hold, in a row, one identity arm for each
// variant of set and no other arm, or -1.
func armRun(clauses []*syntax.CaseClause, set []string) int {
	for i := 0; i+len(set) <= len(clauses); i++ {
		var got []string
		for _, c := range clauses[i : i+len(set)] {
			v, _, ok := identityArm(c)
			if !ok || slices.Contains(got, v) {
				break
			}
			got = append(got, v)
		}
		if len(got) != len(set) {
			continue
		}
		all := true
		for _, v := range set {
			if !slices.Contains(got, v) {
				all = false
			}
		}
		if all {
			return i
		}
	}
	return -1
}

// identityWidening is the first function `func F(s A) R { match s { case
// A as v: v } }`, R a union that holds A's variants: its body becomes
// `{ s }`.
func (p *flatPkg) identityWidening() (flatRewrite, bool) {
	return p.funcs(func(file string, fn *syntax.FuncDecl) (flatRewrite, bool) {
		r, _, ok := p.resultUnion(fn)
		if !ok || len(fn.Body.List) != 1 {
			return flatRewrite{}, false
		}
		params := fn.Type.Params
		if params == nil || len(params.List) != 1 || len(params.List[0].Names) != 1 || params.List[0].Own != nil || params.List[0].Mut != nil {
			return flatRewrite{}, false
		}
		s := params.List[0].Names[0].Name
		at, ok := params.List[0].Type.(*syntax.Ident)
		if !ok {
			return flatRewrite{}, false
		}
		va, ok := p.variants(at.Name)
		if !ok {
			return flatRewrite{}, false
		}
		m := tailMatch(fn)
		if m == nil || len(m.Clauses) != 1 {
			return flatRewrite{}, false
		}
		if subj, ok := m.Subjects[0].X.(*syntax.Ident); !ok || subj.Name != s {
			return flatRewrite{}, false
		}
		c := m.Clauses[0]
		as, ok := c.List[0].(*syntax.AsPattern)
		if _, _, isID := identityArm(c); !isID || !ok || as.X.(*syntax.Ident).Name != at.Name {
			return flatRewrite{}, false
		}
		vr, _ := p.variants(r)
		for _, v := range va {
			if !slices.Contains(vr, v) {
				return flatRewrite{}, false
			}
		}
		return flatRewrite{
			edits: map[string][]edit{file: {{from: int(fn.Body.Lbrace), to: int(fn.Body.Rbrace) + 1, text: "{ " + s + " }"}}},
			what:  fmt.Sprintf("%s: %s returns %s", file, fn.Name.Name, s),
		}, true
	})
}

// UnionFlatReport is what RunUnionFlat found and did.
type UnionFlatReport struct {
	Files   map[string]string // the edited files' new texts
	Changes []string
	Errs    []string
}

// Text is the report's summary: each rewrite, and each package the pass
// could not rewrite.
func (r UnionFlatReport) Text() string {
	if len(r.Changes) == 0 && len(r.Errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "unions: %d rewrites in %d files\n", len(r.Changes), len(r.Files))
	for _, c := range r.Changes {
		b.WriteString("  " + c + "\n")
	}
	for _, e := range r.Errs {
		b.WriteString("not rewritten: " + e + "\n")
	}
	return b.String()
}

// RunUnionFlat applies UnionFlat to each package under opts.Paths (each
// package folder of a module, and each other .ergo file alone) and writes
// the files when opts.Write. A package whose files do not parse is left
// alone: the union pass reads declarations, not a parse error. A
// package's rewrites are kept only when the package checks with the same
// diagnostics after them as before (by code and message): the pass reads
// names, not types, so the checker has the last word on what it wrote.
func RunUnionFlat(opts Options) (UnionFlatReport, error) {
	rep := UnionFlatReport{Files: map[string]string{}}
	one := func(dir string, files []string) error {
		texts := map[string]string{}
		for _, f := range files {
			if abs, err := filepath.Abs(f); err == nil {
				f = abs
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			texts[f] = string(b)
		}
		out, changes, err := UnionFlat(texts)
		if err != nil {
			if strings.Contains(err.Error(), "rewritten") || strings.Contains(err.Error(), "settle") {
				rep.Errs = append(rep.Errs, strings.Join(files, ", ")+": "+err.Error())
			}
			return nil
		}
		if len(changes) == 0 {
			return nil
		}
		before, err := flatVerdict(dir, texts)
		if err != nil {
			return err
		}
		after, err := flatVerdict(dir, out)
		if err != nil {
			return err
		}
		if !slices.Equal(before, after) {
			rep.Errs = append(rep.Errs, strings.Join(files, ", ")+": the rewrite would change the check verdict, so it is not made: "+strings.Join(changes, "; "))
			return nil
		}
		rep.Changes = append(rep.Changes, changes...)
		for f, text := range out {
			if text != texts[f] {
				rep.Files[f] = text
			}
		}
		return nil
	}
	var werr error
	done := map[string]bool{} // a package folder is read once
	for _, root := range opts.Paths {
		// walk matches a module's package folders by their absolute paths:
		// a relative root would read each package file again, alone.
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		err := walk(root, func(file string) {
			if werr == nil {
				werr = one("", []string{file})
			}
		}, func(mod string) {
			targets, err := sketchfront.DiscoverPackages(mod)
			if err != nil {
				return
			}
			for _, t := range targets {
				if werr == nil && !done[t.Dir] {
					done[t.Dir] = true
					werr = one(t.Dir, ergoFiles(t.Dir))
				}
			}
		})
		if err != nil {
			return rep, err
		}
		if werr != nil {
			return rep, werr
		}
	}
	if opts.Write {
		for file, text := range rep.Files {
			info, err := os.Stat(file)
			if err != nil {
				return rep, err
			}
			if err := os.WriteFile(file, []byte(text), info.Mode().Perm()); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// flatVerdict is the diagnostics of the package in dir over texts (or of
// the one file of texts, for no dir), each as code and message, sorted.
func flatVerdict(dir string, texts map[string]string) ([]string, error) {
	var ds []report.Diagnostic
	if dir == "" {
		for _, text := range texts {
			ds = sketchfront.CheckOptions(text, check.Options{}).Diagnostics
		}
	} else {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		a, err := sketchfront.CheckDirOptions(abs, texts, check.Options{})
		if err != nil {
			return nil, err
		}
		ds = a.Diagnostics
	}
	var out []string
	for _, d := range ds {
		out = append(out, d.Code+" "+d.Message)
	}
	slices.Sort(out)
	return out, nil
}

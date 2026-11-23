package lower

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Call-site ids (types.md, Call sites 6; MAP-INT rule 7). The CallSite a
// call passes for a final parameter it leaves out carries Pkg and Id:
// Pkg is the number of the call's package among the packages of the
// program, and Id the number of the call's position among the positions
// of that package where a call starts, in order of file path, line and
// column, both from 1. So two calls at one position (a call whose callee
// is a call, f(x)(y)) share their Pkg and Id as they share their File,
// Line and Column, two positions never share both, and the numbers are
// the same in every lowering of the program, whatever order it asks in
// (the flat unions' second lowerer, the IR build). A CallSite the
// program writes keeps the Pkg and Id it writes, 0 by convention: no
// compiler-passed site has either 0.
//
// An edit of one package moves only that package's Ids, and the package
// numbers move only when the program gains or loses a package. The
// lowered text of a package names its number, never writes it: each
// package that passes a site has one package variable, ergoSitePkg_<name>
// = n, which the split places in the shared package (pkgsplit.go,
// sitePkgNames), so the text of every other package stays byte-equal
// when a package is added, removed or edited (TestSitesStayInTheirPackage).
// The packages are numbered by their last path element, then their path,
// then std before the program's, so a std package and its copy as a user
// package (stdlib's TestStdCopyAgrees) take one number.
//
// Prior art: Compose's compiler keys a group by an integer it derives from
// the call's source position (a hash of the file and offset), so the
// runtime's slot table compares ints; React keys by the hook's call order
// within a component instead, which needs no compiler. Rust's incremental
// compilation keeps a crate's ids local to it (DefIndex within a CrateNum)
// for the same reason: an edit of one crate renumbers nothing in another.

// siteKey is a call's position: its file's path, line and column.
type siteKey struct {
	path         string
	line, column int
}

// siteNum is the number a call position takes: its package's (an index
// of sitePkgs, from 1) and its own within the package (from 1).
type siteNum struct{ pkg, id int }

// sitePkg is one package of the program in the numbering: its key (the
// pkgKey of its variable), its sort string, and whether a site of the
// lowering names its variable (sitePkgDecls writes only those).
type sitePkg struct {
	pkg   *check.Package
	key   string
	order string
	used  bool
}

// sitePackages are the program's packages in the numbering's order, the
// one at index i numbered i+1: the sourced packages and the std packages
// the program reaches.
func (l *lowerer) sitePackages() []sitePkg {
	if l.sitePkgs != nil {
		return l.sitePkgs
	}
	var out []sitePkg
	seen := map[*check.Package]bool{}
	add := func(p *check.Package) {
		if p == nil || seen[p] {
			return
		}
		seen[p] = true
		kind := "1"
		if p.Std() {
			kind = "0"
		}
		out = append(out, sitePkg{pkg: p, key: kind + ":" + p.Path, order: path.Base(p.Path) + "\x00" + p.Path + "\x00" + kind})
	}
	for _, s := range l.srcs {
		add(s.pkg)
	}
	for _, p := range l.libraries() {
		if p.Std() {
			add(p)
		}
	}
	slices.SortFunc(out, func(a, b sitePkg) int { return strings.Compare(a.order, b.order) })
	l.sitePkgs = out
	return out
}

// sitePkgIndex is the index in sitePackages of p, -1 for none.
func (l *lowerer) sitePkgIndex(p *check.Package) int {
	return slices.IndexFunc(l.sitePackages(), func(s sitePkg) bool { return s.pkg == p })
}

// reserveSitePkgs reserves the variable of each package's number, base
// ergoSitePkg_ and the package's last path element.
func (l *lowerer) reserveSitePkgs(add func(key pkgKey, sort, base string)) {
	for _, s := range l.sitePackages() {
		add(pkgKey{"site package", s.key}, "site package "+s.order, "ergoSitePkg_"+goIdentPart(path.Base(s.pkg.Path)))
	}
}

// goIdentPart is s with every byte that a Go identifier cannot hold as _.
func goIdentPart(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '_'
		}
	}
	return string(b)
}

// siteNumber is the Pkg and Id of the CallSite the call x passes.
func (l *lowerer) siteNumber(x *syntax.CallExpr) siteNum {
	if l.siteIDs == nil {
		l.siteIDs = l.numberSites()
	}
	path, line, column := l.locate(x, x.Pos())
	n, ok := l.siteIDs[siteKey{path, line, column}]
	if !ok {
		ice(x, "a call site the numbering did not reach")
	}
	return n
}

// sitePkgName is the variable that holds the number of the package of the
// call x's site, which the lowering then declares (sitePkgDecls).
func (l *lowerer) sitePkgName(x *syntax.CallExpr) string {
	i := l.siteNumber(x).pkg - 1
	pkgs := l.sitePackages()
	pkgs[i].used = true
	return l.pkgName(pkgKey{"site package", pkgs[i].key}, "ergoSitePkg_"+goIdentPart(path.Base(pkgs[i].pkg.Path)))
}

// sitePkgDecls declares the number of each package a site of the
// lowering names, in the numbering's order.
func (l *lowerer) sitePkgDecls() string {
	var b strings.Builder
	for i, s := range l.sitePkgs {
		if s.used {
			b.WriteString("var " + l.pkgNames[pkgKey{"site package", s.key}] + " = " + strconv.Itoa(i+1) + "\n\n")
		}
	}
	return b.String()
}

// numberSites numbers every position where a call starts, in the
// program's sources and the std files it reaches, within its package.
func (l *lowerer) numberSites() map[siteKey]siteNum {
	pkgs := l.sitePackages()
	keys := make([][]siteKey, len(pkgs))
	seen := map[siteKey]bool{}
	index := func(p *check.Package) int {
		i := l.sitePkgIndex(p)
		if i < 0 {
			ice(nil, "a call site in a package the numbering does not hold")
		}
		return i
	}
	add := func(i int, k siteKey) {
		if !seen[k] {
			seen[k] = true
			keys[i] = append(keys[i], k)
		}
	}
	for _, s := range l.srcs {
		i := index(s.pkg)
		syntax.Inspect(s.file, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok {
				path, line, column := l.locate(x, x.Pos())
				add(i, siteKey{path, line, column})
			}
			return true
		})
	}
	// The std files are walked here, once and only when a call passes a
	// site: stdFileOf finds one node's file lazily.
	for _, p := range l.libraries() {
		if !p.Std() {
			continue
		}
		i := index(p)
		for _, f := range p.Files() {
			path := "std/" + p.Path + "/" + f.Name
			if p.Path == stdlib.PreludePath {
				path = "std/" + f.Name
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CallExpr); ok {
					line, column := f.Position(x.Pos())
					add(i, siteKey{path, line, column})
				}
				return true
			})
		}
	}
	ids := make(map[siteKey]siteNum, len(seen))
	for i, ks := range keys {
		slices.SortFunc(ks, func(a, b siteKey) int {
			if a.path != b.path {
				return strings.Compare(a.path, b.path)
			}
			if a.line != b.line {
				return a.line - b.line
			}
			return a.column - b.column
		})
		for j, k := range ks {
			ids[k] = siteNum{pkg: i + 1, id: j + 1}
		}
	}
	return ids
}

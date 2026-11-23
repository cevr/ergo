package lower

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
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
// package that passes a site has one package variable, ergo__SitePkg_<name>
// = n, which the split places in the shared package (pkgsplit.go,
// sitePkgNames), so the text of every other package stays byte-equal
// when another package's site numbering changes (TestSitesStayInTheirPackage).
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

// siteKey is a position in one source file. A sourced file and an embedded
// file can display the same path without becoming the same call position.
type siteKey struct {
	file *syntax.File
	at   syntax.Pos
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
	base  string
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
		kind, base := "0", "ergoSitePkg_"
		if l.isSourced(p) {
			// The two source namespaces can contain the same import path.
			// '__' cannot occur in pkgAlias's encoding of a path.
			kind, base = "1", "ergoSitePkg_src__"
		}
		out = append(out, sitePkg{pkg: p, key: kind + ":" + p.Path, base: base + pkgAlias(p.Path), order: path.Base(p.Path) + "\x00" + p.Path + "\x00" + kind})
	}
	for _, s := range l.srcs {
		add(s.pkg)
	}
	for _, p := range l.libraries() {
		add(p)
	}
	slices.SortFunc(out, func(a, b sitePkg) int { return strings.Compare(a.order, b.order) })
	l.sitePkgs = out
	return out
}

// reserveSitePkgs names one variable per package, by the whole path:
// adding a package of the same basename does not change either name.
func (l *lowerer) reserveSitePkgs(add func(key pkgKey, sort, base string)) {
	for _, s := range l.sitePackages() {
		add(pkgKey{"site package", s.key}, "site package "+s.order, s.base)
	}
}

// sitePkgNames names the generated bindings that belong to assembly, not to
// any one of the packages that reads them.
func (l *lowerer) sitePkgNames() map[string]bool {
	names := make(map[string]bool)
	for _, s := range l.sitePkgs {
		if s.used {
			names[l.pkgNames[pkgKey{"site package", s.key}]] = true
		}
	}
	return names
}

// callSite returns the displayed position and identity passed by call x.
func (l *lowerer) callSite(x *syntax.CallExpr) (file string, line, column int, pkg string, id int) {
	if l.siteIDs == nil {
		l.siteIDs = l.numberSites()
	}
	var f *syntax.File
	if i, ok := l.sourceOf(x); ok {
		f, file = l.srcs[i].file, l.srcs[i].path
	} else if sf, ok := l.stdFileOf(x); ok {
		f, file = sf.file, sf.path
	}
	n, ok := l.siteIDs[siteKey{f, x.Pos()}]
	if !ok {
		ice(x, "a call site the numbering did not reach")
	}
	line, column = f.Position(x.Pos())
	s := &l.sitePkgs[n.pkg-1]
	s.used = true
	return file, line, column, l.pkgName(pkgKey{"site package", s.key}, s.base), n.id
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
	type siteFile struct {
		path string
		file *syntax.File
	}
	var files []siteFile
	var offsets []syntax.Pos
	ids := make(map[siteKey]siteNum)
	for i, p := range pkgs {
		files = files[:0]
		if l.isSourced(p.pkg) {
			for _, s := range l.srcs {
				if s.pkg == p.pkg {
					files = append(files, siteFile{s.path, s.file})
				}
			}
		} else {
			for _, f := range p.pkg.Files() {
				files = append(files, siteFile{embeddedSourcePath(p.pkg, f.Name), f})
			}
		}
		slices.SortFunc(files, func(a, b siteFile) int { return strings.Compare(a.path, b.path) })
		id := 0
		for _, f := range files {
			offsets = offsets[:0]
			syntax.Inspect(f.file, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CallExpr); ok {
					offsets = append(offsets, x.Pos())
				}
				return true
			})
			// Within a file the offset orders positions by line and byte
			// column. Chained calls can start at the same offset.
			slices.Sort(offsets)
			for _, at := range slices.Compact(offsets) {
				id++
				ids[siteKey{f.file, at}] = siteNum{pkg: i + 1, id: id}
			}
		}
	}
	return ids
}

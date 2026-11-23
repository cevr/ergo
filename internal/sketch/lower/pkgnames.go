package lower

import (
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
)

// Package-scope names (NAME-LOCAL; the rule S4's per-package builder
// reads, baton selfhost/S4c/naming.md). A Go name is a function of its
// declaration and its own package, never of what else the program
// reaches, so adding a package to a program renames nothing in the others
// and a function's IR and Go text do not move:
//
//  1. The root package's declarations keep their Ergo spelling, unless a
//     Go reserved name, a fixed generated name (fixedPkgNames) or an
//     earlier declaration of the root holds it; then the smallest free
//     name_N.
//  2. Any other package's declaration is <alias>__<name>: name is its
//     spelling, numbered as in rule 1 among its own package's
//     declarations only (pkgNamer), and alias is its import path,
//     encoded (pkgAlias). S109 keeps "__" out of every Ergo name, so the
//     spelling is one no local and no root declaration can take. The
//     pair (alias, name) is what the split build prints as alias.name;
//     the one-package build joins it with "__".
//  3. A generated entity for a subject (an adapter, a text function, a
//     copy, a machine's initial state, …) is ergo__<base>, and
//     ergo__<alias>__<base> when the subject belongs to a package other
//     than the root: again a spelling under "__".
//  4. The fixed generated names (the runtime's and Go's imports, the
//     environment type, the call tables, …) are constants of the
//     lowering: every build reserves them, so they are part of what a
//     local or a root declaration yields to, like Go's reserved names.
//  5. The prelude's declarations are bare and reserved like rule 4's: the
//     prelude is the same in every program.
//  6. A Go package of an import go, a user extern or the lowering's own
//     use, other than rule 4's, is imported as go__<alias of its path>
//     (HostImportSpelling): a function of the path alone, so which Go
//     packages other packages import renames nothing.
//
// So a local of a function's own body (emitBody, plainScope) yields only
// to rules 4 and 5, its own package's declarations and its body's names.
// Prior art: Gleam and ReScript emit one host module per source module
// and qualify a cross-module name by the module; OCaml's flambda names a
// symbol by its compilation unit (Unit__name), the spelling rule 2 joins.

// fixedPkgNames are the fixed generated names (rule 4), besides the
// runtime's leaves (reserveLeaves).
var fixedPkgNames = map[string]bool{
	"ergo": true, "rt": true, "time": true, "embed": true, "strconv": true, "maps": true, "slices": true,
	"Env": true, "hostConsole": true, "ergoIs": true, "ergoNilTag": true, "ergoCall": true, "ergoCalls": true,
	"ergoCallNames": true, "ergoDecl": true, "ergoDecls": true, "ergoInlines": true, "ergoInit": true,
	"ergoMachineGraph": true, "ergoMachinePair": true, "ergoOrdered": true, "ergoNumber": true,
}

// reserveLeaves makes the runtime's leaves, the packages of leaves (the
// naming pass passes v1.Leaves), fixed names of rule 4: each leaf's
// import is spelled leafName, before any declaration is named.
func (l *lowerer) reserveLeaves(leaves []string) {
	l.leafImports, l.leafNames = map[string]bool{}, map[string]bool{}
	for _, leaf := range leaves {
		pkg := runtimeImport + "/" + leaf
		l.leafImports[pkg] = true
		l.leafNames[leafName(pkg)] = true
		l.fixed.used[leafName(pkg)] = true
	}
}

// fixedName reports a fixed generated name (rule 4).
func (l *lowerer) fixedName(base string) bool { return fixedPkgNames[base] || l.leafNames[base] }

// fixedImport reports the import path of a fixed name (rule 4): the
// runtime, its leaves, its task package, and the Go packages lowered
// code names itself.
func (l *lowerer) fixedImport(path any) bool {
	p, _ := path.(string)
	switch p {
	case runtimeImport, taskImport, "time", "embed", "strconv", "maps", "slices":
		return true
	}
	return l.leafImports[p]
}

// fixedKinds are the kinds of the keys rule 4 names.
var fixedKinds = map[string]bool{
	"import": true, "env": true, "host": true, "is": true, "niche tag": true, "call": true, "calls": true,
	"callnames": true, "decl": true, "decls": true, "inlines": true, "init": true, "machine graphs": true,
	"machine pair": true, "constraint": true,
}

// declKinds are the kinds of the keys of a package's own declarations.
var declKinds = map[string]bool{"func": true, "type": true, "let": true, "mono": true}

// newPkgNamers makes the lowering's namers: fixed holds rules 4 and 5,
// and the program namer below it every package-scope name.
func newPkgNamers() (fixed, program *namer) {
	fixed = newNamer(nil)
	for name := range fixedPkgNames {
		fixed.used[name] = true
	}
	return fixed, newNamer(fixed)
}

// packageScopeName is the Go name of the package-scope entity key, whose
// preferred spelling is base.
func (l *lowerer) packageScopeName(key pkgKey, base string) string {
	if key.kind == "import" && l.fixedImport(key.obj) || key.kind != "import" && fixedKinds[key.kind] && l.fixedName(base) {
		return base // rule 4
	}
	if key.kind == "import" {
		// A Go package an extern, an import go or the lowering itself
		// names (rule 6): go__<alias of its path>, a function of the path
		// alone, under "__" so no declaration or local takes it.
		path, _ := key.obj.(string)
		name := l.names.alloc(HostImportSpelling(path))
		l.fixed.used[name] = true
		return name
	}
	owner := l.keyPkg(key)
	switch {
	case declKinds[key.kind] && owner != nil && owner.Prelude():
		name := l.names.alloc(base) // rule 5
		l.fixed.used[name] = true
		return name
	case declKinds[key.kind] && (owner == nil || owner == l.pkg), key.kind == "export":
		return l.names.alloc(base) // rule 1
	case declKinds[key.kind]:
		name := pkgAlias(owner.Path) + "__" + l.pkgNamer(owner).alloc(base) // rule 2
		l.names.used[name] = true
		return name
	}
	return l.helperName(owner, base) // rule 3
}

// namePrelude names the prelude's types and functions first (rule 5), so
// a root declaration of one of their spellings yields to them.
func (l *lowerer) namePrelude() {
	for _, p := range l.libraries() {
		if !p.Prelude() {
			continue
		}
		for _, tn := range l.canon.types(p) {
			l.typeName(tn)
		}
		for _, f := range l.canon.funcs(p) {
			l.pkgName(pkgKey{"func", f}, f.Name)
		}
	}
}

// helperName is the name of a generated entity for a subject of package
// owner (nil: of the program) whose base name is base (rule 3).
func (l *lowerer) helperName(owner *check.Package, base string) string {
	alias := ""
	if owner != nil && owner != l.pkg && !owner.Prelude() {
		alias = pkgAlias(owner.Path)
	}
	return l.names.alloc(HelperSpelling(alias, base))
}

// HelperSpelling is rule 3's spelling of a generated entity whose base
// name is base, for a subject of the package alias ("" for the root's or
// the program's): ergo__[alias__]base, with base's own "ergo" prefix
// dropped. The name the lowering gives is it, or it with a _N suffix when
// one subject has two entities of one base. A test that looks for a
// generated function by name spells it with this.
func HelperSpelling(alias, base string) string {
	if rest, ok := strings.CutPrefix(base, "ergo"); ok && rest != "" && unicode.IsUpper(rune(rest[0])) {
		base = rest
	}
	if alias != "" {
		base = alias + "__" + base
	}
	return "ergo__" + base
}

// pkgNamer is the namer of package p's own declarations (rule 2), below
// the fixed names.
func (l *lowerer) pkgNamer(p *check.Package) *namer {
	if l.pkgNamers == nil {
		l.pkgNamers = map[*check.Package]*namer{}
	}
	n := l.pkgNamers[p]
	if n == nil {
		n = newNamer(l.fixed)
		l.pkgNamers[p] = n
	}
	return n
}

// plainScope is the parent scope of the locals of f's own body: the root
// package's names for a root function, else f's package's own
// declarations (rule 2's names, which its body calls joined, so a local
// of that spelling captures nothing; they are kept so the body's names
// are the ones its package alone decides).
func (l *lowerer) plainScope(f *check.Func) *namer {
	if f.Pkg == nil || f.Pkg == l.pkg || f.Pkg.Prelude() {
		return l.names
	}
	return l.pkgNamer(f.Pkg)
}

// keyPkg is the package of key's subject, nil when it has none.
func (l *lowerer) keyPkg(key pkgKey) *check.Package {
	switch o := key.obj.(type) {
	case *check.Func:
		return o.Pkg
	case *check.TypeName:
		return o.Pkg
	case *check.Named:
		if o.Obj != nil {
			return o.Obj.Pkg
		}
	case *check.Var:
		return l.infoPkg()
	}
	return nil
}

// infoPkg is the package whose facts the lowering reads now (l.info): a
// let is named while its package's declarations are (declareName).
func (l *lowerer) infoPkg() *check.Package {
	if l.info == l.pkg.Info {
		return l.pkg
	}
	for _, p := range l.libraries() {
		if p.Info == l.info {
			return p
		}
	}
	return nil
}

// PackageAlias is rule 2's spelling of the import path in a joined name
// (pkgAlias). A test that compares two programs that hold one package at
// two paths (std's copy test) folds one alias into the other with it.
func PackageAlias(path string) string { return pkgAlias(path) }

// HostImportSpelling is rule 6's Go name of an import of the Go package
// path. A test that reads a lowered call of a Go package spells it so.
func HostImportSpelling(path string) string { return "go__" + pkgAlias(path) }

// pkgAlias is the spelling of an import path in a joined name (rule 2): a
// function of the path alone, one to one, with no "__" in it, and never
// "ergo", the generated names' own prefix (rule 3). '/', '.', '-' and
// '_' become "_s", "_d", "_h" and "_u", so an underscore is always
// followed by a letter; "ergo" itself is "ergo_e", which no path gives.
func pkgAlias(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch r {
		case '/':
			b.WriteString("_s")
		case '.':
			b.WriteString("_d")
		case '-':
			b.WriteString("_h")
		case '_':
			b.WriteString("_u")
		default:
			b.WriteRune(r)
		}
	}
	if b.String() == "ergo" {
		return "ergo_e"
	}
	return b.String()
}

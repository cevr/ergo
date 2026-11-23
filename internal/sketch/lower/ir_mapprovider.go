package lower

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// Maps keyed through a provider (CONSTRAINTS C5; types.md, Maps). A Map
// with a provider other than Structural is the prelude's record
// hashTable, so its type, its operations and its copies are those of an
// ordinary generic record and its functions; this file lowers only what
// is not Ergo source: the predeclared hash.

// hashSeedName is the package-scope Go name of the build's hash seed.
const hashSeedName = "ergoHashSeed"

// reserveHash reserves the names hash's lowering uses: Go's maphash
// import and the seed, one per process (maphash.MakeSeed). Go's maphash
// takes no seed from a number, so ergo test cannot fix it: no output of a
// program, Map order, codec or law verdict reads a hash beyond its
// equality to another hash of the same process.
func (l *lowerer) reserveHash() {
	l.pkgName(pkgKey{"import", "hash/maphash"}, "maphash")
	l.pkgName(pkgKey{"hash seed", nil}, hashSeedName)
}

// hashCall is hash(k): Go's maphash.Comparable of k under the seed, which
// hashes k as Go's == compares it (-0.0 and 0.0 hash equal; every field
// of a record is read).
func (b *irBuilder) hashCall(x *syntax.CallExpr, p irPos) ([]*irStmt, *irExpr, bool) {
	if len(x.Args) != 1 {
		irNo("hash form")
	}
	t := b.l.info.Types[x.Args[0]]
	if t == nil {
		irNo("hash of an untyped value")
	}
	pre, k := b.value(x.Args[0])
	seed := b.l.hashSeed()
	call := &irExpr{K: "pcall", Pos: p, T: "hash/maphash", Name: "Comparable", V: b.goType(t, x.Args[0]),
		Args: []*irExpr{{K: "global", Pos: p, Name: seed}, k}}
	return pre, call, false
}

// hashSeed is the Go name of the build's hash seed, declared once: among
// the declarations an IR body keeps (withKeeps) when the IR lowers, else
// in the emitter's generated declarations.
func (l *lowerer) hashSeed() string {
	pkg := l.goPkg("hash/maphash")
	seed := l.pkgName(pkgKey{"hash seed", nil}, hashSeedName)
	decl := "var " + seed + " = " + pkg + ".MakeSeed()"
	switch {
	case l.ir != nil:
		if l.ir.keep == nil {
			l.ir.keep = map[string]bool{}
		}
		l.ir.keep[decl] = true
	case !l.hashSeeded:
		l.hashSeeded = true
		l.gen.WriteString(decl + "\n\n")
	}
	return seed
}

// hashCall is the emitter's hash(k), for a body the IR does not take (a
// fallback, and every body of a census): the IR's call, Go's
// maphash.Comparable of k at k's type under the seed.
func (fc *funcCtx) hashCall(x *syntax.CallExpr) operand {
	if len(x.Args) != 1 {
		ice(x, "hash with %d arguments", len(x.Args))
	}
	k := fc.value(x.Args[0])
	seed := fc.l.hashSeed()
	return operand{val: fc.l.goPkg("hash/maphash") + ".Comparable[" + fc.goType(fc.typeOf(x.Args[0])) + "](" + seed + ", " + k + ")"}
}

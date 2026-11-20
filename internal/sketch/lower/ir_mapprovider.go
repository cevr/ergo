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
	pkg := b.l.goPkg("hash/maphash")
	seed := b.l.pkgName(pkgKey{"hash seed", nil}, hashSeedName)
	b.keepImport("var " + seed + " = " + pkg + ".MakeSeed()")
	call := &irExpr{K: "pcall", Pos: p, T: "hash/maphash", Name: "Comparable", V: b.goType(t, x.Args[0]),
		Args: []*irExpr{{K: "global", Pos: p, Name: seed}, k}}
	return pre, call, false
}

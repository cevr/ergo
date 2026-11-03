package check

// Package variables that refer to parsed nodes and hold none by value stay
// clean (E2d round 1 review B1's negative control): a node pointer, a
// syntax.Node interface, and a slice and a map of node pointers, assigned
// from a parse result at a listed Parse site.

import "ergo.local/prototype/internal/sketch/syntax"

var (
	acceptParsedFile  *syntax.File
	acceptParsedName  *syntax.Ident
	acceptParsedNode  syntax.Node
	acceptParsedDecls []syntax.Decl
	acceptParsedNames map[string]*syntax.Ident
)

func acceptGlobalParsed() {
	f, err := syntax.Parse("type T int\n")
	if err != nil {
		return
	}
	acceptParsedFile, acceptParsedName, acceptParsedNode = f, f.Name, f
	acceptParsedDecls = f.Decls
	acceptParsedNames = map[string]*syntax.Ident{"T": f.Name}
}

func acceptGlobalRead() syntax.Node {
	if acceptParsedName != nil {
		return acceptParsedName
	}
	return acceptParsedNode
}

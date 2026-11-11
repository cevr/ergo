// Package hostsyntax is the self-hosting pilot's Go input boundary: it
// calls Ergo's Go parser and exports only the parser's facts, as a wire that
// Ergo code decodes (selfhost/tree). It makes no layout decision and does
// not import the formatter.
//
// Wire v1: the header "ergotree1\n", then one document. An item is
//
//	i<dec>;     an int
//	p<dec>;     a source position (a byte offset)
//	q           an absent position (syntax.NoPos)
//	-           an absent node (nil)
//	t f         a bool
//	s<len>:...  a string of len bytes, any bytes
//	c<id>;      a node, by its id in the document's node list
//	l<n>:...    a list of n items
//
// A document is D, a list of the line starts (ints), a list of nodes and
// the root node; or X, the source, then the syntax failure's code and
// message (strings) and its offset, length, line and column (ints); or U,
// the source and the name of a node type this adapter does not know. A
// node is n, its family (the Go type's name) as a string, then <n>: and n
// fields, each a name (a string) and an item: every exported field of the
// Go struct, in declaration order. Node ids follow a preorder walk from
// the root; a node reached twice keeps its first id.
package hostsyntax

import (
	"errors"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Header starts every wire of this version.
const Header = "ergotree1\n"

// Parse parses src with the Go parser and returns its wire.
func Parse(src string) string {
	f, err := syntax.Parse(src)
	if err != nil {
		var se *syntax.Error
		if !errors.As(err, &se) {
			// Parse returns only *syntax.Error; anything else is a defect.
			panic(err)
		}
		return failure(src, se)
	}
	return encode(f)
}

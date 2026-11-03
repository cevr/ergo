package e2bmaker

import "ergo.local/prototype/internal/sketch/syntax"

// Maker is an interface declared outside the checker whose method yields a
// node (E2b round 3 review M2).
type Maker interface {
	Make(*syntax.VarSpec) *syntax.VarSpec
}

package e2bdup

import "ergo.local/prototype/internal/sketch/syntax"

// Dup builds a node outside the checker (E2b round 3 review M2).
func Dup(s *syntax.VarSpec) *syntax.VarSpec { return &syntax.VarSpec{Names: s.Names} }

package check

import "ergo.local/prototype/internal/sketch/syntax"

// contextFails refuses an inline or a local context's header failure row,
// `context fails E { … }` (CTX-ROWS-1b's syntax): its entries' failures
// already escape at the with that builds it, each through its own try
// (rows.md, Context 3 and 9), and a written row for a literal is not
// checked, so it is refused, never read partly (Context 8). A named
// context's row is checked (contextneeds.go, CTX-ROWS-2).
func (c *checker) contextFails(row *syntax.ErrorRow) {
	if row != nil {
		c.errorf(row, CodeOutside, "an inline or a local context's fails clause is not checked: its entries' failures escape through their own try at the with (rows.md, Context 3 and 9); a fails row is a named context's header (Context 4)")
	}
}

package check

import "ergo.local/prototype/internal/sketch/syntax"

// contextFails refuses a context header's failure row, a named context's or
// a literal's: `context fails E | F { … }` parses (CTX-ROWS-1b) and is
// checked by CTX-ROWS-2 (FALLIBLE-CONTEXT, Design 1 and 2: its entries'
// failures within the row, a with handling or passing it on). Until then it
// is refused, never read partly (rows.md, Context 8).
func (c *checker) contextFails(row *syntax.ErrorRow) {
	if row != nil {
		c.errorf(row, CodeOutside, "a context's fails clause is CTX-ROWS-2's (the context header, rows.md Context 8); it is not checked yet")
	}
}

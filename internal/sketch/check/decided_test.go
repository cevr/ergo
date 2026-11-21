package check

import (
	"fmt"

	format "ergo.local/prototype/internal/hostsyntax"
)

// decided returns a source that a test generates in the decided syntax
// (#196), in canonical form. The generators write decided text directly
// (spec D-A6: no test oracle depends on the one-off translator). A source
// the formatter cannot read is a generator bug: decided panics, so the
// test fails at its generator.
func decided(src string) string {
	out, err := format.Source(src)
	if err != nil {
		panic(fmt.Sprintf("decided: the generated source does not parse: %v\n%s", err, src))
	}
	return out
}

// Decided is decided for the external tests.
var Decided = decided

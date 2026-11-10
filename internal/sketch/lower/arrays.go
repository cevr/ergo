package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
)

// Fixed arrays [N]T (196y; types.md, Arrays) lower to Go's [N]T. A Go
// array is a value already: an assignment, an argument and a capture copy
// it, so the value rule (values.go) adds a copy only where an element
// holds a slice or a Map, as it does for a record's field.

// arrayLen is the Go text of an array type's length.
func arrayLen(a *check.Array) string { return strconv.FormatInt(a.Len, 10) }

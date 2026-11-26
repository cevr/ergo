package lint

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// largeValueCopy is the speed hint for copying a big value on every turn
// of a loop (catalog PF18, "share a big table, do not copy it"): a for-in
// over a slice, array or map whose binding takes a value of more than
// option max bytes (default 256; the bytes of the lowered Go, without
// padding). Each turn copies the element into the binding.
var largeValueCopy = &Rule{
	Name:    "large-value-copy",
	Code:    "L148",
	Doc:     "A loop that binds elements bigger than max bytes (default 256) copies each one on every turn.",
	Instead: "loop over the indices and read the fields you need through the collection, or keep the large part behind a handle",
	Default: Hint,
	Check:   checkLargeValueCopy,
	Options: capOptions(256),
}

func checkLargeValueCopy(p *Pass) {
	max := p.Options.(maxOptions).Max
	for _, r := range p.fileIndex().ranges {
		switch underOf(p.Info.Types[r.X]).(type) {
		case *check.Slice, *check.Array, *check.Map:
		default:
			continue
		}
		for _, b := range [2]syntax.Expr{r.Key, r.Value} {
			id, ok := b.(*syntax.Ident)
			if !ok {
				continue
			}
			v, ok := p.Info.Defs[id].(*check.Var)
			if !ok {
				continue
			}
			if size := valueBytes(v.Type); float64(size) > max {
				p.Report(id.Pos(), id.End(), fmt.Sprintf("each turn copies a value of at least %d bytes into %s", size, id.Name))
			}
		}
	}
}

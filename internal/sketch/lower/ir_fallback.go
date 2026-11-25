package lower

// The switch that refuses the text fallback (FALLBACK-OFF): with
// Unit.NoFallback an IR lowering that keeps any body on the source
// emitter's text, because the IR builder stopped at a reason (finish),
// fails with every such body and its reason, the census's stop. F8 (the
// text path's removal) needs every example to build this way.

import (
	"fmt"
	"slices"
	"strings"
)

// FallbackRefused is an IR lowering with Unit.NoFallback that kept bodies
// on the text path: each with the reason the IR builder stopped at.
type FallbackRefused struct {
	Funcs []IRCensusFunc
}

func (e *FallbackRefused) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d function bodies fall back to the text path (-fallback=off refuses them; pass -fallback=on or ERGO_FALLBACK=on to allow text fallback):", len(e.Funcs))
	for _, f := range e.Funcs {
		at := f.Pkg
		if f.File != "" {
			at = f.File
		}
		fmt.Fprintf(&b, "\n\t%s (%s): %s", f.Name, at, strings.Join(f.Reasons, "; "))
	}
	return b.String()
}

// refuseFallbacks is the lowering's error when it kept bodies on the text
// path under Unit.NoFallback, else nil.
func (st *irState) refuseFallbacks() error {
	if !st.noFallback || len(st.fallbacks) == 0 {
		return nil
	}
	fs := slices.Clone(st.fallbacks)
	slices.SortStableFunc(fs, func(a, b IRCensusFunc) int {
		return strings.Compare(a.Pkg+"\x00"+a.Name, b.Pkg+"\x00"+b.Name)
	})
	return &FallbackRefused{Funcs: fs}
}

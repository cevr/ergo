package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/derive"
	"ergo.local/prototype/internal/sketch/regexpplan"
)

// Derived pattern matchers (patterns.md, Lowering). A pattern's bundle
// declares two matcher functions, the parse `matchT(s string)` and the
// find `scanT(s string, from int)`, whose results are the slot or event
// arrays of regexpplan (slots.go). Their Ergo bodies are placeholders: the
// lowering emits each one's Go from the plan the derive compiled, checked
// by regexpplan before it leaves that package, and never inlines it. The
// form is read from the derive record of the function's package, never from
// a name (internal/residue, TestLoweringKeysOnNoStdName).

// patternLowering selects the parse lowering of derived patterns: "" the
// engine the construction check selected (direct, table or NFA),
// "reference" the canonical NFA always. Find is the linear NFA scan in
// every mode. It is a string for -ldflags -X
// (ergo.local/prototype/internal/sketch/lower.patternLowering=reference).
// Tests may also force "direct" or "table": each plan with a deterministic
// candidate then emits that form, and any other plan its selected engine.
// The default is never a forced form.
var patternLowering = ""

// SetPatternLoweringForTest selects the pattern lowering ("", "reference",
// "direct" or "table") and returns the previous one.
func SetPatternLoweringForTest(mode string) string {
	prev := patternLowering
	patternLowering = mode
	return prev
}

// nativeOf is the matcher a derive declared as f, or nil.
func nativeOf(f *check.Func) *derive.Native {
	if f == nil || f.Pkg == nil || f.Pkg.Derive == nil || f.Decl == nil {
		return nil
	}
	return f.Pkg.Derive.Natives[f.Decl]
}

// emitNative writes the Go of the matcher n under the Go name name.
func (l *lowerer) emitNative(b *strings.Builder, name string, n *derive.Native, site *check.Func) {
	var src string
	var err error
	if n.Find {
		src, err = n.Plan.EmitFind(name)
	} else {
		engine := n.Plan.Inspect().Engine
		switch {
		case patternLowering == "reference":
			engine = regexpplan.NFA
		case (patternLowering == "direct" || patternLowering == "table") && n.Plan.Deterministic():
			engine = regexpplan.Engine(patternLowering)
		}
		src, err = n.Plan.EmitParse(name, engine)
	}
	if err != nil {
		ice(site.Decl.Name, "the matcher %s does not pass its construction check: %v", site.Name, err)
	}
	b.WriteString(strings.TrimSpace(src) + "\n\n")
}

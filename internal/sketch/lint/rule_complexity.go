package lint

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The complexity caps of the strict preset (q149: caps are a style
// choice, so they are opt-in; effect-oxlint maxCognitiveComplexity,
// complexity and maxHalsteadDifficulty, at the owner's thresholds). Each
// measures a function declaration's body (metrics.go) and takes the
// option max.

var cognitiveComplexity = &Rule{
	Name:    "cognitive-complexity",
	Code:    "L107",
	Doc:     "A function whose cognitive complexity (SonarSource) passes max (21) is hard to follow; split it.",
	Default: Warning,
	Check:   func(p *Pass) { checkCap(p, "cognitive complexity", cognitive) },
	Options: capOptions(21),
}

var cyclomaticComplexity = &Rule{
	Name:    "cyclomatic-complexity",
	Code:    "L108",
	Doc:     "A function whose cyclomatic complexity (McCabe) passes max (21) has too many paths to test; split it.",
	Default: Warning,
	Check:   func(p *Pass) { checkCap(p, "cyclomatic complexity", cyclomatic) },
	Options: capOptions(21),
}

var halsteadDifficulty = &Rule{
	Name:    "halstead-difficulty",
	Code:    "L109",
	Doc:     "A function whose Halstead difficulty passes max (79) is dense; name its parts.",
	Default: Warning,
	Check:   func(p *Pass) { checkCap(p, "Halstead difficulty", halstead) },
	Options: capOptions(79),
}

func checkCap(p *Pass, metric string, measure func(*syntax.BlockStmt) float64) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	max := p.Options.(maxOptions).Max
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil || f.Derived != nil {
			continue
		}
		if v := measure(f.Body); v > max {
			p.Report(f.Name.Pos(), f.Name.End(), fmt.Sprintf("%s of %s is %v, more than %v; split it into functions that each do one thing", metric, f.Name.Name, v, max))
		}
	}
}

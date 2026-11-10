package check

import "ergo.local/prototype/internal/sketch/syntax"

// checkFor checks src as one package and returns the checker, for the
// tests that read its deterministic counters.
func checkFor(src string) (*checker, []Diagnostic) {
	file, err := syntax.Parse(src)
	if err != nil {
		return nil, []Diagnostic{{Code: CodeSyntax, Message: err.Error()}}
	}
	c := newChecker("main", Std, prelude().scope)
	c.run([]*syntax.File{file})
	return c, c.sorted()
}

// TotalitySteps checks src as one package and returns the solver steps of
// each type switch, in check order, with the diagnostics. Steps are
// deterministic, so a test bounds them where a wall-clock bound would
// depend on the host's load.
func TotalitySteps(src string) ([]int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return nil, diags
	}
	return c.totalitySteps, diags
}

// TotalityBudgets checks src and returns the budget of each match's
// totality solver, in check order: proportional to written source (solverFor).
func TotalityBudgets(src string) []int {
	c, _ := checkFor(src)
	if c == nil {
		return nil
	}
	return c.totalityBudgets
}

// SolverSteps checks src and returns each match or irrefutable-pattern
// proof in check order: totality steps, reachability steps (zero for
// irrefutable patterns), and diagnostics.
func SolverSteps(src string) (total, useful []int, diags []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return nil, nil, diags
	}
	return c.totalitySteps, c.usefulSteps, diags
}

// PatternCost reports source-weighted proof work and allocated cells.
// This alias is test-only, like SolverSteps; it adds no production API.
type PatternCost = patternCost

func SolverCosts(src string) ([]PatternCost, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return nil, diags
	}
	return c.patternCosts, diags
}

// UsefulSteps checks src and returns the reachability steps of each type
// switch, in check order, with the diagnostics.
func UsefulSteps(src string) ([]int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return nil, diags
	}
	return c.usefulSteps, diags
}

// BodyGraphEdges checks src and returns the number of edges of its body
// graph (round-3 M5: linear in the source, never one per same-named
// method), with the diagnostics.
func BodyGraphEdges(src string) (int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, diags
	}
	return c.bodyEdges, diags
}

// VariantOps checks src and returns the variant-index operations that
// resolved its type-switch cases (round-5 M2: linear in the source), with
// the diagnostics.
func VariantOps(src string) (int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, diags
	}
	return c.variantOps, diags
}

// LookupWork checks src and returns the switches' variant operations (all
// charged to their step counters) and the identity work done inside case
// lookups: the type nodes the table built while a lookup ran, each one hash
// lookup of the node's own fields (class E). No recursive key is built in a
// lookup: typeString is for printing only (TestTypeStringOnlyPrints).
func LookupWork(src string) (ops, builds int, d []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, -1, diags
	}
	return c.variantOps, c.types.lookupBuilds, diags
}

// InstancesMade checks src and returns the generic instances it created
// (round-6 M5: linear in the source for a default-only switch over
// distinct instantiations of a generic union), with the diagnostics.
func InstancesMade(src string) (int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, diags
	}
	return c.instancesMade, diags
}

// InferOps checks src and returns the constraint-guard work of its generic
// calls (round-8 M4: linear in each signature), with the diagnostics.
func InferOps(src string) (int, []Diagnostic) {
	c, diags := checkFor(src)
	if c == nil {
		return -1, diags
	}
	return c.inferOps, diags
}

// SelfSettleOnly makes resolveSelfCalls skip its typing from empty rows,
// so the worklist of settleWaiting alone resolves the pending calls x.f(…) (a test
// of that path); it returns the function that restores the default.
func SelfSettleOnly() (restore func()) {
	selfSettleOnly = true
	return func() { selfSettleOnly = false }
}

// SetPureStdForTest counts every extern of the std package path as pure
// in the footprint (pureStdAlso; fu-pure-task) until restore runs. A std
// package imported before the call keeps the footprints it recorded.
func SetPureStdForTest(path string) (restore func()) {
	pureStdAlso = func(f *Func) bool { return f.Pkg.Path == path }
	return func() { pureStdAlso = nil }
}

package check

import "ergo.local/prototype/internal/sketch/syntax"

// The with budget (D16 as amended; #229 done-when 1; E5 round 2).
//
// A push of a with frame is the union of its context's summary with the
// target's with scope (capset.go withScope). It costs O(log) per frame on
// a chain of contexts, two chains in turn and the other shapes
// TestWithSiteWorkLinear counts, but O(m log) on `with A, B, A, B, …` of
// two contexts of m interleaved capabilities, or on `with Z1, …, Zn` whose
// Zi each embed one large context and add one capability: each push there
// changes the binder of m capabilities. So a push is repeatable work: each
// of its steps is charged before it runs against one budget per package,
// withBudgetPerByte × the package's source bytes + withBudgetFloor. A
// push that does not fit leaves its frame unknown (D3), so nothing
// cascades from it, and every later push that needs a step fails too;
// the first such with operand, in the order the bodies are checked, is
// S102 (withBudgetMessage), once per package, reported after every body
// (reportWithBudget). The checker never accepts a program whose with
// scopes it has not computed, as S103 never accepts a match it has not
// proved total.
//
// The budget is the package's, not a body's: one body's pushes cost what
// its contexts bind, which its own source does not bound (`with A, B` of
// two large contexts), and a per-body budget would refuse that body. The
// charged total is a function of the package's source, so whether a
// package is refused does not depend on its declarations' order; the
// operand reported follows the order the bodies are checked in.
// Prior art: TypeScript stops instantiation past a fixed count (TS2589),
// rustc refuses past recursion_limit, and the context pass refuses past
// its budget (contextsum.go); each budget is a function of the source only.

// withBudgetPerByte is the push work allowed per byte of the package's
// source: 16. The shapes TestWithSiteWorkLinear counts spend under 1.5
// with-site steps per byte, lookups included, at 4n; the corpus, the
// fixtures and the examples spend under 1% of their budget
// (TestWithBudgetHeadroom).
const withBudgetPerByte = 16

// withBudgetFloor is the push work every package is allowed beyond its
// bytes: a small file's withs never meet the budget.
const withBudgetFloor = 1 << 16

// withBudgetMessage is the S102 of the first with operand whose push
// passed the package's with budget.
const withBudgetMessage = "the with scopes of this package need more work than its with budget to find which with binds each capability; split this with or the contexts it names (D16)"

// A withRefusal is the with operand whose push first passed the budget,
// in its file.
type withRefusal struct {
	at   syntax.Node
	file *fileInfo
}

// startWithBudget sets the package's with meter from its source size.
func (c *checker) startWithBudget(files []*syntax.File) {
	size := 0
	for _, f := range files {
		size += len(f.Src)
	}
	limit := withBudgetPerByte*size + withBudgetFloor
	if c.withLimit != nil {
		limit = c.withLimit(size)
	}
	c.withs = &contextMeter{limit: limit}
}

// pushFrame pushes f, the frame of the with operand at, on t's with scope,
// charged to the package's with meter. A push that does not fit leaves f
// unknown (withScope.push); the first such operand is recorded, silent
// checks included, so a body checked only silently still refuses its
// package.
func (c *checker) pushFrame(t *target, at syntax.Node, f *withFrame) {
	var ok bool
	if t.scope, ok = t.scope.push(c.withs, f); !ok && c.withRefused == nil {
		c.withRefused = &withRefusal{at: at, file: c.cur}
	}
}

// reportWithBudget reports the with operand whose push first passed the
// budget (S102), once per package.
func (c *checker) reportWithBudget() {
	r := c.withRefused
	if r == nil {
		return
	}
	c.cur = r.file
	c.errorf(r.at, CodeMissingCap, "%s", withBudgetMessage)
}

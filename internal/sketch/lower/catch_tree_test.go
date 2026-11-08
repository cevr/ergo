package lower

import (
	"os"
	"testing"
)

// TestCatchTreeShape (M1b, red acceptance check): a catch with record
// patterns and an or-pattern lowers through the match tree. For the
// function that holds the catch, the emitted Go calls no ergoIs helper,
// asserts no type (only a `.(type)` switch narrows the error), tests each
// column at most once on every path, and holds each arm's literal once
// (TestMatchTreeShape's rules 1 to 4).
func TestCatchTreeShape(t *testing.T) {
	if matchLowering == "flat" {
		t.Skip("matchLowering=flat: the build has no tree")
	}
	for _, c := range catchShapeCases() {
		t.Run(c.name, func(t *testing.T) {
			src := stripDirectives(mustLower(t, c.name, c.src).mainGo())
			fn, helper := treeFunc(t, src, c.fn)
			for _, problem := range treeShapeProblems(fn, helper, c.literals) {
				t.Error(problem)
			}
			if t.Failed() || os.Getenv("CATCH_TREE_SHOW") != "" {
				t.Logf("emitted %s:\n%s", c.fn, treeFuncText(src, fn))
			}
		})
	}
}

// catchShapeDecls are four error types of the shape cases: two that share
// a field name and type (the or-pattern binds it from either), one with a
// field of another type, and one with none.
const catchShapeDecls = `type Declined { Reason string, Code int }
type Refused { Reason string }
type Frozen {}
type Expired { Days int }

func raise(k int) string fails Declined | Refused | Frozen | Expired {
	if k == 0 {
		return Declined{Reason: "card", Code: 1}
	}
	if k == 1 {
		return Declined{Reason: "limit", Code: 2}
	}
	if k == 2 {
		return Refused{Reason: "fraud"}
	}
	if k == 3 {
		return Frozen
	}
	if k == 4 {
		return Expired{Days: 0}
	}
	if k == 5 {
		return Expired{Days: 9}
	}
	"ok"
}
`

func catchShapeCases() []treeShapeCase {
	return []treeShapeCase{
		{"total", catchShapeDecls + `
func total(k int) string {
	raise(k) catch {
	case Declined{Reason: "card"}:
		"arm0"
	case Declined{Reason: r}, Refused{Reason: r}:
		"arm1" + r
	case Expired{Days: 0}:
		"arm2"
	case Frozen:
		"arm3"
	case _:
		"arm4"
	}
}

func main() {}
`, "total", []string{"arm0", "arm1", "arm2", "arm3", "arm4"}},
		{"partial", catchShapeDecls + `
func partial(k int) string fails Expired {
	try raise(k) catch {
	case Declined{Code: 1} as d:
		"arm0" + d.Reason
	case Declined{Reason: r}, Refused{Reason: r}:
		"arm1" + r
	case Frozen:
		"arm2"
	case Expired{Days: 0}:
		"arm3"
	}
}

func main() {}
`, "partial", []string{"arm0", "arm1", "arm2", "arm3"}},
	}
}

package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// An else block is a branch of its if, like the if body (grammar.md,
// Statements 4 and 5): a return, break or continue in it leaves what the
// same jump in the body leaves, and is S108 only when it would cross a with
// block or a trailing block. The cases below hold the risks of that rule
// beside the jumps: barriers around the else, missing return (S107) after
// an if-else at statement and tail position, unused locals (S110) across an
// else that returns, a task started in an else block, and the reference
// walk, which must see an else block's names once, as typing does.

type elseCase struct {
	name string
	src  string
	want []string // "line:code", in order
}

const elseDecls = "let W = context {}\ntype E {}\nfunc g() fails E { E }\nfunc use(f func() int) int { f() }\n"

var elseCases = []elseCase{
	{"return in else", `
func f(b bool) int {
	if b {
		let _ = 1
	} else {
		return 2
	}
	1
}`, nil},
	{"break and continue in else blocks of a for", `
func f(xs []int) int {
	let mut n = 0
	for x in xs {
		if x > 0 {
			n += x
		} else if x == 0 {
			continue
		} else {
			break
		}
	}
	n
}`, nil},
	{"break in an else block inside a match arm", `
func f(xs []int, b bool) int {
	for x in xs {
		match b {
		case true:
			if x > 0 {
				let _ = x
			} else {
				break
			}
		case false:
			let _ = 0
		}
	}
	0
}`, nil},
	{"return in an else block of a catch arm", `
func f(b bool) int {
	g() catch {
	case E:
		if b {
			let _ = 1
		} else {
			return 2
		}
	}
	1
}`, nil},
	{"return in an else block of a function literal", `
func f(b bool) int {
	let fl = func() int {
		if b {
			let _ = 1
		} else {
			return 3
		}
		2
	}
	fl()
}`, nil},
	{"return in an else block inside a with block", `
func f(b bool) int {
	with W {
		if b {
			let _ = 1
		} else {
			return 2
		}
	}
	1
}`, []string{"11:S108"}},
	{"return in an else block of an if inside a with block", `
func f(b bool, c bool) int {
	with W {
		if b {
			if c {
				let _ = 1
			} else {
				return 2
			}
		}
	}
	1
}`, []string{"12:S108"}},
	{"return in an else block of a trailing block", `
func f(b bool) int {
	use() {
		if b {
			let _ = 1
		} else {
			return 2
		}
		0
	}
}`, []string{"11:S108"}},
	{"break in an else block of a with block inside a for", `
func f(xs []int, b bool) int {
	for x in xs {
		with W {
			if b {
				let _ = x
			} else {
				break
			}
		}
	}
	0
}`, []string{"12:S108"}},
	{"break in an else block of a function literal inside a for", `
func f(xs []int, b bool) int {
	for x in xs {
		let fl = func() int {
			if b {
				let _ = x
			} else {
				break
			}
			0
		}
		let _ = fl()
	}
	0
}`, []string{"12:S108"}},
	{"break in an else block with no for", `
func f(b bool) int {
	if b {
		let _ = 1
	} else {
		break
	}
	0
}`, []string{"10:S108"}},
	{"if-else returning in both branches, then statements", `
func f(b bool) int {
	if b {
		return 1
	} else {
		return 2
	}
	let _ = 3
	4
}`, nil},
	{"if-else returning in both branches at the tail", `
func f(b bool) int {
	if b {
		return 1
	} else {
		return 2
	}
}`, nil},
	{"else-if chain returning in every branch at the tail", `
func f(n int) int {
	if n > 2 {
		return 1
	} else if n > 1 {
		return 2
	} else {
		return 3
	}
}`, nil},
	{"tail if-else whose else has no value", `
func f(b bool) int {
	if b {
		return 1
	} else {
		let _ = 2
	}
}`, []string{"11:S107"}},
	{"tail else-if chain whose last else has no value", `
func f(n int) int {
	if n > 2 {
		return 1
	} else if n > 1 {
		return 2
	} else {
		let _ = 3
	}
}`, []string{"13:S107"}},
	{"a local read only in an else block that returns", `
func f(b bool) int {
	let x = 2
	if b {
		let _ = 1
	} else {
		return x
	}
	1
}`, nil},
	{"an unused local in an else block that returns", `
func f(b bool) int {
	if b {
		let _ = 1
	} else {
		let x = 2
		return 3
	}
	1
}`, []string{"10:S110"}},
	{"a local of the else block is not in scope after the if", `
func f(b bool) int {
	if b {
		let _ = 1
	} else {
		let y = 2
		return y
	}
	y
}`, []string{"13:S104"}},
}

func elseDiags(src string) []string {
	_, diags := check.CheckSource(src)
	var got []string
	for _, d := range diags {
		got = append(got, fmt.Sprintf("%d:%s", d.Line, d.Code))
	}
	return got
}

// TestElseBranchJumps checks each case's diagnostics, and that the
// reference walk agrees with typing on it.
func TestElseBranchJumps(t *testing.T) {
	for _, c := range elseCases {
		src := elseDecls + c.src + "\n"
		got := elseDiags(src)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			_, diags := check.CheckSource(src)
			t.Errorf("%s: got %v, want %v\n%v\n%s", c.name, got, c.want, diags, src)
		}
		if p := check.RefWalkProblems(src); len(p) != 0 {
			t.Errorf("%s: the walk and typing disagree: %v", c.name, p)
		}
	}
}

// TestElseBranchTask: a task started in an else block and waited there
// before a return checks clean, as it does in the if body.
func TestElseBranchTask(t *testing.T) {
	for _, branch := range []string{"then", "else"} {
		jump := "let t = task.Go() { 2 }\n\t\treturn t.Wait()"
		then, els := "let _ = 1", "let _ = 1"
		if branch == "then" {
			then = jump
		} else {
			els = jump
		}
		src := "import task\n\nfunc f(b bool) int {\n\tif b {\n\t\t" + then + "\n\t} else {\n\t\t" + els + "\n\t}\n\t1\n}\n"
		if got := elseDiags(src); len(got) != 0 {
			_, diags := check.CheckSource(src)
			t.Errorf("%s: want no diagnostic, got %v\n%s", branch, diags, src)
		}
	}
}

package lower

import "testing"

// TestElseBranchRun: a return, break or continue in an else block at
// statement position leaves what it leaves in the if body (grammar.md,
// Statements 5), and the lowered Go does the same at run time. returnInElse
// is apps-1 probe c7, the regression fixture apps-1 spec.md:318 asks for.
func TestElseBranchRun(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{
		{name: "returnInElse", src: `type Bad { At int }

func first(s string) int fails Bad {
	if len(s) > 5 {
		Console.Println("long")
	} else {
		return Bad{At: 0}
	}
	return 0
}

func second(n int) int {
	if n > 5 {
		return 1
	} else {
		return 2
	}
}

func main() needs Console {
	let n = first("ab") catch {
	case Bad as b:
		b.At
	}
	Console.Println("\(n) \(second(3))")
}
`, stdout: "0 2\n"},
		{name: "jumpsInElse", src: `type Bad { At int }

func looped(s string) int fails Bad {
	for i in 0..len(s) {
		if s[i] == 'a' {
			let _ = i
		} else {
			return Bad{At: i}
		}
	}
	0
}

func chain(n int) int {
	if n > 5 {
		let _ = n
	} else if n > 3 {
		return 3
	} else {
		return 2
	}
	1
}

func count(xs []int) int {
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
}

func inLit(n int) int {
	let fl = func() int {
		if n > 10 {
			let _ = n
		} else {
			return 8
		}
		2
	}
	fl() + 100
}

func both(b bool) int {
	if b {
		return 1
	} else {
		return 2
	}
	let _ = 3
	4
}

func main() needs Console {
	let k = looped("ab") catch {
	case Bad as b:
		b.At
	}
	Console.Println("\(k) \(chain(9)) \(chain(4)) \(chain(1))")
	Console.Println("\(count([]int{1, 0, 2, -1, 5}))")
	Console.Println("\(inLit(3)) \(inLit(20)) \(both(true)) \(both(false))")
}
`, stdout: "1 1 3 2\n3\n108 102 1 2\n"},
	})
}

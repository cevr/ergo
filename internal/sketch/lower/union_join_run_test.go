package lower

import "testing"

// K3 (M8): branches that are variants of one union join to it and run:
// synthesis repro c10 (line 18 written with its type), a generic union, a
// match nested in an if, and a variant with fields.
func TestErgoRunUnionJoin(t *testing.T) {
	wantRun(t, `type State = Open{} | Closed{} | Ajar{ Width int }

func parse(s string) Option[State] {
	let state = match s {
	case "open":
		Open
	case "closed":
		Closed
	case "ajar":
		Ajar{Width: 3}
	case _:
		return None
	}
	Some[State]{Value: state}
}

func pick(n int) Option[int] {
	let o = if n > 0 { Some{Value: n} } else { None }
	o
}

func nested(n int, c bool) State {
	let s = if c {
		match n {
		case 0:
			Open
		case _:
			Ajar{Width: n}
		}
	} else {
		Closed
	}
	s
}

func show(s State) string {
	match s {
	case Open:
		"open"
	case Closed:
		"closed"
	case Ajar as a:
		"ajar \(a.Width)"
	}
}

func best(xs []int) Option[int] {
	let mut b Option[int] = None
	for x in xs {
		b = Some[int]{Value: x}
	}
	b
}

func main() needs Console {
	Console.Println("\(parse("open") == Some[State]{Value: Open}) \(parse("ajar") == Some[State]{Value: Ajar{Width: 3}}) \(parse("x") == None) \(pick(2) == Some{Value: 2}) \(pick(0) == None) \(best([]int{1}) == Some[int]{Value: 1})")
	Console.Println(show(nested(0, true)) + " " + show(nested(4, true)) + " " + show(nested(4, false)))
}
`, "true true true true true true\nopen ajar 4 closed\n")
}

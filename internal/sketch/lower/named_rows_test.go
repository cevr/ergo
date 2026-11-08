package lower

import "testing"

// TestErgoRunNamedRows (L2 acceptance): `fails U` of a union of error types
// runs as the spelled row: try passes each variant on, `case U as u:`
// binds the union in a type switch and in a catch with record patterns, a
// generic union names its instance's row, and std's flag.Failure names
// flag's failures.
func TestErgoRunNamedRows(t *testing.T) {
	wantRun(t, `import flag

type Bad { At int }
type Oops { At int }
type Other {}
type Fail = Bad | Oops
type Gen[T] = Held[T]{ V T } | Lost{}

func row(n int) string fails Fail {
	if n == 1 {
		return Bad{At: 1}
	}
	if n == 2 {
		return Oops{At: 2}
	}
	"ok \(n)"
}

func wider(n int) string fails Fail | Other {
	if n == 3 {
		return Other
	}
	try row(n)
}

func describe(f Fail) string {
	match f {
	case Bad{At: a}:
		"bad \(a)"
	case Oops{At: a}:
		"oops \(a)"
	}
}

func held(n int) int fails Gen[int] {
	if n == 0 {
		return Lost
	}
	if n == 1 {
		return Held[int]{V: 7}
	}
	n
}

func build(args []string) string fails flag.Failure {
	let v = try flag.Parse(flag.Set{Name: "t", Usage: "u", Flags: []flag.Flag{flag.Bool("v", false, "verbose")}, Args: []flag.ArgSpec{}}, args)
	"parsed \(len(flag.Rest(v)))"
}

func main() needs Console {
	for n in 0..4 {
		let s = wider(n) catch {
		case Fail as f:
			describe(f)
		case Other:
			"other"
		}
		Console.Println("\(n) \(s)")
	}
	for n in 0..3 {
		let r = row(n) catch {
		case Bad{At: 1}:
			"bad one"
		case Fail as f:
			describe(f)
		}
		Console.Println(r)
	}
	for n in 0..3 {
		let h = held(n) catch {
		case Held[int]{V: v}:
			v
		case Lost:
			-1
		}
		Console.Println("\(h)")
	}
	for _, args in [][]string{[]string{"-v"}, []string{"-x"}} {
		let s = build(args) catch {
		case flag.Failure as f:
			flag.Describe(f)
		}
		Console.Println(s)
	}
}
`, "0 ok 0\n1 bad 1\n2 oops 2\n3 other\nok 0\nbad one\noops 2\n-1\n7\n2\nparsed 0\nflag provided but not defined: -x\n")
}

package lower

import "testing"

// Native acquisition callbacks preserve the enclosing source frame. A
// control transfer in a constructor argument must close prior successes
// without constructing or registering the entry it escapes.
func TestRuntime199AcquireFlow(t *testing.T) {
	const prelude = `type A { Use() }
type B { Use() }
type First {}
type Second {}
type Fault {}
func Use(_ First) {}
func Use(_ Second) {}
func Close(_ First) needs Console { Console.Println("A closed") }
func Close(_ Second) needs Console { Console.Println("B closed") }
func open(n int) First needs Console { let _ = n; Console.Println("A opened"); First }
func fail() First fails Fault { return Fault }
`
	cases := []struct{ name, body, want string }{
		{"success", `func main() needs Console {
let n = 1
with wire { B: Second, A: open(n) } { B.Use(); A.Use(); Console.Println("body") }
Console.Println("end")
}`, "A opened\nbody\nA closed\nB closed\nend\n"},
		{"failure", `func work() fails Fault needs Console {
with wire { B: Second, A: try fail() } { B.Use(); A.Use() }
}
func main() needs Console { work() catch { case Fault: Console.Println("caught") } }
`, "B closed\ncaught\n"},
		{"return", `func work() int needs Console {
with wire { B: Second, A: open(if true { return 7 } else { 0 }) } { B.Use(); A.Use() }
9
}
func main() needs Console { if work() == 7 { Console.Println("returned") } }
`, "B closed\nreturned\n"},
		{"break", `func main() needs Console {
for true {
with wire { B: Second, A: open(if true { break } else { 0 }) } { B.Use(); A.Use() }
}
Console.Println("end")
}`, "B closed\nend\n"},
		{"continue", `func main() needs Console {
for n in 0..2 {
with wire { B: Second, A: open(if n >= 0 { continue } else { 0 }) } { B.Use(); A.Use() }
Console.Println("BAD")
}
Console.Println("end")
}`, "B closed\nB closed\nend\n"},
	}
	var programs []*lowered
	for _, tc := range cases {
		programs = append(programs, mustLower(t, tc.name, prelude+tc.body))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.FailNow()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runExe(t, exes[tc.name])
			if code != 0 || stderr != "" || stdout != tc.want {
				t.Fatalf("exit=%d stdout=%q want=%q stderr=%s", code, stdout, tc.want, stderr)
			}
		})
	}
}

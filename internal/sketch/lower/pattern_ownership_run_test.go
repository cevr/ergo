package lower

import (
	"fmt"
	"math/rand/v2"
)

// genPatternDrop extends the drop reference model, rather than adding a
// second runtime test. Modes vary the ownership partition, source and
// consumer independently of the generated resource identities and branch.
// The checker model covers refused partitions; these are admitted ones.
func genPatternDrop(seed int) *dgCase {
	r := rand.New(rand.NewPCG(uint64(seed), 0x706f31))
	a, b := 10+r.IntN(1000), 1010+r.IntN(1000)
	selected := r.IntN(2) == 0
	makePair := fmt.Sprintf("Pair{R: Res{id: %d, bad: false}, S: Res{id: %d, bad: false}, N: %t}", a, b, selected)
	body := ""
	var extra string
	closeA, closeB := fmt.Sprintf("close %d", a), fmt.Sprintf("close %d", b)
	want := []string{"body", "end", closeB, closeA}
	mode := r.IntN(10)
	switch mode {
	case 0:
		body = fmt.Sprintf("let (a, b) = (Res{id: %d, bad: false}, Res{id: %d, bad: false})\nlend(a)\nlend(b)\nConsole.Println(\"body\")", a, b)
	case 1:
		body = "let Nest{P: Pair{R: a, S: b}} = Nest{P: " + makePair + "}\nlend(a)\nlend(b)\nConsole.Println(\"body\")"
	case 2, 3:
		value := makePair
		if mode == 2 {
			body = "let source Held = " + value + "\n"
			value = "source"
		} else {
			extra = "func make() Held { " + value + " }\n"
			value = "make()"
		}
		body += "match " + value + ` {
case Pair{R: a, S: b, N: true}: lend(a); lend(b); Console.Println("arm")
case Pair{N: false}: Console.Println("inspect")
case Empty: Console.Println("empty")
}
Console.Println("body")`
		if selected {
			want = []string{"arm", closeB, closeA, "body", "end"}
		} else if mode == 3 {
			want = []string{"inspect", closeB, closeA, "body", "end"}
		} else {
			want = []string{"inspect", "body", "end", closeB, closeA}
		}
	case 4:
		body = "let a = match (" + makePair + `) {
case Pair{R: a, S: b}: lend(b); a
}
lend(a)
Console.Println("body")`
		want = []string{closeB, "body", "end", closeA}
	case 5:
		fail := r.IntN(2) == 0
		extra = fmt.Sprintf(`type Bad {}
func acquire() Pair fails Bad {
let r = Res{id: %d, bad: false}
if %t { return Bad }
Pair{R: r, S: Res{id: %d, bad: false}, N: true}
}
func work() fails Bad needs Console {
let Pair{R: a, S: b} = try acquire()
lend(a)
lend(b)
Console.Println("body")
}
`, a, fail, b)
		body = `work() catch { case Bad: Console.Println("failed") }`
		want = []string{"body", closeB, closeA, "end"}
		if fail {
			want = []string{closeA, "failed", "end"}
		}
	case 6:
		extra = "type Closed { R Res }\nfunc Close(c Closed) needs Console { Console.Println(\"custom\") }\nfunc lendClosed(c Closed) {}\n"
		body = fmt.Sprintf("match (Closed{R: Res{id: %d, bad: false}}) { case Closed as owner: lendClosed(owner); Console.Println(\"body\") }", a)
		want = []string{"body", "custom", closeA, "end"}
	case 7:
		body = fmt.Sprintf(`match (Res{id: %d, bad: false}, Res{id: %d, bad: false}) {
case (a, b): lend(a); lend(b); Console.Println("body")
}`, a, b)
		want = []string{"body", closeB, closeA, "end"}
	case 8:
		extra = "func make() Held { " + makePair + " }\n"
		body = `let _ = match make() { case Pair: Console.Println("arm"); true; case Empty: false }
Console.Println("body")`
		want = []string{"arm", closeB, closeA, "body", "end"}
	case 9:
		body = fmt.Sprintf("let Pair{R: a, S: b} = if %t { let local = %s; local } else { %s }\nlend(a)\nlend(b)\nConsole.Println(\"body\")", selected, makePair, makePair)
	}
	src := dgPrelude + `type Pair { R Res, S Res, N bool }
type Nest { P Pair }
type Empty {}
type Held = Pair | Empty
func lend(r Res) {}
` + extra + "func main() needs Console {\n" + body + "\nConsole.Println(\"end\")\n}\n"
	return &dgCase{src: src, want: want}
}

package lower

import (
	"testing"
)

// A type parameter a method set constrains lowers to Go any with a witness
// (D-E1; stage E1 orchestrator answer 2): operations on a value and through
// a pointer, a 196m function and a generic one, a caller's witness passed
// on, an operation type's value, fallible and no-result operations, a
// constraint that names its own parameter, a method value, an instance used
// as a value, and an operations conversion inside a generic function (C8).
func TestErgoRunE1Witnesses(t *testing.T) {
	needsBuild(t)
	wantRun(t, `type Valid {
	Check() bool,
}

type Both {
	Check() bool,
	Name() string,
}

type Scaler {
	Scale(n int) int fails Bad,
	Touch(),
}

type Bad { Why string }

type Lesser[K] {
	Less(other K) bool,
}

type Card { Number string }

type Num { V int }

type Box[T] { V T }

type Holder[T Valid] { Item T }

func Check(c Card) bool {
	c.Number != ""
}

func Check[T](b Box[T]) bool {
	true
}

func Name(c Card) string {
	"card " + c.Number
}

func Scale(n Num, k int) int fails Bad {
	if k < 0 {
		return Bad{Why: "negative"}
	}
	n.V * k
}

func Touch(n Num) {
}

func Less(a Num, b Num) bool {
	a.V < b.V
}

func All[T Valid](xs []T) bool {
	for x in xs {
		if !x.Check() {
			return false
		}
	}
	true
}

func Named[T Both](x T) string {
	if All([]T{x}) { x.Name() } else { "invalid" }
}

func AsValid[T Valid](x T) Valid {
	x
}

func One[T Valid](x *T) bool {
	x.Check()
}

func ScaleAll[T Scaler](xs []T, k int) int fails Bad {
	let mut sum = 0
	for x in xs {
		x.Touch()
		let n = try x.Scale(k)
		sum = sum + n
	}
	sum
}

func Min[T Lesser[T]](a, b T) T {
	if a.Less(b) { a } else { b }
}

func Checker[T Valid](x T) func() bool {
	x.Check
}

func Ok[T Valid](h Holder[T]) bool {
	h.Item.Check()
}

func Big[T ordered](b Box[T], x T) bool {
	b.V > x
}

func Pair[A Valid, B Both](a A, b B) string {
	if a.Check() { b.Name() } else { "none" }
}

func AsValidBox[T](b Box[T]) Valid {
	b
}

func pick(f func([]Card) bool) bool {
	f([]Card{Card{Number: "9"}})
}

func main() needs Console {
	let mut c = Card{Number: "7"}
	let mut d = Card{Number: ""}
	let v = AsValid(c)
	let s = ScaleAll([]Num{Num{V: 2}, Num{V: 3}}, 10) catch {
	case Bad:
		0
	}
	let bad = ScaleAll([]Num{Num{V: 2}}, -1) catch {
	case Bad:
		-1
	}
	let m = Min(Num{V: 5}, Num{V: 4})
	let f = Checker(c)
	let all = All[Card]
	let vs = []Valid{c}
	Console.Println("\(v.Check()) \(Named(c)) \(s) \(bad) \(m.V) \(f()) \(Ok(Holder[Card]{Item: c})) \(Big(Box[int]{V: 3}, 2)) \(pick(all))")
	Console.Println("\(All([]Card{c, d})) \(All(vs)) \(All([]Box[int]{Box[int]{V: 1}})) \(One(&c)) \(One(&d))")
	Console.Println("\(Pair(c, c)) \(Pair(d, c)) \(AsValidBox(Box[string]{V: "x"}).Check())")
}
`, "true card 7 50 -1 4 true true true true\nfalse true true true false\ncard 7 none true\n")
}

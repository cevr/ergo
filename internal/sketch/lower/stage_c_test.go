package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ergoRun runs src through the public CLI, including its checker,
// generated module and runtime, and returns the combined output. A
// successful check alone missed the former reachable-end panic, so every
// stage C form has a run test.
func ergoRun(t *testing.T, src string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "run", path)
	cmd.Dir = filepath.Dir(path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func wantRun(t *testing.T, src, want string) {
	t.Helper()
	out, err := ergoRun(t, src)
	if err != nil || out != want {
		t.Fatalf("ergo run: %v\n%s\nwant:\n%s", err, out, want)
	}
}

func TestErgoRunFunctionTails(t *testing.T) {
	wantRun(t, `type Failed { Code int }
func value() int { 42 }
func small() int8 { -128 }
func early(n int) int { if n < 0 { return 0 }; n }
func literal() int { let f = func() int { 7 }; f() }
func n123() int { 123 }
func ignored() { n123() }
func id[T](v T) T { return v }
func ignoredFunction() { id[int](5) }
func fail() int fails Failed { Failed{Code: 3} }
func through() int fails Failed { fail() }
func main() needs Console {
	ignored()
	ignoredFunction()
	if value() == 42 && int(small()) == -128 && early(3) == 3 && literal() == 7 {
		Console.Println("tails")
	}
	let n = through() catch { case Failed: 9 }
	if n == 9 { Console.Println("failure") }
}
`, "tails\nfailure\n")
}

// D-C2: if and match yield values in every position, branches that leave
// join any type, and operands keep their left-to-right order.
func TestErgoRunBranchExpressions(t *testing.T) {
	wantRun(t, `type E { Code int }
type A {}
type B { N int }
type U = A | B
func sign(n int) int { if n < 0 { -1 } else if n == 0 { 0 } else { 1 } }
func size(u U) int {
	match u {
	case A:
		0
	case B as b:
		b.N
	}
}
func rate(c bool, t int64) int64 { let r int64 = if c { 0 } else { 3 }; t * r }
func plus(u U) int {
	1 + match u {
	case A:
		1
	case _:
		0
	}
}
func half(n int8) int8 { n / 2 }
func arg(c bool) int8 { half(if c { 10 } else { 20 }) }
func pick(c bool) U { if c { A } else { B{N: 4} } }
func early(c bool) int { let x = if c { return 7 } else { 5 }; x + 1 }
func count(stop int) int {
	let mut n = 0
	for {
		let step = if n >= stop { break } else { 1 }
		n = n + step
	}
	n
}
func G(c bool) int fails E { if c { return E{Code: 2} }; 1 }
func voidTail(c bool) fails E { if c { G(true) } }
func arm(c bool) int {
	G(true) catch {
	case E as e:
		if c { e.Code } else { 0 }
	}
}
func word(b bool) string {
	match b {
	case true:
		"yes"
	case false:
		"no"
	}
}
func trace(s string, n int) int needs Console { Console.Println(s); n }
func inOrder(c bool) int needs Console { trace("left", 1) + if c { trace("then", 2) } else { trace("else", 3) } }
func main() needs Console {
	if sign(-5) == -1 && sign(0) == 0 && sign(9) == 1 { Console.Println("sign") }
	if size(A) == 0 && size(B{N: 3}) == 3 { Console.Println("match") }
	if rate(false, 100) == 300 && rate(true, 100) == 0 { Console.Println("let") }
	if plus(A) == 2 && plus(B{N: 1}) == 1 { Console.Println("operand") }
	if arg(true) == 5 && arg(false) == 10 { Console.Println("argument") }
	if size(pick(false)) == 4 && size(pick(true)) == 0 { Console.Println("union") }
	if early(true) == 7 && early(false) == 6 { Console.Println("return") }
	if count(3) == 3 { Console.Println("break") }
	voidTail(true) catch { case E: Console.Println("void failed") }
	voidTail(false) catch { case E: Console.Println("unexpected") }
	if arm(true) == 2 && arm(false) == 0 { Console.Println("catch arm") }
	if word(true) == "yes" && word(false) == "no" { Console.Println("bool") }
	if inOrder(false) == 4 { Console.Println("order") }
}
`, "sign\nmatch\nlet\noperand\nargument\nunion\nreturn\nbreak\nvoid failed\ncatch arm\nbool\nleft\nelse\norder\n")
}

// D-C3: one catch with type, record, as, case err: and case _ arms; a
// row value is returned, matched and assigned to a declared union (ConvRow);
// a partial catch under try lets the rest escape.
func TestErgoRunOneCatch(t *testing.T) {
	wantRun(t, `type NotFound { ID string }
type Declined { Reason string }
type Frozen {}
type Order { ID string }
type PayError = Declined | Frozen

func fetch(id string) Order fails NotFound | Declined | Frozen {
	if id == "" {
		return NotFound{ID: id}
	}
	if id == "d" {
		return Declined{Reason: "card"}
	}
	if id == "f" {
		return Frozen
	}
	Order{ID: id}
}

func describe(id string) string fails Declined | Frozen {
	let order = fetch(id) catch {
	case NotFound as e:
		Order{ID: "missing:" + e.ID}
	case err:
		return err
	}
	order.ID
}

func reason(id string) string {
	describe(id) catch {
	case Declined{Reason: "card"}:
		"card"
	case Declined{Reason: r}:
		r
	case Frozen:
		"frozen"
	}
}

func payName(p PayError) string {
	match p {
	case Declined:
		"d"
	case Frozen:
		"f"
	}
}

func keep(id string) string {
	let o = fetch(id) catch {
	case NotFound:
		Order{ID: "nf"}
	case err:
		let p PayError = err
		Order{ID: payName(p)}
	}
	o.ID
}

func kind(id string) string {
	let v = fetch(id) catch {
	case NotFound:
		Order{ID: "nf"}
	case err:
		let label = match err {
		case Declined:
			"declined"
		case Frozen:
			"frozen"
		}
		Order{ID: label}
	}
	v.ID
}

func partial(id string) Order fails Declined | Frozen {
	try fetch(id) catch {
	case NotFound:
		Order{ID: "none"}
	}
}

func anything(id string) string {
	let o = fetch(id) catch {
	case _:
		Order{ID: "failed"}
	}
	o.ID
}

func settle(id string) Order {
	partial(id) catch {
	case Declined:
		Order{ID: "x"}
	case Frozen:
		Order{ID: "y"}
	}
}

func main() needs Console {
	if reason("a") == "a" && reason("") == "missing:" { Console.Println("as") }
	if reason("d") == "card" && reason("f") == "frozen" { Console.Println("record") }
	if kind("d") == "declined" && kind("f") == "frozen" && kind("") == "nf" { Console.Println("row match") }
	let p = settle("")
	let q = settle("f")
	if keep("d") == "d" && keep("f") == "f" && keep("") == "nf" && anything("") == "failed" { Console.Println("convert") }
	if p.ID == "none" && q.ID == "y" { Console.Println("partial") }
}
`, "as\nrecord\nrow match\nconvert\npartial\n")
}

// Stage B lowering completeness (check-v1 ruling, 2026-10-30): record,
// nested, constant, tuple, as and several-subject patterns, let
// destructuring and tuples run, with no fallback that hides a case.
func TestErgoRunPatterns(t *testing.T) {
	wantRun(t, `type Inner = Small{N int} | Big{N int, Label string}
type Shape = Dot | Box{W int, H int} | Wrap{In Inner}
type Dot {}

func divmod(a int, b int) (int, int) { (a / b, a % b) }

func area(s Shape) int {
	match s {
	case Dot:
		0
	case Box{W: w, H: h}:
		w * h
	case Wrap{In: Small{N: n}}:
		n
	case Wrap{In: Big{N: 0}}:
		-1
	case Wrap{In: Big{Label: l} as b}:
		b.N + len(l)
	}
}

func pair(a int, b bool) string {
	match (a, b) {
	case (0, true):
		"zero-yes"
	case (0, false):
		"zero-no"
	case (_, true), (_, false):
		"other"
	}
}

func classify(t (int, string)) string {
	match t {
	case (1, s):
		s
	case (n, "x") as whole:
		let (m, _) = whole
		if n == m { "x" } else { "?" }
	case _:
		"none"
	}
}

func either(s Shape) int {
	match s {
	case Box{W: v}, Wrap{In: Small{N: v}}:
		v
	case _:
		0
	}
}

func main() needs Console {
	let (q, r) = divmod(17, 5)
	let a, b = divmod(9, 4)
	let Box{W: w} = Box{W: 3, H: 4}
	if q == 3 && r == 2 && a == 2 && b == 1 && w == 3 { Console.Println("lets") }
	if area(Dot) == 0 && area(Box{W: 2, H: 3}) == 6 && area(Wrap{In: Small{N: 5}}) == 5 { Console.Println("records") }
	if area(Wrap{In: Big{N: 0, Label: "z"}}) == -1 && area(Wrap{In: Big{N: 2, Label: "abc"}}) == 5 { Console.Println("nested") }
	if pair(0, true) == "zero-yes" && pair(0, false) == "zero-no" && pair(4, true) == "other" { Console.Println("subjects") }
	if classify((1, "one")) == "one" && classify((2, "x")) == "x" && classify((3, "y")) == "none" { Console.Println("tuples") }
	if either(Box{W: 7, H: 1}) == 7 && either(Wrap{In: Small{N: 8}}) == 8 && either(Dot) == 0 { Console.Println("or-patterns") }
}
`, "lets\nrecords\nnested\nsubjects\ntuples\nor-patterns\n")
}

// 196r, sixth round: try covers the whole expression to its right; the
// operands still run left to right, and the first failure leaves.
func TestErgoRunWholeTry(t *testing.T) {
	wantRun(t, `type E { Code int }
func a(n int) int fails E needs Console {
	Console.Println("a")
	if n < 0 {
		return E{Code: n}
	}
	n
}
func sum(x int, y int) int fails E needs Console { try a(x) + a(y) }
func nest(x int) int fails E needs Console { try a(a(x) + 1) }
func main() needs Console {
	let s = sum(1, 2) catch { case E: 0 }
	let f = sum(-1, 2) catch { case E{Code: c}: c }
	let n = nest(1) catch { case E: 0 }
	if s == 3 && f == -1 && n == 2 { Console.Println("ok") }
}
`, "a\na\na\na\na\nok\n")
}

// D-C4: break and continue in a match act on the enclosing for, through
// the Go switch the match lowers to.
func TestErgoRunBreakInMatch(t *testing.T) {
	wantRun(t, `type A {}
type B { N int }
type U = A | B
func firstNonZero(xs []int) int {
	let mut i = 0
	let mut found = 0
	for i < len(xs) {
		let x = xs[i]
		i = i + 1
		match x {
		case 0:
			continue
		case _:
			found = x
			break
		}
	}
	found
}
func sumUntilA(us []U) int {
	let mut i = 0
	let mut total = 0
	for i < len(us) {
		let u = us[i]
		i = i + 1
		match u {
		case A:
			break
		case B{N: n}:
			total = total + n
		}
	}
	total
}
func main() needs Console {
	if firstNonZero([]int{0, 0, 7, 9}) == 7 { Console.Println("value match") }
	if sumUntilA([]U{B{N: 1}, B{N: 2}, A, B{N: 5}}) == 3 { Console.Println("pattern match") }
}
`, "value match\npattern match\n")
}

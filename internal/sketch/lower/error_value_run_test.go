package lower

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// K1 (M6, owner q27 assumed "returns a value"): a function's declared
// result type is its contract. When it is an error type E, a tail or a
// return of an E value is the success value, and E is not in its row; in
// any other function an E value at a tail or a return is a failure
// (rows.md, Error rows 4), whether it is a literal, a call's value, a
// local or a catch binding.

// The rule table's axes.
var (
	evForms     = []string{"literal", "call", "local", "catch"}
	evPositions = []string{"tail", "return"}
	// value: the result is Missing; failure: int with the row {Missing};
	// refused: int with no row, where the failure escapes (S101); named:
	// Fail, a named union of error types that holds Missing, so the value
	// is the success value too (fs1).
	evTargets = []string{"value", "failure", "refused", "named"}
)

const evDecls = `type Missing { Key string }
type Other { Why string }
type Fail = Missing | Other
func other() int fails Other { 1 }
func missing(k string) Missing { Missing{Key: k} }
func probe(k string) int fails Missing {
	if k != "" {
		return Missing{Key: k}
	}
	1
}
`

// evFunc writes function name over (form, position, target): its body
// produces a Missing value with Key k, by form, at position; the target
// decides the declared result and row. ok is false for a combination the
// table leaves out: a catch arm's tail value at the body's tail must have
// the caught call's type (rows.md, Catch 3), so a catch binding reaches a
// body only by return.
func evFunc(name, form, position, target string) (src string, ok bool) {
	if form == "catch" && position == "tail" {
		return "", false
	}
	head := map[string]string{"value": "Missing", "failure": "int fails Missing", "refused": "int", "named": "Fail"}[target]
	other := map[string]string{"value": `Missing{Key: "other"}`, "failure": "7", "refused": "7", "named": `Other{Why: "other"}`}[target]
	var prelude, expr string
	switch form {
	case "literal":
		expr = "Missing{Key: k}"
	case "call":
		expr = "missing(k)"
	case "local":
		prelude, expr = "let m = missing(k)\n\t\t", "m"
	}
	var body string
	switch {
	case form == "catch":
		// The binding is returned from the arm; probe(k) fails for every
		// k != "", and the generated callers pass a non-empty k.
		body = fmt.Sprintf("let n = probe(k) catch {\n\tcase Missing as e:\n\t\treturn e\n\t}\n\t_ = n\n\t%s", other)
	case position == "tail":
		body = strings.ReplaceAll(prelude, "\t\t", "\t") + expr
	default:
		body = fmt.Sprintf("if k != \"\" {\n\t\t%sreturn %s\n\t}\n\t%s", prelude, expr, other)
	}
	return fmt.Sprintf("func %s(k string) %s {\n\t%s\n}\n", name, head, body), true
}

// TestErrorValueRuleTable is the K1 property: every (form, position,
// target) of the rule table, each with a seeded key, checks as the table
// says (value, failure and named check; refused is one S101), and the checked
// ones run as it says: a value target returns the Missing value, a
// failure target fails with it. The table is enumerated whole; the fixed
// seed only draws the keys, and a failure prints the program.
func TestErrorValueRuleTable(t *testing.T) {
	needsBuild(t)
	r := rand.New(rand.NewSource(27))
	var funcs, calls strings.Builder
	var want strings.Builder
	n := 0
	for _, form := range evForms {
		for _, position := range evPositions {
			for _, target := range evTargets {
				name := fmt.Sprintf("G%d", n) // exported: its rows are written, never inferred
				f, ok := evFunc(name, form, position, target)
				if !ok {
					continue
				}
				n++
				_, ds := check.CheckSource(evDecls + f)
				var codes []string
				for _, d := range ds {
					codes = append(codes, d.Code)
				}
				wantCodes := ""
				if target == "refused" {
					wantCodes = "S101"
				}
				if got := strings.Join(codes, " "); got != wantCodes {
					t.Errorf("%s %s %s: want [%s], got %v\n%s", form, position, target, wantCodes, ds, f)
					continue
				}
				if target == "refused" {
					continue
				}
				funcs.WriteString(f)
				key := fmt.Sprintf("k%d", r.Intn(1000))
				switch target {
				case "value":
					fmt.Fprintf(&calls, "\tConsole.Println(\"%s value \\(%s(%q).Key)\")\n", name, name, key)
					fmt.Fprintf(&want, "%s value %s\n", name, key)
				case "failure":
					fmt.Fprintf(&calls, "\tlet v%s = %s(%q) catch {\n\tcase Missing as m:\n\t\tConsole.Println(\"%s failed \\(m.Key)\")\n\t\t0\n\t}\n\tConsole.Println(\"\\(v%s)\")\n", name, name, key, name, name)
					fmt.Fprintf(&want, "%s failed %s\n0\n", name, key)
				case "named":
					fmt.Fprintf(&calls, "\tmatch %s(%q) {\n\tcase Missing as m:\n\t\tConsole.Println(\"%s member \\(m.Key)\")\n\tcase Other:\n\t\tConsole.Println(\"%s other\")\n\t}\n", name, key, name, name)
					fmt.Fprintf(&want, "%s member %s\n", name, key)
				}
			}
		}
	}
	// Every cell but catch at the tail.
	if want := (len(evForms)*len(evPositions) - 1) * len(evTargets); n != want {
		t.Fatalf("the table has %d rows, want %d", n, want)
	}
	if t.Failed() {
		return
	}
	src := evDecls + funcs.String() + "func main() needs Console {\n" + calls.String() + "}\n"
	out, err := ergoRun(t, src)
	if err != nil || out != want.String() {
		t.Fatalf("ergo run: %v\n%s\nwant:\n%s\nsource:\n%s", err, out, want.String(), src)
	}
}

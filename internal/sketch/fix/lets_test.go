package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketchfront"
)

// A use of a declared name: where, after its declaration, the generator
// writes it, and whether that use assigns it.
type letUse struct {
	name    string
	text    string // %s is the name
	assigns bool
}

var letUses = []letUse{
	{"never", "\tlet w%[2]d = %[1]s + 1\n\tif %[1]s == 7 {\n\t\tn = w%[2]d\n\t}", false},
	{"same depth", "\t%[1]s = %[1]s + 1", true},
	{"nested", "\tif n > 0 {\n\t\t%[1]s += 2\n\t}", true},
	{"shadowed", "\tif n > 1 {\n\t\tlet %[1]s = 3\n\t\t%[1]s = %[1]s + 1\n\t}", true},
	{"closure", "\tlet g%[2]d = func() int {\n\t\t%[1]s = 5\n\t\t%[1]s\n\t}", true},
}

// The spellings of one declaration: the Go form and its hand-written let
// twin; %[1]s is the name, %[2]s the let keyword.
var letForms = []struct{ goForm, twin string }{
	{"\tvar %[1]s = %[3]d", "\t%[2]s %[1]s = %[3]d"},
	{"\tvar %[1]s int = %[3]d", "\t%[2]s %[1]s int = %[3]d"},
	{"\t%[1]s := %[3]d", "\t%[2]s %[1]s = %[3]d"},
}

// TestLetsMatchesTheTwin (VET-CHECK-VAR) is a seeded property of the
// parser's fix for a Go local declaration. Each seed writes one function
// twice: with Go declarations (`var x = e`, `var x T = e`, `x := e`) and
// with the let twins a person writes, `let mut` exactly when a later
// statement of the block assigns the name. Each name is used in one of
// five positions: never assigned (read, compared), assigned at the same
// depth, in a nested block, after a shadowing let in a nested block (no
// shadowing, so the text assigns the name), or in a function literal.
// Some seeds end with `var z int`, which has no value to let: it keeps
// the plain habit and no fix. It checks that the fixed text is the twin
// byte for byte and that both check the same, that the rounds equal the
// declarations (the loop is bounded by the fix count), and that a second
// fix writes nothing. ERGO_SEED=<n> replays one seed. Mutants:
// let-fix-offset, let-fix-mut-always, let-fix-mut-never,
// let-fix-no-reparse.
func TestLetsMatchesTheTwin(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(40) {
			seeds = append(seeds, i+1)
		}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7a))
		var goSrc, twin strings.Builder
		head := "func f(n0 int) int {\n\tlet mut n = n0\n"
		goSrc.WriteString(head)
		twin.WriteString(head)
		decls := 1 + r.IntN(5)
		var sum []string
		for i := range decls {
			name := fmt.Sprintf("v%d", i)
			use := letUses[r.IntN(len(letUses))]
			reached[use.name] = true
			form := letForms[r.IntN(len(letForms))]
			kw := "let"
			if use.assigns {
				kw = "let mut"
			}
			fmt.Fprintf(&goSrc, form.goForm+"\n", name, kw, i)
			fmt.Fprintf(&twin, form.twin+"\n", name, kw, i)
			body := fmt.Sprintf(use.text, name, i) + "\n"
			goSrc.WriteString(body)
			twin.WriteString(body)
			sum = append(sum, name)
			if use.name == "closure" {
				sum = append(sum, fmt.Sprintf("g%d()", i))
			}
		}
		if r.IntN(4) == 0 {
			reached["no value"] = true
			goSrc.WriteString("\tvar z int\n")
			twin.WriteString("\tvar z int\n")
		}
		tail := "\tn + " + strings.Join(sum, " + ") + "\n}\n\nfunc main() {\n}\n"
		goSrc.WriteString(tail)
		twin.WriteString(tail)

		got, n := fix.Lets(goSrc.String())
		replay := fmt.Sprintf("replay: ERGO_SEED=%d go test -run TestLetsMatchesTheTwin ./internal/sketch/fix", seed)
		if got != twin.String() {
			t.Fatalf("seed %d: the fix wrote\n%s\nthe twin is\n%s\nfrom\n%s\n%s", seed, got, twin.String(), goSrc.String(), replay)
		}
		if n != decls {
			t.Fatalf("seed %d: %d rounds for %d declarations\n%s", seed, n, decls, replay)
		}
		if again, m := fix.Lets(got); m != 0 || again != got {
			t.Fatalf("seed %d: a second fix made %d edits\n%s", seed, m, replay)
		}
		if a, b := verdict(got), verdict(twin.String()); !slices.Equal(a, b) {
			t.Fatalf("seed %d: the fixed file checks %v, the twin %v\n%s", seed, a, b, replay)
		}
	}
	if len(seeds) > 1 {
		for _, k := range []string{"never", "same depth", "nested", "shadowed", "closure", "no value"} {
			if !reached[k] {
				t.Errorf("no seed wrote the %s position", k)
			}
		}
	}
}

// verdict is the checker's diagnostics of src, as text.
func verdict(src string) []string {
	var out []string
	for _, d := range sketchfront.Check(src).Diagnostics {
		out = append(out, fmt.Sprint(d))
	}
	return out
}

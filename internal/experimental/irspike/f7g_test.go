package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// orderProgram writes GenerateOrder(seed) into a temporary directory and
// loads it.
func orderProgram(t *testing.T, seed uint64) (lower.Unit, string, string) {
	t.Helper()
	text := GenerateOrder(seed)
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
	}
	return u, text, dir
}

// TestIRContinuePolls checks the fact continueStmt promises (F7 GP,
// "continue in a polling loop"): in an IR body whose loops end each
// iteration in the back-edge poll, every continue comes right after a
// poll, so no iteration skips it. It reads LowerIR's IR in process.
// Mutant f7g-continue-skips-poll: continue without its poll.
func TestIRContinuePolls(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 24)
	continues := 0
	for seed := first; seed < first+count; seed++ {
		u, text, _ := orderProgram(t, seed)
		files, _, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		var ir struct {
			Funcs []struct {
				Name string `json:"name"`
				Body []any  `json:"body"`
			} `json:"funcs"`
		}
		for _, f := range files {
			if f.Path == lower.IRFileName {
				if err := json.Unmarshal(f.Data, &ir); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, fn := range ir.Funcs {
			if !hasBackEdgePoll(fn.Body) {
				continue
			}
			walkLists(fn.Body, func(list []any) {
				for i, s := range list {
					if m, _ := s.(map[string]any); m["k"] == "continue" {
						continues++
						if i == 0 || !isPoll(list[i-1]) {
							t.Fatalf("seed %d: %s: a continue in a polling loop that does not poll first\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRContinuePolls ./internal/experimental/irspike\n%s", seed, fn.Name, seed, text)
						}
					}
				}
			})
		}
	}
	if count >= 24 && continues == 0 {
		t.Fatal("no IR body of a polling frame has a continue")
	}
}

// TestIROrderMatchesSource is the agreement property over GenerateOrder
// (F7 GP): each program prints, exits and fails the same on both paths,
// and the run gives IR bodies to functions with a continue in a polling
// loop, with operation calls inside an expression, in a condition and in
// a range, and with each try form (tryForms). A failing seed prints the Ergo source, both outcomes and its
// replay command. It builds Go because the question is what the compiled
// program does: the order of effects, polls and defects.
func TestIROrderMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 24)
	continued, traced, spanned, looped := 0, 0, 0, 0
	var tries [len(tryForms)]int
	for seed := first; seed < first+count; seed++ {
		u, text, dir := orderProgram(t, seed)
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIROrderMatchesSource ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
		for _, name := range rep.IR {
			if bodyHas(text, name, "continue") {
				continued++
			}
			if bodyHas(text, name, " tr(") || bodyHas(text, name, "(tr(") || bodyHas(text, name, "-tr(") {
				traced++
			}
			if bodyHas(text, name, "in span(") {
				spanned++
			}
			if bodyHas(text, name, "< tr(") {
				looped++
			}
			for i, re := range tryForms {
				if re.MatchString(bodyOf(text, name)) {
					tries[i]++
				}
			}
		}
	}
	if count >= 12 && (continued == 0 || traced == 0 || spanned == 0 || looped == 0 || slices.Contains(tries[:], 0)) {
		t.Fatalf("the run did not reach IR bodies with: a continue %d, an operation call inside an expression %d, a for-in over a call %d, a loop condition with a call %d, the try forms (try of a non-call, try inside an expression, a catch arm that continues, a catch arm whose value is an if; a positional record literal) %v", continued, traced, spanned, looped, tries)
	}
}

// tryForms are GenerateOrder's try forms (tryStmt), as they read in a
// body: a try over two calls, a try inside an expression, a catch arm
// that continues, and a catch arm whose value is an if.
var tryForms = [...]*regexp.Regexp{
	regexp.MustCompile(`try chk\([^\n]*\) \* 2 \+ chk\(`),
	regexp.MustCompile(`[(\[{-]\(?try chk\(`),
	regexp.MustCompile(`case E1:\n\s+continue\n`),
	regexp.MustCompile(`case E\d as x:\n\s+if `),
	regexp.MustCompile(`Pt\{[^:}]*\}`), // a positional record literal (orderExpr)
}

// bodyOf is function name's body in text.
func bodyOf(text, name string) string {
	_, rest, ok := strings.Cut(text, "func "+name+"(")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n}\n")
	return body
}

// bodyHas reports that function name's body in text contains s.
func bodyHas(text, name, s string) bool {
	return strings.Contains(bodyOf(text, name), s)
}

// hasBackEdgePoll reports a loop in body whose last statement is a poll.
func hasBackEdgePoll(body []any) bool {
	found := false
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k := v["k"]; k == "while" || k == "range" || k == "count" {
				if b, _ := v["body"].([]any); len(b) > 0 && isPoll(b[len(b)-1]) {
					found = true
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(body)
	return found
}

// walkLists calls f on every statement list in v.
func walkLists(v any, f func([]any)) {
	switch v := v.(type) {
	case map[string]any:
		for _, x := range v {
			walkLists(x, f)
		}
	case []any:
		f(v)
		for _, x := range v {
			walkLists(x, f)
		}
	}
}

// isPoll reports the poll statement: the task's Check.
func isPoll(s any) bool {
	m, _ := s.(map[string]any)
	x, _ := m["x"].(map[string]any)
	return m["k"] == "expr" && x["k"] == "mcall" && x["name"] == "Check"
}

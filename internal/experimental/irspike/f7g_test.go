package irspike

import (
	"encoding/json"
	"fmt"
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
// a range, and with each form of orderForms: seed s's function forced is
// an IR body of form s mod len(orderForms()), and the test's forms and the
// generator's schedule are the same set. A failing seed prints the Ergo source, both outcomes and its
// replay command. It builds Go because the question is what the compiled
// program does: the order of effects, polls and defects.
func TestIROrderMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 24)
	// Every form the test asks for is in GenerateOrder's schedule, and
	// every scheduled form is one it asks for.
	for _, f := range orderForms() {
		if orderFormRes[f.name] == nil {
			t.Fatalf("GenerateOrder schedules %q, which the test does not check", f.name)
		}
	}
	for name := range orderFormRes {
		if !slices.ContainsFunc(orderForms(), func(f orderForm) bool { return f.name == name }) {
			t.Fatalf("GenerateOrder's schedule has no %q", name)
		}
	}
	seen := map[string]int{}
	gated := 0
	for seed := first; seed < first+count; seed++ {
		u, text, dir := orderProgram(t, seed)
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := fmt.Sprintf("replay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIROrderMatchesSource ./internal/experimental/irspike", seed)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		// The seed's forced function is an IR body of its form.
		want := OrderFormName(uint64(seed))
		// settle's result holds the prelude's Never (orderDecls); a
		// program that calls settled lowers both.
		if strings.Contains(text, "settled(tr(") && (!slices.Contains(rep.IR, "settle") || !slices.Contains(rep.IR, "settled")) {
			t.Fatalf("seed %d: settle and settled are not IR bodies (IR %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
		// cut's body takes slices of a slice.
		if strings.Contains(text, "cut(tr(") && !slices.Contains(rep.IR, "cut") {
			t.Fatalf("seed %d: cut is not an IR body (IR %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
		// tagged's body takes len and an index of a named string.
		if strings.Contains(text, "tagged(tr(") && !slices.Contains(rep.IR, "tagged") {
			t.Fatalf("seed %d: tagged is not an IR body (IR %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
		// calm's body names a type whose argument is its error row.
		if strings.Contains(text, "calm(tr(") && !slices.Contains(rep.IR, "calm") {
			t.Fatalf("seed %d: calm is not an IR body (IR %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
		if !slices.Contains(rep.IR, "forced") || !orderFormRes[want].MatchString(bodyOf(text, "forced")) {
			t.Fatalf("seed %d: forced is not an IR body with %s (IR %v)\n%s\n%s", seed, want, rep.IR, replay, text)
		}
		for _, name := range rep.IR {
			for form, re := range orderFormRes {
				if re.MatchString(bodyOf(text, name)) {
					seen[form]++
				}
			}
			if name == "gate" && gateTails.MatchString(bodyOf(text, name)) {
				gated++
			}
		}
	}
	var missing []string
	for form := range orderFormRes {
		if seen[form] == 0 {
			missing = append(missing, form)
		}
	}
	if count >= uint64(len(orderForms())) && (len(missing) > 0 || gated == 0) {
		slices.Sort(missing)
		t.Fatalf("the run did not reach IR bodies with: %v (gate's failing branch tails: %d)", missing, gated)
	}
}

// orderFormRes are GenerateOrder's forms (orderForms), as each reads in
// a body.
var orderFormRes = map[string]*regexp.Regexp{
	"continue":                                 regexp.MustCompile(`\n\s+continue\n`),
	"call inside an expression":                regexp.MustCompile(`[ (-]tr\(`),
	"short-circuit operand with a call":        regexp.MustCompile(` > 0 && tr\(`),
	"step that gives no output":                regexp.MustCompile(`settled\(tr\(`),
	"len and index of a named string":          regexp.MustCompile(`tagged\(tr\(`),
	"slice of a slice":                         regexp.MustCompile(`cut\(tr\(`),
	"call with a call site":                    regexp.MustCompile(`here\(tr\(`),
	"error row as a type argument":             regexp.MustCompile(`calm\(tr\(`),
	"for-in over a call":                       regexp.MustCompile(`in span\(`),
	"loop condition with a call":               regexp.MustCompile(`< tr\(`),
	"try of a non-call":                        regexp.MustCompile(`try chk\([^\n]*\) \* 2 \+ chk\(`),
	"try inside an expression":                 regexp.MustCompile(`[(\[{-]\(?try chk\(`),
	"catch arm that continues":                 regexp.MustCompile(`case E1:\n\s+continue\n`),
	"catch arm whose value is an if":           regexp.MustCompile(`case E\d as x:\n\s+if `),
	"positional record literal":                regexp.MustCompile(`Pt\{[^:}]*\}`),
	"fallible call with an empty row":          regexp.MustCompile(`app\([^\n]*, func\(n int\) int \{`),
	"call of gate":                             regexp.MustCompile(`try gate\(`),
	"catch of a match":                         regexp.MustCompile(`\tcase _:\n[^\n]*\n\s*\} catch \{`),
	"op-assignment of a value with statements": regexp.MustCompile(` \+= if tr\(`),
}

// gateTails is gate's body (orderDecls): a branch tail that fails a body
// with no value.
var gateTails = regexp.MustCompile(`\n\t\tE1\{Code: n\}\n\t\} else if`)

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

// isPoll reports the poll statement: the task's Check, or a quiet loop's
// amortized poll (POLL-AMORT, lower/ir_poll.go), a block that counts the
// back edge down and checks when the count ends.
func isPoll(s any) bool {
	m, _ := s.(map[string]any)
	x, _ := m["x"].(map[string]any)
	if m["k"] == "block" {
		body, _ := m["body"].([]any)
		if len(body) != 2 {
			return false
		}
		dec, _ := body[0].(map[string]any)
		at, _ := body[1].(map[string]any)
		then, _ := at["then"].([]any)
		return dec["k"] == "set" && dec["op"] == "-" && at["k"] == "if" && len(then) == 2 && isPoll(then[1])
	}
	return m["k"] == "expr" && x["k"] == "mcall" && x["name"] == "Check"
}

package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// redactedShape is a generated Ergo type that holds a leaf type at a
// generated depth, behind records, unions, slices and Options, with the
// declarations it needs and a value of it that holds the leaf value.
type redactedShape struct {
	decls []string
	typ   string
	value string
	how   string
}

// redactedShapeOf wraps the leaf (its type and a value of it) in up to
// three generated layers; k names the declarations. options says whether
// an Option layer may be drawn.
func redactedShapeOf(r *rand.Rand, k int, leafType, leafValue string, options bool) redactedShape {
	s := redactedShape{typ: leafType, value: leafValue, how: leafType}
	for j := range r.IntN(4) {
		kinds := 3
		if options {
			kinds = 4
		}
		switch r.IntN(kinds) {
		case 0:
			name := fmt.Sprintf("r%d_%d", k, j)
			s.decls = append(s.decls, fmt.Sprintf("type %s { A string, B %s }", name, s.typ))
			s.typ, s.value = name, fmt.Sprintf("%s{A: \"a\", B: %s}", name, s.value)
		case 1:
			v, w, u := fmt.Sprintf("v%d_%d", k, j), fmt.Sprintf("w%d_%d", k, j), fmt.Sprintf("u%d_%d", k, j)
			s.decls = append(s.decls, fmt.Sprintf("type %s { X %s }", v, s.typ), fmt.Sprintf("type %s {}", w), fmt.Sprintf("type %s = %s | %s", u, v, w))
			s.typ, s.value = u, fmt.Sprintf("%s{X: %s}", v, s.value)
		case 2:
			s.typ, s.value = "[]"+s.typ, fmt.Sprintf("[]%s{%s}", s.typ, s.value)
		default:
			s.typ, s.value = "Option["+s.typ+"]", fmt.Sprintf("Some[%s]{Value: %s}", s.typ, s.value)
		}
		s.how = s.typ + " over " + s.how
	}
	return s
}

// redactedSecret is a secret no output writes by chance, as Ergo source
// for NewRedacted and as the text to look for: twelve capitals, or a
// ten-digit number.
func redactedSecret(r *rand.Rand) (leafType, source, text string) {
	if r.IntN(2) == 0 {
		b := make([]byte, 12)
		for i := range b {
			b[i] = byte('A' + r.IntN(26))
		}
		return "config.Redacted[string]", strconv.Quote(string(b)), string(b)
	}
	n := strconv.Itoa(1_000_000_000 + r.IntN(1_000_000_000))
	return "config.Redacted[int]", n, n
}

// TestRedactedRefusedAtCheck is the check-time half of config's rule that
// a secret has no encoded form (docs/spec/stdlib.md, config 2): over
// generated types that hold a config.Redacted at a generated depth,
// json.Encode and toml.Encode of a value of the type are refused, and the
// same type with a string in its place is accepted, so the refusal is the
// secret's; a Redacted itself has no text form ("\(r)" is refused).
//
// Mutant it kills, no other test does: config.Redacted declared as a
// record `type Redacted[T] { Value T }` built by an Ergo NewRedacted
// (the record has a wire form, so json.Encode of it is accepted).
// Replay: ERGO_REDACTED_SEED=n.
func TestRedactedRefusedAtCheck(t *testing.T) {
	first, last := 0, 40
	if s := os.Getenv("ERGO_REDACTED_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n+1
	}
	encodes := func(s redactedShape) string {
		return "import config\nimport json\nimport toml\n\n" + strings.Join(s.decls, "\n") +
			"\n\nfunc asJSON(v " + s.typ + ") string { json.Encode(json.Codec[" + s.typ + "]{MaxBytes: 0, MaxDepth: 0}, v) }\n" +
			"\nfunc asTOML(v " + s.typ + ") string fails toml.Unrepresentable { toml.Encode(v) }\n" +
			"\nfunc text(v config.Redacted[string]) string { \"\\(v)\" }\n" +
			"\nfunc main() {\n\tlet _ = asJSON(" + s.value + ")\n}\n"
	}
	for seed := first; seed < last; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 23))
		leafType, source, _ := redactedSecret(r)
		shapeSeed := r.Uint64()
		secret := redactedShapeOf(rand.New(rand.NewPCG(shapeSeed, 1)), 0, leafType, "config.NewRedacted("+source+")", false)
		plain := redactedShapeOf(rand.New(rand.NewPCG(shapeSeed, 1)), 0, "string", `"plain"`, false)
		src := encodes(secret)
		_, ds := check.CheckSource(src)
		refused := map[string]bool{}
		for _, d := range ds {
			if strings.Contains(d.Message, "Redacted") {
				refused[strconv.Itoa(d.Line)] = true
			}
		}
		lineOf := func(text, prefix string) string {
			for i, l := range strings.Split(text, "\n") {
				if strings.HasPrefix(l, prefix) {
					return strconv.Itoa(i + 1)
				}
			}
			return "?"
		}
		for _, f := range []string{"func asJSON", "func asTOML", "func text"} {
			if !refused[lineOf(src, f)] {
				t.Fatalf("seed %d: %s of %s is not refused for the secret:\n%s\ndiagnostics: %v\nreplay: ERGO_REDACTED_SEED=%d go test -run '^TestRedactedRefusedAtCheck$' ./internal/sketch/lower", seed, f, secret.how, src, ds, seed)
			}
		}
		control := strings.NewReplacer("import config\n", "", "func text(v config.Redacted[string])", "func text(v string)").Replace(encodes(plain))
		if _, ds := check.CheckSource(control); len(ds) > 0 {
			t.Fatalf("seed %d: the control %s is refused:\n%s\ndiagnostics: %v\nreplay: ERGO_REDACTED_SEED=%d go test -run '^TestRedactedRefusedAtCheck$' ./internal/sketch/lower", seed, plain.how, control, ds, seed)
		}
	}
}

// TestRedactedNeverPrints is the program half of config's rule that a
// secret never appears in rendered output (docs/spec/stdlib.md, config 1;
// the Go half is runtime/v1 TestRedactedNeverRenders): one generated test
// program holds a secret at a generated depth in each of its cases, behind
// records, unions, slices and Options, and
//
//   - lets a failure that holds it escape a test, so the test runner
//     writes the failure's text (the text of a failure is the one text of
//     a value: main's exit report and a trace write it too, lower text.go);
//     no output holds any secret;
//   - fails a property test over the type, so the runner writes a
//     generated counterexample; every Redacted in it is written as the
//     call of its builder, config.NewRedacted[T](…), with no argument.
//
// Mutant it kills, no other test does: config.Redacted declared as a
// record with an unexported field `type Redacted[T] { value T }` built
// by an Ergo NewRedacted: it has no wire form and no text form, so
// TestRedactedRefusedAtCheck holds, but a failure's text writes its
// fields. Replay: ERGO_REDACTED_SEED=n.
//
// It builds Go because the failure text and the counterexample are
// written by the built test binary, at run time.
func TestRedactedNeverPrints(t *testing.T) {
	needsBuild(t)
	seed := uint64(1)
	if s := os.Getenv("ERGO_REDACTED_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewPCG(seed, 29))
	// Two programs over the same types: the failures (esc) and the
	// counterexamples (gen), so a type that cannot be generated leaves the
	// failures' check standing.
	var esc, gen strings.Builder
	esc.WriteString("import config\n")
	gen.WriteString("import config\n")
	var secrets []string
	for k := range 16 {
		leafType, source, text := redactedSecret(r)
		s := redactedShapeOf(r, k, leafType, "config.NewRedacted("+source+")", true)
		secrets = append(secrets, text)
		decls := fmt.Sprintf("\n// %s\n%s\n", s.how, strings.Join(s.decls, "\n"))
		esc.WriteString(decls)
		gen.WriteString(decls)
		fmt.Fprintf(&esc, "type esc%d { V %s }\n", k, s.typ)
		fmt.Fprintf(&esc, "func fail%d(v %s) fails esc%d { return esc%d{V: v} }\n", k, s.typ, k, k)
		fmt.Fprintf(&esc, "test Esc%d() { try fail%d(%s) }\n", k, k, s.value)
		fmt.Fprintf(&gen, "test Gen%d(v %s) { assert false }\n", k, s.typ)
	}
	programs := []*lowered{lowerTestBuild(t, "redactedesc", esc.String())}
	genProgram, genErr := lowerTestBuildErr("redactedgen", gen.String())
	if genErr == nil {
		programs = append(programs, genProgram)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	runIt := func(src, exe string, each func(all string, fail func(string))) {
		out, errout, _ := runExeArgs(t, exe, "--seed", "1")
		all := out + errout
		each(all, func(why string) {
			t.Fatalf("seed %d: %s\nsource:\n%s\noutput:\n%s\nreplay: ERGO_REDACTED_SEED=%d go test -run '^TestRedactedNeverPrints$' ./internal/sketch/lower", seed, why, src, all, seed)
		})
	}
	runIt(esc.String(), exes["redactedesc"], func(all string, fail func(string)) {
		for k, secret := range secrets {
			if strings.Contains(all, secret) {
				fail(fmt.Sprintf("case %d: the output holds the secret %s", k, secret))
			}
			if !regexp.MustCompile(`(?m)^FAIL Esc` + strconv.Itoa(k) + `:`).MatchString(all) {
				fail(fmt.Sprintf("case %d: no failure line for Esc%d", k, k))
			}
		}
	})
	if genErr != nil {
		t.Fatal(genErr)
	}
	built := regexp.MustCompile(`config\.NewRedacted\[(string|int)\]\(…\)`)
	runIt(gen.String(), exes["redactedgen"], func(all string, fail func(string)) {
		for _, line := range strings.Split(all, "\n") {
			if strings.HasPrefix(line, "FAIL Gen") && strings.Contains(built.ReplaceAllString(line, ""), "Redacted") {
				fail("a counterexample writes a Redacted other than as its builder's call: " + line)
			}
		}
	})
}

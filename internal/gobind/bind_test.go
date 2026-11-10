package gobind_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/std/gostd"
)

// TestBindMappingProperty generates Go packages whose declarations draw on
// the whole type grammar of interop.md's Type mapping, binds them with
// ergo bind's own path (go list, export data, both platforms), and holds
// each declaration's verdict and Ergo signature to ref, a reference written
// from the table, not from gobind.Map. Every binding is in ergo fmt's form.
// A failing seed prints its Go source and both verdicts; ERGO_BIND_SEED
// replays one seed.
//
// Mutants it alone catches: uintptr mapped to uint64; (T, bool) mapped to
// Option[T]; chan admitted; any admitted; the variadic spread mark
// dropped; a nil-able *S result mapped without Option; a declaration of one
// platform kept; a Go [N]T refused, or bound with its slice or Map elements
// (batch-14 apply 1). No binary is built: go/types reads export data in process.
func TestBindMappingProperty(t *testing.T) {
	// Seed 13 reaches an array whose elements hold a slice or a map.
	seeds := []uint64{1, 2, 3, 4, 5, 6, 13}
	if s := os.Getenv("ERGO_BIND_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_BIND_SEED: %v", err)
		}
		seeds = []uint64{n}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.test/bindprop\n\ngo 1.27\n")
	want := map[string]map[string]verdict{}
	sources := map[string]string{}
	var patterns []string
	for i, seed := range seeds {
		name := fmt.Sprintf("p%d", seed)
		src, decls := genPackage(name, rand.New(rand.NewPCG(seed, 0x6b1d)))
		write(t, filepath.Join(dir, name, name+".go"), src)
		if i == 0 {
			// Platform files: a declaration of one platform, and one whose
			// Go type differs between them, are refused.
			write(t, filepath.Join(dir, name, "only_darwin.go"), "package "+name+"\n\nfunc OnlyDarwin() {}\n\nfunc Differs() int32 { return 0 }\n")
			write(t, filepath.Join(dir, name, "only_linux.go"), "package "+name+"\n\nfunc OnlyLinux() {}\n\nfunc Differs() int64 { return 0 }\n")
			decls["OnlyDarwin"] = verdict{refused: true}
			decls["OnlyLinux"] = verdict{refused: true}
			decls["Differs"] = verdict{refused: true}
		}
		want["example.test/bindprop/"+name] = decls
		sources["example.test/bindprop/"+name] = src
		patterns = append(patterns, "./"+name)
	}
	files, err := gobind.Bind(dir, os.Environ(), "go1.27", patterns...)
	if err != nil {
		t.Fatal(err)
	}
	for path, decls := range want {
		text, ok := files[path]
		if !ok {
			t.Fatalf("no binding for %s", path)
		}
		if again, err := format.Source(text); err != nil || again != text {
			t.Errorf("binding of %s is not in ergo fmt form (err %v)\n%s", path, err, text)
		}
		// Every binding checks clean: the checker resolves each of its
		// declarations with no diagnostic (IG1's acceptance, IG2's checker).
		set := check.NewGoBindings(func(p string) (string, bool) {
			if text, ok := files[p]; ok {
				return text, true
			}
			return gostd.Binding(p)
		})
		set.ResolveAll(path)
		if ds := set.Diagnostics(); len(ds) > 0 {
			t.Errorf("binding of %s does not check (replay: ERGO_BIND_SEED=%s):\n%s\n--- binding:\n%s", path, strings.TrimPrefix(filepath.Base(path), "p"), strings.Join(ds, "\n"), text)
		}
		got := readBinding(t, path, text)
		var bad []string
		for name, w := range decls {
			g, ok := got[name]
			switch {
			case !ok:
				bad = append(bad, fmt.Sprintf("%s: missing; want %s", name, w))
			case g != w:
				bad = append(bad, fmt.Sprintf("%s:\n  got  %s\n  want %s", name, g, w))
			}
		}
		for name := range got {
			if _, ok := decls[name]; !ok {
				bad = append(bad, fmt.Sprintf("%s: not exported, yet bound: %s", name, got[name]))
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			t.Errorf("seed package %s (replay: ERGO_BIND_SEED=%s):\n%s\n--- Go source:\n%s\n--- binding:\n%s", path, strings.TrimPrefix(filepath.Base(path), "p"), strings.Join(bad, "\n"), sources[path], text)
		}
	}
}

// TestGoStdBindingsMatchToolchain regenerates the embedded bindings of
// Go's standard library with the host toolchain and compares them; the
// checker reads their rows in place of the deleted v1.PureErrorFuncs (G4's
// error rule, IG2). Mutant: any edit of a
// generated binding, or a mapping change not regenerated.
func TestGoStdBindingsMatchToolchain(t *testing.T) {
	v, err := gobind.GoVersion(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	embedded := gostd.Paths()
	if len(embedded) == 0 {
		t.Fatal("no embedded binding")
	}
	stamp, _ := gostd.Binding("strings")
	if !strings.Contains(stamp, " "+v+" ") {
		t.Skipf("the bindings are of another toolchain than %s; go generate ./internal/sketch/std/gostd regenerates them", v)
	}
	files, err := gobind.StdBindings(t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if strings.Join(paths, " ") != strings.Join(embedded, " ") {
		t.Errorf("the toolchain binds other packages than the embedded set; run go generate ./internal/sketch/std/gostd")
	}
	for _, p := range paths {
		if got, _ := gostd.Binding(p); got != files[p] {
			t.Errorf("the embedded binding of %s differs from the toolchain's; run go generate ./internal/sketch/std/gostd", p)
		}
	}
}

func write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// verdict is one declaration of a binding: refused, or its Ergo text with
// spaces collapsed and whether it is variadic.
type verdict struct {
	refused  bool
	ergo     string
	variadic bool
}

func (v verdict) String() string {
	if v.refused {
		return "refused"
	}
	if v.variadic {
		return "variadic " + v.ergo
	}
	return v.ergo
}

var space = regexp.MustCompile(`\s+`)

func squash(s string) string { return strings.TrimSpace(space.ReplaceAllString(s, " ")) }

// readBinding reads a binding file by its fixed form (interop.md, Binding
// files): `// ergo:refused NAME: reason`, `// ergo:variadic`, and `extern go
// path.NAME decl`, a type's members up to its closing brace.
func readBinding(t *testing.T, path, text string) map[string]verdict {
	out := map[string]verdict{}
	lines := strings.Split(text, "\n")
	variadic := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "// ergo:refused "):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, "// ergo:refused "), ": ")
			out[name] = verdict{refused: true}
		case line == "// ergo:variadic":
			variadic = true
		case strings.HasPrefix(line, "extern go "+path+"."):
			rest := strings.TrimPrefix(line, "extern go "+path+".")
			name, decl, _ := strings.Cut(rest, " ")
			for strings.HasSuffix(strings.TrimSpace(lines[i]), "{") && i+1 < len(lines) {
				for i++; i < len(lines) && lines[i] != "}"; i++ {
					decl += " " + lines[i]
				}
				decl += " }"
			}
			out[name] = verdict{ergo: squash(decl), variadic: variadic}
			variadic = false
		}
	}
	return out
}

// ---------------------------------------------------------------- generator and reference

// gt is a Go type of the generator's grammar.
type gt struct {
	kind    string // basic, uintptr, D, S, I, reader, duration, error, any, iface, struct, ptrS, ptrInt, slice, map, array, chan, func, seq, tparam
	name    string // a basic type's or a type parameter's name
	elem    *gt
	key     *gt
	params  []*gt
	results []*gt
}

// at is the position of a type, as the table distinguishes them.
type at int

const (
	param at = iota
	result
	errResult
	inner
)

func (t *gt) goText() string {
	switch t.kind {
	case "basic", "tparam":
		return t.name
	case "uintptr":
		return "uintptr"
	case "D", "S", "I":
		return t.kind
	case "reader":
		return "io.Reader"
	case "duration":
		return "time.Duration"
	case "error":
		return "error"
	case "any":
		return "any"
	case "iface":
		return "interface{ M() }"
	case "struct":
		return "struct{ X int }"
	case "ptrS":
		return "*S"
	case "ptrInt":
		return "*int"
	case "slice":
		return "[]" + t.elem.goText()
	case "map":
		return "map[" + t.key.goText() + "]" + t.elem.goText()
	case "array":
		return "[3]" + t.elem.goText()
	case "chan":
		return "chan " + t.elem.goText()
	case "seq":
		return "iter.Seq[" + t.elem.goText() + "]"
	case "func":
		var ps, rs []string
		for _, p := range t.params {
			ps = append(ps, p.goText())
		}
		for _, r := range t.results {
			rs = append(rs, r.goText())
		}
		s := "func(" + strings.Join(ps, ", ") + ")"
		switch len(rs) {
		case 0:
		case 1:
			s += " " + rs[0]
		default:
			s += " (" + strings.Join(rs, ", ") + ")"
		}
		return s
	}
	panic(t.kind)
}

// ref maps a type by interop.md's table, straight from its rows. ok is
// false for a refused type. cb is set inside a callback.
func ref(t *gt, pos at, cb bool) (string, bool) {
	switch t.kind {
	case "basic", "tparam":
		return t.name, true
	case "D", "S", "I":
		return t.kind, true
	case "reader":
		return "io.Reader", true
	case "duration":
		return "duration", true
	case "error":
		return "go.Error", true
	case "ptrS":
		if cb && pos != param {
			return "", false // a callback's pointer result may be nil (IG6)
		}
		switch pos {
		case param:
			return "*S", true
		case errResult:
			return "go.Ref[S]", true
		case result:
			return "Option[go.Ref[S]]", true
		}
		return "", false
	case "slice":
		e, ok := ref(t.elem, inner, cb)
		return "[]" + e, ok
	case "map":
		k, ok1 := ref(t.key, inner, cb)
		v, ok2 := ref(t.elem, inner, cb)
		return "Map[" + k + ", " + v + "]", ok1 && ok2
	case "seq":
		e, ok := ref(t.elem, inner, cb)
		return "Seq[" + e + "]", ok
	case "array":
		// Go copies an array whole; an element that holds a slice or a
		// Map would be shared, and is refused (batch-14 apply 1).
		if holdsShared(t.elem) {
			return "", false
		}
		e, ok := ref(t.elem, inner, cb)
		return "[3]" + e, ok
	case "func":
		if pos != param || cb {
			return "", false
		}
		var ps []string
		for _, p := range t.params {
			s, ok := ref(p, param, true)
			if !ok {
				return "", false
			}
			ps = append(ps, s)
		}
		res, fails, ok := refResults(t.results, true)
		if !ok || strings.HasPrefix(res, "(") {
			return "", false // one result at most (IG6)
		}
		// A generated package is in no reviewed table of synchronous
		// callbacks, so every callback is escaping (IG6).
		s := "escaping func(" + strings.Join(ps, ", ") + ")"
		if res != "" {
			s += " " + res
		}
		if fails {
			s += " fails go.Error"
		}
		return s, true
	}
	// uintptr, any, an unnamed interface or struct, *int, a chan
	return "", false
}

// holdsShared reports a generated type whose copy shares memory: a slice,
// a map, or an array of one.
func holdsShared(t *gt) bool {
	switch t.kind {
	case "slice", "map":
		return true
	case "array":
		return holdsShared(t.elem)
	}
	return false
}

// refResults maps results: a last error is the row fails go.Error; the one
// result beside it is at errResult; several are a tuple.
func refResults(rs []*gt, cb bool) (string, bool, bool) {
	fails := len(rs) > 0 && rs[len(rs)-1].kind == "error"
	if fails {
		rs = rs[:len(rs)-1]
	}
	var parts []string
	for _, r := range rs {
		pos := result
		if fails && len(rs) == 1 {
			pos = errResult
		}
		s, ok := ref(r, pos, cb)
		if !ok {
			return "", false, false
		}
		parts = append(parts, s)
	}
	switch len(parts) {
	case 0:
		return "", fails, true
	case 1:
		return parts[0], fails, true
	}
	return "(" + strings.Join(parts, ", ") + ")", fails, true
}

type gen struct {
	r       *rand.Rand
	tparams []string // type parameters in scope
}

var basics = []string{"bool", "string", "int", "int8", "uint16", "uint64", "float32", "float64", "complex128", "byte", "rune"}

func (g *gen) typ(depth int) *gt {
	kinds := []string{"basic", "basic", "basic", "uintptr", "D", "S", "I", "reader", "duration", "error", "any", "iface", "struct", "ptrS", "ptrS", "ptrInt"}
	if len(g.tparams) > 0 {
		kinds = append(kinds, "tparam", "tparam")
	}
	if depth < 2 {
		kinds = append(kinds, "slice", "slice", "map", "array", "chan", "func", "seq")
	}
	t := &gt{kind: kinds[g.r.IntN(len(kinds))]}
	switch t.kind {
	case "basic":
		t.name = basics[g.r.IntN(len(basics))]
	case "tparam":
		t.name = g.tparams[g.r.IntN(len(g.tparams))]
	case "slice", "array", "chan", "seq":
		t.elem = g.typ(depth + 1)
	case "map":
		keys := []*gt{{kind: "basic", name: "string"}, {kind: "basic", name: "int"}, {kind: "D"}}
		t.key = keys[g.r.IntN(len(keys))]
		t.elem = g.typ(depth + 1)
	case "func":
		for range g.r.IntN(3) {
			t.params = append(t.params, g.typ(depth+1))
		}
		t.results = g.results(depth + 1)
	}
	return t
}

func (g *gen) results(depth int) []*gt {
	switch g.r.IntN(6) {
	case 0:
		return nil
	case 1:
		return []*gt{g.typ(depth)}
	case 2:
		return []*gt{g.typ(depth), {kind: "basic", name: "bool"}}
	case 3:
		return []*gt{g.typ(depth), {kind: "error"}}
	case 4:
		return []*gt{g.typ(depth), g.typ(depth), {kind: "error"}}
	}
	return []*gt{{kind: "error"}}
}

// genPackage writes a Go package of random declarations and the verdict
// ref gives each exported one.
func genPackage(name string, r *rand.Rand) (string, map[string]verdict) {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"cmp\"\n\t\"io\"\n\t\"iter\"\n\t\"time\"\n)\n\n", name)
	b.WriteString("type S struct{ X int }\n\ntype D int\n\ntype I interface{ M() int }\n\n")
	b.WriteString("var (\n\t_ io.Reader\n\t_ time.Duration\n\t_ iter.Seq[int]\n)\n\nfunc _[T cmp.Ordered]() {}\n\n")
	want := map[string]verdict{
		"S": {ergo: "type S { X int, }"},
		"D": {ergo: "type D int"},
		"I": {ergo: "type I { M() int needs go.Foreign, }"},
	}
	g := &gen{r: r}
	for i := range 14 {
		switch r.IntN(10) {
		case 0, 1, 2, 3, 4:
			g.function(&b, want, fmt.Sprintf("F%d", i), "")
		case 5:
			g.function(&b, want, fmt.Sprintf("f%d", i), "") // unexported: absent
		case 6:
			g.function(&b, want, fmt.Sprintf("V%d", i), "s S")
		case 7:
			g.function(&b, want, fmt.Sprintf("P%d", i), "s *S")
		case 8:
			g.constant(&b, want, fmt.Sprintf("C%d", i))
		case 9:
			if r.IntN(2) == 0 {
				fmt.Fprintf(&b, "var E%d error\n\n", i)
				want[fmt.Sprintf("E%d", i)] = verdict{ergo: fmt.Sprintf("let E%d go.Error", i)}
			} else {
				fmt.Fprintf(&b, "var X%d int\n\n", i)
				want[fmt.Sprintf("X%d", i)] = verdict{refused: true}
			}
		}
	}
	return b.String(), want
}

func (g *gen) constant(b *strings.Builder, want map[string]verdict, name string) {
	cases := []struct{ decl, ergo string }{
		{"D = 3", "D"},
		{"time.Duration = 5", "duration"},
		{"= 7", "int"},
		{"= 1 << 64", ""},
		{"= 2.5", "float64"},
		{"= \"s\"", "string"},
		{"= 'x'", "rune"},
		{"uint8 = 9", "uint8"},
	}
	c := cases[g.r.IntN(len(cases))]
	fmt.Fprintf(b, "const %s %s\n\n", name, c.decl)
	if c.ergo == "" {
		want[name] = verdict{refused: true}
		return
	}
	want[name] = verdict{ergo: "let " + name + " " + c.ergo}
}

// function writes a function, or a method with recv, and its verdict.
func (g *gen) function(b *strings.Builder, want map[string]verdict, name, recv string) {
	r := g.r
	g.tparams = nil
	var goTP, ergoTP []string
	refused := false
	var params []string // Go params
	var ergoParams []string
	if recv == "" && r.IntN(4) == 0 {
		switch r.IntN(7) {
		case 0:
			goTP, ergoTP, g.tparams = []string{"T any"}, []string{"T"}, []string{"T"}
		case 1:
			goTP, ergoTP, g.tparams = []string{"T comparable"}, []string{"T comparable"}, []string{"T"}
		case 2:
			goTP, ergoTP, g.tparams = []string{"T cmp.Ordered"}, []string{"T ordered"}, []string{"T"}
		case 3:
			goTP, ergoTP, g.tparams = []string{"Q ~[]E", "E any"}, []string{"E"}, []string{"E"}
			params, ergoParams = append(params, "q Q"), append(ergoParams, "q []E")
		case 4:
			goTP, ergoTP, g.tparams = []string{"M ~map[K]V", "K comparable", "V any"}, []string{"K comparable", "V"}, []string{"K", "V"}
			params, ergoParams = append(params, "m M"), append(ergoParams, "m Map[K, V]")
		case 5:
			goTP, g.tparams, refused = []string{"T interface{ ~int | ~string }"}, []string{"T"}, true
		case 6:
			goTP, g.tparams, refused = []string{"T any", "U comparable"}, []string{"T", "U"}, true
		}
	}
	for i := range r.IntN(4) {
		t := g.typ(0)
		params = append(params, fmt.Sprintf("a%d %s", i, t.goText()))
		s, ok := ref(t, param, false)
		refused = refused || !ok
		ergoParams = append(ergoParams, fmt.Sprintf("a%d %s", i, s))
	}
	variadic := r.IntN(4) == 0
	if variadic {
		t := g.typ(1)
		params = append(params, "rest ..."+t.goText())
		s, ok := ref(t, inner, false)
		refused = refused || !ok
		ergoParams = append(ergoParams, "rest []"+s)
	}
	results := g.results(0)
	res, fails, ok := refResults(results, false)
	refused = refused || !ok
	var goRes []string
	for _, t := range results {
		goRes = append(goRes, t.goText())
	}
	head := "func "
	ergoHead := "func "
	key := name
	if recv != "" {
		head += "(" + recv + ") "
		ergoHead += "(" + recv + ") "
		key = "S." + name
	}
	tp := ""
	if len(goTP) > 0 {
		tp = "[" + strings.Join(goTP, ", ") + "]"
	}
	fmt.Fprintf(b, "%s%s%s(%s) (%s) { panic(0) }\n\n", head, name, tp, strings.Join(params, ", "), strings.Join(goRes, ", "))
	if name[0] >= 'a' && name[0] <= 'z' {
		return
	}
	if refused {
		want[key] = verdict{refused: true}
		return
	}
	e := ergoHead + name
	if len(ergoTP) > 0 {
		e += "[" + strings.Join(ergoTP, ", ") + "]"
	}
	e += "(" + strings.Join(ergoParams, ", ") + ")"
	if res != "" {
		e += " " + res
	}
	if fails {
		e += " fails go.Error"
	}
	e += " needs go.Foreign"
	want[key] = verdict{ergo: squash(e), variadic: variadic}
}

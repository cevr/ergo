package bindc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	goformat "go/format"
	goparser "go/parser"
	gotoken "go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The Ergo names of a binding (names.go, Binding.ergoNames). The oracle of
// these tests is the Ergo checker itself (internal/sketch/check, read
// only): an emitted text is conflict-free when the checker reports no
// declaration in it as already declared, declared twice or a prelude name,
// and no extern has two parameters with one name. The product reads no
// diagnostics: it derives its reserved set from the syntax keywords and
// the checker's predeclared set (check.IsPredeclared).

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkerRefuses returns the names of names that the checker refuses as a
// parameter name for being predeclared (D-D3). Keywords and reserved words
// do not parse as names and are left out.
func checkerRefuses(t *testing.T, names []string) map[string]bool {
	t.Helper()
	var src strings.Builder
	at := map[int]string{}
	line := 1
	for i, n := range names {
		if syntax.IsKeyword(n) {
			continue
		}
		at[line] = n
		fmt.Fprintf(&src, "func p%d(%s int) {\n}\n", i, n)
		line += 2
	}
	_, ds := check.CheckSource(src.String())
	out := map[string]bool{}
	for _, d := range ds {
		n, ok := at[d.Line]
		if ok && strings.HasPrefix(d.Message, n+" is a prelude name") {
			out[n] = true
		}
	}
	return out
}

// checkerDomain is the set of names the predeclared names are compared
// on: every identifier in a string literal of the checker's source (its
// universe names are literals there), every identifier of the prelude's
// source, and Go's keywords and predeclared names.
func checkerDomain(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	dir := filepath.Join("..", "sketch", "check")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := gotoken.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := goparser.ParseFile(fset, filepath.Join(dir, name), nil, goparser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == gotoken.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && identRE.MatchString(s) {
					set[s] = true
				}
			}
			return true
		})
	}
	for _, src := range std.Prelude() {
		for _, w := range regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`).FindAllString(src, -1) {
			set[w] = true
		}
	}
	for _, m := range []map[string]bool{goKeywords, goPredeclared} {
		for n := range m {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestErgoPredeclaredMatchesChecker: on the checker domain, a name is in
// the product's predeclared set exactly when the checker refuses it as a
// parameter name. A control: the checker refuses Option and len, so the
// probe sees the checker's refusals.
func TestErgoPredeclaredMatchesChecker(t *testing.T) {
	domain := checkerDomain(t)
	refused := checkerRefuses(t, domain)
	if !refused["Option"] || !refused["len"] {
		t.Fatalf("control: the checker refuses %d names of %d, not Option and len; the probe is broken", len(refused), len(domain))
	}
	for _, n := range domain {
		if syntax.IsKeyword(n) {
			continue
		}
		if got := ergoPredeclared(n); got != refused[n] {
			t.Errorf("%s: predeclared %v, the checker refuses it: %v", n, got, refused[n])
		}
	}
	t.Logf("domain %d names, the checker refuses %d", len(domain), len(refused))
}

// ergoConflicts is the oracle: the checker's diagnostics of src that name a
// declaration that collides, and every extern with two parameters of one
// name. The checker refuses an extern's parameter as it refuses a
// function's (D-D3: a prelude name is "is a prelude name"; a name used
// twice is "already a parameter", which this filter leaves out), so the
// two parameters of one name are also checked on the syntax tree, and
// TestErgoParamProperty checks that none is reserved.
func ergoConflicts(t *testing.T, src string) []string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		return []string{"does not parse: " + err.Error()}
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*syntax.FuncDecl)
		if !ok || fn.Type.Params == nil {
			continue
		}
		seen := map[string]bool{}
		for _, p := range fn.Type.Params.List {
			for _, n := range p.Names {
				if seen[n.Name] {
					out = append(out, fmt.Sprintf("func %s: two parameters %s", fn.Name.Name, n.Name))
				}
				seen[n.Name] = true
			}
		}
	}
	_, ds := check.CheckSource(src)
	for _, d := range ds {
		if strings.Contains(d.Message, "already declared") || strings.Contains(d.Message, "declared twice") ||
			strings.Contains(d.Message, "is a prelude name") {
			out = append(out, d.String())
		}
	}
	return out
}

// ergoParams maps each extern of src to its parameter names.
func ergoParams(t *testing.T, src string) map[string][]string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("the Ergo text does not parse: %v", err)
	}
	out := map[string][]string{}
	for _, d := range f.Decls {
		if fn, ok := d.(*syntax.FuncDecl); ok {
			ps := []string{}
			for _, p := range fn.Type.Params.List {
				for _, n := range p.Names {
					ps = append(ps, n.Name)
				}
			}
			out[fn.Name.Name] = ps
		}
	}
	return out
}

// TestErgoConflictsControl: the oracle sees each kind of collision. The
// prelude-name row redeclares a prelude function: since owner q30 (L6) a
// package type may take a prelude type's name (type Option is legal), but a
// prelude function's name stays S108.
func TestErgoConflictsControl(t *testing.T) {
	for _, src := range []string{
		"import go ergo.local/bind/fixture\n\ntype Unreachable {\n\tV uint32,\n}\n",
		"import go ergo.local/bind/fixture\n\ntype CallError = NotLoaded{} | Destroyed{}\n\nextern go ergo.local/bind/fixture.CallError func CallError() uint32 fails CallError\n",
		"import go ergo.local/bind/fixture\n\ntype S = A{} | B{}\n\ntype T = A{} | C{}\n",
		"import go ergo.local/bind/fixture\n\nextern go ergo.local/bind/fixture.F func F(a uint32, a uint32) uint32\n",
	} {
		if len(ergoConflicts(t, src)) == 0 {
			t.Errorf("the oracle sees no conflict in\n%s", src)
		}
	}
}

// bindErgo binds a fixture variant and emits its Ergo text. It returns the
// Bind error, or the text.
func bindErgo(t *testing.T, extra string) (*Binding, string, error) {
	t.Helper()
	return bindErgoImport(t, extra, "fixture")
}

// bindErgoImport is bindErgo with the Go package imported from
// ergo.local/bind/<base> (go.package is base when Go allows it as a
// package name, else the fixture's). Bind must refuse an import that the
// Ergo text cannot declare: ErgoText failing after Bind accepted fails the
// test.
func bindErgoImport(t *testing.T, extra, base string) (*Binding, string, error) {
	t.Helper()
	h, ct := fixtureVariant(t, extra, nil)
	var c map[string]any
	if err := json.Unmarshal(ct, &c); err != nil {
		t.Fatal(err)
	}
	g := c["go"].(map[string]any)
	g["import_path"] = "ergo.local/bind/" + base
	if isGoIdent(base) && !goKeywords[base] && base != "_" {
		g["package"] = base
	}
	ct, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bindVariant(t, h, ct)
	if err != nil {
		if ct2, skipped := skipCallPlans(t, err, ct); len(skipped) > 0 {
			b, err = bindVariant(t, h, ct2)
		}
	}
	if err != nil {
		return nil, "", err
	}
	out, err := ErgoText(b)
	if err != nil {
		t.Fatalf("ErgoText: %v", err)
	}
	return b, string(out), nil
}

// TestErgoNameRegressions: the cases of the wave 1d verifier (M1, M2;
// wave-1d-receipts/probe_test.go TestReviewNames) and their neighbours.
// Each binds to a text with no conflict and has the wanted extern, or Bind
// refuses it with the wanted reason.
func TestErgoNameRegressions(t *testing.T) {
	cases := []struct {
		name, extra string
		want        string // a line of the Ergo text, or with refuse a part of the Bind error
		refuse      bool
	}{
		// M1: two parameters that the per-name rewrite gave one name.
		{"parameter-keyword", "uint32_t fx_probe(uint32_t context, uint32_t context_arg);",
			"func Probe(contextArg uint32, contextArg2 uint32) uint32", false},
		{"go-keyword", "uint32_t fx_probe(uint32_t type, uint32_t type_arg);",
			"func Probe(typeArg uint32, typeArg2 uint32) uint32", false},
		{"parameter-trailing", "uint32_t fx_probe(uint32_t value_, uint32_t value);",
			"func Probe(value uint32, value2 uint32) uint32", false},
		{"ergo-keyword", "uint32_t fx_probe(uint32_t match, uint32_t needs);",
			"func Probe(matchArg uint32, needsArg uint32) uint32", false},
		{"field-method", "typedef struct fx_extra { uint32_t fill; } fx_extra;\nuint32_t fx_extra_read(const fx_extra *extra);",
			"func ExtraRead(extra Extra) uint32", false},
		{"type-function", "uint32_t fx_point_(void);", "fx_point and fx_point_ have the Go name Point", true},
		{"predeclared-parameter", "uint32_t fx_probe(uint32_t len, uint32_t len_arg, uint32_t string);",
			"func Probe(lenArg uint32, lenArg2 uint32, stringArg uint32) uint32", false},
		{"import-parameter", "uint32_t fx_probe(uint32_t fixture, fx_context *ctx);",
			"func Probe(fixture2 uint32, ctx *fixture.Context) uint32", false},
		{"go-word-parameter", "uint32_t fx_probe(uint32_t nil, uint32_t make, uint32_t any);",
			"func Probe(nil uint32, make uint32, any uint32) uint32", false},
		{"unnamed-keyword-type", "typedef uint32_t fx_match;\nuint32_t fx_probe(fx_match, fx_match, uint32_t match_arg);",
			"func Probe(matchArg fixture.Match, matchArg2 fixture.Match, matchArg3 uint32) uint32", false},
		// M2: a package-level name of two kinds, or a predeclared one.
		{"function-union", "uint32_t fx_call_error(void);",
			"the CallError union and function fx_call_error have the Ergo name CallError", true},
		{"function-variant", "uint32_t fx_invalid_argument(void);",
			"status code FX_INVALID_ARGUMENT and function fx_invalid_argument have the Ergo name InvalidArgument", true},
		{"function-callerror-variant", "uint32_t fx_not_loaded(void);",
			"a variant of the CallError union and function fx_not_loaded have the Ergo name NotLoaded", true},
		{"function-status", "uint32_t fx_status_(void);",
			"the generated package and fx_status_ have the Go name Status", true},
		{"record-prelude", "typedef struct fx_option { uint32_t v; } fx_option;\nuint32_t fx_read_option(fx_option option);",
			"an Ergo keyword or predeclared name and struct fx_option have the Ergo name Option", true},
		{"function-prelude", "uint32_t fx_some(void);",
			"an Ergo keyword or predeclared name and function fx_some have the Ergo name Some", true},
		{"variant-prelude", "#define FX_NONE (-INT32_C(40))",
			"an Ergo keyword or predeclared name and status code FX_NONE have the Ergo name None", true},
		{"variant-record", "#define FX_PROBE (-INT32_C(41))\ntypedef struct fx_probe { uint32_t v; } fx_probe;\nuint32_t fx_read_probe(fx_probe probe);",
			"status code FX_PROBE and struct fx_probe have the Ergo name Probe", true},
		{"variant-function", "#define FX_PROBE (-INT32_C(41))\nuint32_t fx_probe(void);",
			"status code FX_PROBE and function fx_probe have the Ergo name Probe", true},
		{"variant-generated", "#define FX_TOO_LONG (-INT32_C(42))",
			"a variant of the CallError union and status code FX_TOO_LONG have the Ergo name TooLong", true},
		// Not a collision: a raw-only function declares no Ergo name, and a
		// record that no typed signature reaches is not emitted.
		{"raw-only-prelude", "void fx_some(void *p);", "// not bound: fx_some (", false},
		{"unreached-record", "typedef struct fx_option { uint32_t v; } fx_option;", "extern go ergo.local/bind/fixture.AbiVersion", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, src, err := bindErgo(t, c.extra)
			if c.refuse {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("Bind error %v, want one with %q\n%s", err, c.want, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			if !strings.Contains(src, c.want) {
				t.Errorf("the Ergo text has no %q:\n%s", c.want, src)
			}
			for _, x := range ergoConflicts(t, src) {
				t.Errorf("conflict: %s", x)
			}
		})
	}
}

// TestErgoGoAgreement (the wave 1d verifier's TestReviewAgreement): for
// both golden headers, every extern of the Ergo text has the parameter
// and result types of the typed Go function of its name in the golden Go
// package, in order, the error result and the import qualifier removed and
// fixed array lengths erased (the Ergo text has []T until 196y).
func TestErgoGoAgreement(t *testing.T) {
	dims := regexp.MustCompile(`\[[0-9]+\]`)
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			b := bindHeader(t, h.dir, h.header, parseHeader(t, h.dir, h.header))
			out, err := ErgoText(b)
			if err != nil {
				t.Fatal(err)
			}
			src := string(out)
			ef, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			norm := func(s string) string {
				return dims.ReplaceAllString(strings.ReplaceAll(s, b.Contract.Go.Package+".", ""), "[]")
			}
			goSigs := map[string][]string{}
			for name, gsrc := range goldenGoFiles(t, h.dir) {
				fset := gotoken.NewFileSet()
				gf, err := goparser.ParseFile(fset, name, gsrc, 0)
				if err != nil {
					t.Fatal(err)
				}
				render := func(e ast.Expr) string {
					var x bytes.Buffer
					if err := goformat.Node(&x, fset, e); err != nil {
						t.Fatal(err)
					}
					return norm(x.String())
				}
				for _, d := range gf.Decls {
					fn, ok := d.(*ast.FuncDecl)
					if !ok || fn.Recv != nil || !fn.Name.IsExported() {
						continue
					}
					var types []string
					for _, p := range fn.Type.Params.List {
						for range max(1, len(p.Names)) {
							types = append(types, render(p.Type))
						}
					}
					types = append(types, "->")
					if fn.Type.Results != nil {
						for _, r := range fn.Type.Results.List {
							if render(r.Type) != "error" {
								types = append(types, render(r.Type))
							}
						}
					}
					goSigs[fn.Name.Name] = types
				}
			}
			at := func(n syntax.Node) string { return norm(src[int(n.Pos()):int(n.End())]) }
			count := 0
			for _, d := range ef.Decls {
				fn, ok := d.(*syntax.FuncDecl)
				if !ok {
					continue
				}
				count++
				var types []string
				for _, p := range fn.Type.Params.List {
					for range max(1, len(p.Names)) {
						types = append(types, at(p.Type))
					}
				}
				types = append(types, "->")
				if fn.Type.Results != nil {
					for _, r := range fn.Type.Results.List {
						switch rt := r.Type.(type) {
						case *syntax.ErrorRow:
						case *syntax.TupleExpr:
							for _, e := range rt.Elts {
								types = append(types, at(e))
							}
						default:
							types = append(types, at(rt))
						}
					}
				}
				if want, ok := goSigs[fn.Name.Name]; !ok || strings.Join(types, " ") != strings.Join(want, " ") {
					t.Errorf("%s: Ergo %v, Go %v", fn.Name.Name, types, want)
				}
			}
			if count == 0 {
				t.Fatal("no extern")
			}
		})
	}
}

// ergoNamePool is the domain of C parameter names: the Ergo keywords that
// C allows, the predeclared names, their "_arg" and "Arg" forms and the
// numbered forms the allocator gives, trailing underscores, the import's
// name, names that normalize to one, and ordinary names.
func ergoNamePool(t *testing.T, predeclared map[string]bool) []string {
	t.Helper()
	words := append(syntax.Keywords(), syntax.Reserved()...)
	for n := range predeclared {
		if n == strings.ToLower(n) {
			words = append(words, n)
		}
	}
	words = append(words, "fixture", "ctx", "value", "count", "nil", "make", "any")
	set := map[string]bool{}
	for _, w := range words {
		for _, n := range []string{w, w + "_arg", w + "_arg_2", w + "_", w + "2", w + "_arg2"} {
			if !cKeywords[n] {
				set[n] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestErgoParamProperty (M1): for fixture variants with typed functions
// whose parameters (named, and unnamed through typedefs) take names of the
// pool in random C spellings, Bind refuses with a name error, or every
// extern's Ergo parameters are as many as its Go parameters, distinct, not
// reserved, off the import's name, and the emitted text has no conflict.
func TestErgoParamProperty(t *testing.T) {
	reserved := checkerRefuses(t, checkerDomain(t))
	pool := ergoNamePool(t, reserved)
	stats := map[string]int{}
	n := max(8, *propCases/10)
	for i := 0; i < n; i++ {
		seed := *propSeed + uint64(i)
		r := rand.New(rand.NewPCG(seed, 0xe790))
		name := func() string { return respell(r, pool[r.IntN(len(pool))]) }
		var extra strings.Builder
		var tds []string
		for k := 0; k < 2; k++ {
			td := "fx_" + strings.TrimLeft(name(), "_")
			if !contains(tds, td) {
				tds = append(tds, td)
				fmt.Fprintf(&extra, "typedef uint32_t %s;\n", td)
			}
		}
		shapes := []func(n string) string{
			func(n string) string { return "uint32_t " + n },
			func(n string) string { return "double " + n },
			func(n string) string { return "const uint8_t *" + n + ", uint32_t " + n + "_count" },
			func(n string) string { return "fx_point " + n },
			func(n string) string { return "fx_context *" + n },
			func(n string) string { return "const fx_node *" + n },
			func(n string) string { return tds[r.IntN(len(tds))] },
		}
		for k, nf := 0, 1+r.IntN(4); k < nf; k++ {
			used := map[string]bool{}
			var ps []string
			for p, np := 0, 2+r.IntN(5); p < np; p++ {
				pn := name()
				if used[pn] || used[pn+"_count"] {
					continue
				}
				used[pn], used[pn+"_count"] = true, true
				ps = append(ps, shapes[r.IntN(len(shapes))](pn))
			}
			fmt.Fprintf(&extra, "uint32_t fx_np%d(%s);\n", k, strings.Join(ps, ", "))
		}
		b, src, err := bindErgo(t, extra.String())
		if err != nil {
			if !strings.Contains(err.Error(), "Go name") && !strings.Contains(err.Error(), "Ergo name") {
				t.Fatalf("seed %d: Bind refuses for a reason other than a name: %v\n%s", seed, err, extra.String())
			}
			stats["refused"]++
			continue
		}
		stats["bound"]++
		params := ergoParams(t, src)
		importName := b.Contract.Go.ImportPath[strings.LastIndexByte(b.Contract.Go.ImportPath, '/')+1:]
		for _, f := range b.Funcs {
			if f.ErgoUnbound() != "" {
				continue
			}
			ps, ok := params[f.GoName]
			if !ok || len(ps) != len(f.Sig.Params) {
				t.Fatalf("seed %d: the extern %s has parameters %v for %d Go parameters", seed, f.GoName, ps, len(f.Sig.Params))
			}
			for j, e := range ps {
				cp := f.C.Params[f.Sig.Params[j].C]
				want := lowerCamel(cp.Name)
				if want == "" {
					ty := cp.Type
					for ty.Kind == TypePointer {
						ty = *ty.Elem
					}
					want = lowerCamel(strings.TrimPrefix(ty.Name, "fx_"))
				}
				if reserved[e] || syntax.IsKeyword(e) || e == "_" {
					t.Errorf("seed %d: %s: parameter %s is reserved in Ergo\n%s", seed, f.C.Name, e, extra.String())
				}
				if e == importName {
					t.Errorf("seed %d: %s: parameter %s is the import's name", seed, f.C.Name, e)
				}
				switch {
				case e == want:
				case strings.HasPrefix(e, want+"Arg"):
					stats["escaped"]++
				default:
					stats["numbered"]++
				}
			}
		}
		for _, x := range ergoConflicts(t, src) {
			t.Errorf("seed %d: conflict: %s\n%s", seed, x, extra.String())
		}
	}
	t.Logf("%d cases: %v", n, stats)
	for _, k := range []string{"bound", "escaped", "numbered"} {
		if stats[k] == 0 {
			t.Errorf("the generator made no case of %q", k)
		}
	}
}

// packageNamePool is the domain of package-level Ergo names: the generated
// unions and variants, the fixture's variants, records and externs, the
// predeclared capitalized names (from the checker), and fresh names.
func packageNamePool(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{"Status": true, "CallError": true, "NotLoaded": true, "Destroyed": true, "TooLong": true,
		"InvalidArgument": true, "WrongThread": true, "Node": true, "Point": true, "Box": true, "ContextOptions": true,
		"AbiVersion": true, "PointLength": true, "NodeCount": true, "Fixture": true}
	for n := range checkerRefuses(t, checkerDomain(t)) {
		if n[0] >= 'A' && n[0] <= 'Z' {
			set[n] = true
		}
	}
	for k := 0; k < 6; k++ {
		set["Fresh"+strconv.Itoa(k)] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestErgoNamespaceProperty (M2): for fixture variants that declare, with
// names of the pool, typed functions, records that a typed function takes
// by value, and status codes, Bind refuses with a name error, or the
// emitted text has no conflict. Every kind meets every other kind and the
// predeclared names in the refusals.
func TestErgoNamespaceProperty(t *testing.T) {
	pool := packageNamePool(t)
	base := string(readInput(t, "testdata/fixture/fixture.h"))
	stats := map[string]int{}
	n := max(8, *propCases/10)
	for i := 0; i < n; i++ {
		seed := *propSeed + uint64(i)
		r := rand.New(rand.NewPCG(seed, 0x5a7e))
		var extra strings.Builder
		// The C names this case declares: functions and typedefs share
		// one C namespace, macros have their own.
		used := map[string]bool{}
		declared := func(c string) bool {
			return used[c] || regexp.MustCompile(`\b`+c+`\b`).MatchString(base)
		}
		for k, nd := 0, 1+r.IntN(4); k < nd; k++ {
			c := snake(pool[r.IntN(len(pool))])
			fn, macro := "fx_"+c, "FX_"+strings.ToUpper(c)
			switch kind := r.IntN(3); {
			case kind < 2 && !declared(fn):
				used[fn] = true
				if kind == 0 {
					fmt.Fprintf(&extra, "uint32_t %s(void);\n", fn)
				} else {
					fmt.Fprintf(&extra, "typedef struct %s { uint32_t v; } %s;\nuint32_t fx_use%d_%d(%s value);\n", fn, fn, i, k, fn)
				}
			case kind == 2 && !declared(macro):
				used[macro] = true
				fmt.Fprintf(&extra, "#define %s (-INT32_C(%d))\n", macro, 100+k)
			}
		}
		_, src, err := bindErgo(t, extra.String())
		if err != nil {
			msg := err.Error()
			if !strings.Contains(msg, "Go name") && !strings.Contains(msg, "Ergo name") {
				t.Fatalf("seed %d: Bind refuses for a reason other than a name: %v\n%s", seed, err, extra.String())
			}
			for _, line := range strings.Split(msg, "\n") {
				if !strings.Contains(line, " have the Ergo name ") {
					continue
				}
				stats["refused "+ergoKinds(line)]++
			}
			stats["refused"]++
			continue
		}
		stats["bound"]++
		for _, x := range ergoConflicts(t, src) {
			t.Errorf("seed %d: conflict: %s\n%s", seed, x, extra.String())
		}
	}
	t.Logf("%d cases: %v", n, stats)
	for _, k := range []string{"bound", "refused",
		"refused function/generated", "refused function/variant", "refused function/predeclared",
		"refused predeclared/record", "refused predeclared/variant", "refused record/variant", "refused generated/variant"} {
		if stats[k] == 0 {
			t.Errorf("the generator made no case of %q", k)
		}
	}
}

// ergoKinds names the two kinds of a refusal line "<a> and <b> have the
// Ergo name N", sorted: function, record, variant, generated, predeclared.
func ergoKinds(line string) string {
	kind := func(s string) string {
		switch {
		case strings.HasPrefix(s, "function "):
			return "function"
		case strings.HasPrefix(s, "struct "):
			return "record"
		case strings.HasPrefix(s, "status code "):
			return "variant"
		case strings.HasPrefix(s, "an Ergo keyword"):
			return "predeclared"
		case strings.HasPrefix(s, "the import of "):
			return "import"
		}
		return "generated"
	}
	line = strings.TrimSpace(line)
	line = line[:strings.Index(line, " have the Ergo name ")]
	// The second source is a C identity ("function x", "struct x",
	// "status code X"): it holds no " and ".
	j := strings.LastIndex(line, " and ")
	ks := []string{kind(line[:j]), kind(line[j+len(" and "):])}
	sort.Strings(ks)
	return ks[0] + "/" + ks[1]
}

// ergoDeclared is the oracle's view of an Ergo text: the name its Go
// import binds (the last element of the path, as the checker binds it),
// every package-level name it declares (types, the variants of a union,
// externs) and every parameter name of an extern. It reads the parsed
// text only.
func ergoDeclared(t *testing.T, src string) (imp string, decls, params map[string]bool) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("the Ergo text does not parse: %v\n%s", err, src)
	}
	decls, params = map[string]bool{}, map[string]bool{}
	imps := 0
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.ImportDecl:
			for _, s := range d.Specs {
				imps++
				if s.Kind != syntax.ImportGo || s.Name != nil {
					t.Fatalf("the Ergo text has an import that is not `import go <path>`:\n%s", src)
				}
				p := s.Path.Value
				imp = p[strings.LastIndex(p, "/")+1:]
			}
		case *syntax.TypeDecl:
			decls[d.Name.Name] = true
			if u, ok := d.Spec.(*syntax.InterfaceType); ok {
				for _, v := range u.Union {
					if v, ok := v.(*syntax.VariantSpec); ok {
						decls[v.Name.Name] = true
					}
				}
			}
		case *syntax.FuncDecl:
			decls[d.Name.Name] = true
			if d.Type.Params != nil {
				for _, p := range d.Type.Params.List {
					for _, n := range p.Names {
						params[n.Name] = true
					}
				}
			}
		}
	}
	if imps != 1 {
		t.Fatalf("the Ergo text has %d imports, want 1:\n%s", imps, src)
	}
	return imp, decls, params
}

// importClashes is the oracle of the import's name: what makes the name
// that the import of base binds collide in src. reserved is the set of
// names the checker refuses (checkerRefuses). It returns nothing for an
// import that is an identifier Ergo does not reserve, that no
// package-level name and no parameter takes, in a text with no other
// conflict (ergoConflicts).
func importClashes(t *testing.T, src, base string, reserved map[string]bool) []string {
	t.Helper()
	imp, decls, params := ergoDeclared(t, src)
	var out []string
	if imp != base {
		out = append(out, fmt.Sprintf("the import binds %q, want %q", imp, base))
	}
	if !identRE.MatchString(imp) || syntax.IsKeyword(imp) || imp == "_" || reserved[imp] {
		out = append(out, fmt.Sprintf("the import binds %q, which Ergo reserves or which is not an identifier", imp))
	}
	if decls[imp] {
		out = append(out, fmt.Sprintf("the import's name %s is also declared in the package", imp))
	}
	if params[imp] {
		out = append(out, fmt.Sprintf("the import's name %s is also a parameter", imp))
	}
	return append(out, ergoConflicts(t, src)...)
}

// importNamePool is the domain of import names (the last element of
// go.import_path): the generated unions and variants, every name the
// fixture's Ergo text declares (unions, variants, records, externs) and
// its parameters, the Ergo keywords and reserved words, the names the
// checker predeclares, ordinary names, and names that are not identifiers.
func importNamePool(t *testing.T, reserved map[string]bool) []string {
	t.Helper()
	_, src, err := bindErgo(t, "")
	if err != nil {
		t.Fatal(err)
	}
	_, decls, params := ergoDeclared(t, src)
	set := map[string]bool{}
	for _, m := range []map[string]bool{decls, params, reserved} {
		for n := range m {
			set[n] = true
		}
	}
	for _, n := range append(syntax.Keywords(), syntax.Reserved()...) {
		set[n] = true
	}
	for _, n := range []string{"Status", "CallError", "NotLoaded", "Destroyed", "TooLong",
		"fixture", "Fixture", "mylib", "lib2", "purego", "Fresh0", "contextArg2", "box2",
		"_", "", "my-lib", "9lives", "a.b", "fx lib"} {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestErgoImportProperty (wave 1d r2 M1): the import's name enters the Ergo
// package scope only through claim. Over every name of the pool, with the
// fixture header, Bind refuses the import exactly when the oracle sees a
// clash, with the import as one source; else the text has no clash. Then,
// for fixture variants that declare pool names as functions, records,
// status codes and parameters and import one of them (or a pool name),
// Bind refuses with a name error, or the text has no clash.
func TestErgoImportProperty(t *testing.T) {
	reserved := checkerRefuses(t, checkerDomain(t))
	pool := importNamePool(t, reserved)
	_, src, err := bindErgo(t, "")
	if err != nil {
		t.Fatal(err)
	}
	_, decls, params := ergoDeclared(t, src)
	stats := map[string]int{}
	for _, base := range pool {
		clash := !identRE.MatchString(base) || syntax.IsKeyword(base) || base == "_" || reserved[base] || decls[base]
		_, src, err := bindErgoImport(t, "", base)
		if err != nil {
			msg := err.Error()
			if !clash {
				t.Errorf("import %q: Bind refuses a name that clashes with nothing: %v", base, err)
			}
			if !strings.Contains(msg, "the import of ergo.local/bind/"+base) {
				t.Errorf("import %q: the refusal does not name the import: %v", base, err)
			}
			for _, line := range strings.Split(msg, "\n") {
				switch {
				case strings.Contains(line, " have the Ergo name "):
					stats["refused "+ergoKinds(line)]++
				case strings.Contains(line, "is not an identifier"):
					stats["refused not an identifier"]++
				}
			}
			stats["refused"]++
			continue
		}
		if clash {
			t.Errorf("import %q: Bind accepts an import that clashes", base)
		}
		for _, x := range importClashes(t, src, base, reserved) {
			t.Errorf("import %q: %s", base, x)
		}
		stats["bound"]++
		if params[base] {
			stats["bound, a parameter renamed"]++
		}
	}
	t.Logf("%d import names: %v", len(pool), stats)
	for _, k := range []string{"bound", "bound, a parameter renamed", "refused not an identifier",
		"refused generated/import", "refused import/predeclared", "refused import/variant",
		"refused import/record", "refused function/import"} {
		if stats[k] == 0 {
			t.Errorf("the pool made no case of %q", k)
		}
	}

	names := packageNamePool(t)
	lower := ergoNamePool(t, reserved)
	base := string(readInput(t, "testdata/fixture/fixture.h"))
	vstats := map[string]int{}
	n := max(8, *propCases/10)
	for i := 0; i < n; i++ {
		seed := *propSeed + uint64(i)
		r := rand.New(rand.NewPCG(seed, 0x1a90))
		var extra strings.Builder
		used := map[string]bool{}
		declared := func(c string) bool {
			return used[c] || regexp.MustCompile(`\b`+c+`\b`).MatchString(base)
		}
		var picks []string
		for k, nd := 0, r.IntN(3); k < nd; k++ {
			e := names[r.IntN(len(names))]
			c := snake(e)
			fn, macro := "fx_"+c, "FX_"+strings.ToUpper(c)
			switch kind := r.IntN(3); {
			case kind < 2 && !declared(fn):
				used[fn] = true
				picks = append(picks, e)
				if kind == 0 {
					fmt.Fprintf(&extra, "uint32_t %s(void);\n", fn)
				} else {
					fmt.Fprintf(&extra, "typedef struct %s { uint32_t v; } %s;\nuint32_t fx_use%d_%d(%s value);\n", fn, fn, i, k, fn)
				}
			case kind == 2 && !declared(macro):
				used[macro] = true
				picks = append(picks, e)
				fmt.Fprintf(&extra, "#define %s (-INT32_C(%d))\n", macro, 100+k)
			}
		}
		var ps []string
		for k, np := 0, 1+r.IntN(3); k < np; k++ {
			p := lower[r.IntN(len(lower))]
			if !contains(ps, "uint32_t "+p) {
				ps = append(ps, "uint32_t "+p)
				picks = append(picks, lowerCamel(p))
			}
		}
		fmt.Fprintf(&extra, "uint32_t fx_imp%d(%s);\n", i, strings.Join(ps, ", "))
		imp := pool[r.IntN(len(pool))]
		if r.IntN(3) > 0 {
			imp = picks[r.IntN(len(picks))]
		}
		_, src, err := bindErgoImport(t, extra.String(), imp)
		if err != nil {
			msg := err.Error()
			if !strings.Contains(msg, "Go name") && !strings.Contains(msg, "Ergo name") {
				t.Fatalf("seed %d: import %q: Bind refuses for a reason other than a name: %v\n%s", seed, imp, err, extra.String())
			}
			vstats["refused"]++
			if strings.Contains(msg, "the import of ") {
				vstats["refused the import"]++
			}
			continue
		}
		vstats["bound"]++
		for _, x := range importClashes(t, src, imp, reserved) {
			t.Errorf("seed %d: import %q: %s\n%s", seed, imp, x, extra.String())
		}
	}
	t.Logf("%d variants: %v", n, vstats)
	for _, k := range []string{"bound", "refused the import"} {
		if vstats[k] == 0 {
			t.Errorf("the generator made no case of %q", k)
		}
	}
}

// TestErgoImportRegressions: the wave 1d r2 verifier's import probe
// (TestReviewImportNames). Each import is refused at Bind with the import
// as one source, or binds to a text with no clash; ErgoText never sees a
// colliding import.
func TestErgoImportRegressions(t *testing.T) {
	reserved := checkerRefuses(t, checkerDomain(t))
	cases := []struct{ base, want string }{
		{"CallError", "the import of ergo.local/bind/CallError and the CallError union have the Ergo name CallError"},
		{"Status", "the import of ergo.local/bind/Status and the Status union have the Ergo name Status"},
		{"NotLoaded", "the import of ergo.local/bind/NotLoaded and a variant of the CallError union have the Ergo name NotLoaded"},
		{"Option", "an Ergo keyword or predeclared name and the import of ergo.local/bind/Option have the Ergo name Option"},
		{"len", "an Ergo keyword or predeclared name and the import of ergo.local/bind/len have the Ergo name len"},
		// The verifier's product failure: a keyword import made ErgoText
		// fail to parse `*context.Context`.
		{"context", "an Ergo keyword or predeclared name and the import of ergo.local/bind/context have the Ergo name context"},
		{"type", "an Ergo keyword or predeclared name and the import of ergo.local/bind/type have the Ergo name type"},
		{"InvalidArgument", "the import of ergo.local/bind/InvalidArgument and status code FX_INVALID_ARGUMENT have the Ergo name InvalidArgument"},
		{"Point", "the import of ergo.local/bind/Point and struct fx_point have the Ergo name Point"},
		{"AbiVersion", "the import of ergo.local/bind/AbiVersion and function fx_abi_version have the Ergo name AbiVersion"},
		{"my-lib", `the import of ergo.local/bind/my-lib: Ergo name "my-lib" is not an identifier`},
		{"fixture", ""},
		{"node", ""},
	}
	for _, c := range cases {
		t.Run(c.base, func(t *testing.T) {
			_, src, err := bindErgoImport(t, "", c.base)
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("Bind error %v, want one with %q\n%s", err, c.want, src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			for _, x := range importClashes(t, src, c.base, reserved) {
				t.Errorf("clash: %s", x)
			}
		})
	}
}

package bindc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	goparser "go/parser"
	gotoken "go/token"
	"go/types"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// fixtureVariant is the fixture header with extra declarations before its
// closing block and every old name of rename replaced by its new one, and
// the fixture contract with the same renames (autofill fields) and the new
// header's sha256.
func fixtureVariant(t *testing.T, extra string, rename map[string]string) (header, contract []byte) {
	t.Helper()
	const tail = "\n#ifdef __cplusplus\n}\n#endif\n"
	h := string(readInput(t, "testdata/fixture/fixture.h"))
	i := strings.LastIndex(h, tail)
	if i < 0 {
		t.Fatal("the fixture header has no closing extern \"C\" block")
	}
	h = h[:i] + "\n" + extra + "\n" + h[i:]
	var c map[string]any
	if err := json.Unmarshal(readInput(t, "testdata/fixture/fixture.bind.json"), &c); err != nil {
		t.Fatal(err)
	}
	olds := make([]string, 0, len(rename))
	for o := range rename {
		olds = append(olds, o)
	}
	sort.Strings(olds)
	for _, o := range olds {
		h = strings.ReplaceAll(h, o, rename[o])
		for _, a := range c["autofill"].([]any) {
			if a := a.(map[string]any); a["field"] == o {
				a["field"] = rename[o]
			}
		}
	}
	sum := sha256.Sum256([]byte(h))
	c["header_sha256"] = hex.EncodeToString(sum[:])
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(h), data
}

// bindVariant parses and binds a fixture variant.
func bindVariant(t *testing.T, header, contract []byte) (*Binding, error) {
	t.Helper()
	m, err := Parse("fixture.h", header)
	if err != nil {
		t.Fatalf("the variant does not parse: %v\n%s", err, header)
	}
	return Bind(m, header, contract)
}

// TestGoNameRegressions: the cases of the wave verifiers. A parameter named
// ready, status_error or slice_data (wave 1c M3), and an autofill field
// named fill (wave 1d, on base: "M4"), bind to a Go package that
// type-checks on amd64 and arm64; the parameter or field gets "_".
func TestGoNameRegressions(t *testing.T) {
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	cases := []struct {
		name, extra string
		rename      map[string]string
		want        string // a line of the emitted file
	}{
		{"ready", "fx_status fx_ready_arg(uint32_t ready);", nil, "func ReadyArg(ready_ uint32) error {"},
		{"status_error", "fx_status fx_error_arg(uint32_t status_error);", nil, "func ErrorArg(statusError_ uint32) error {"},
		{"slice_data", "fx_status fx_bytes_arg(const uint8_t *bytes, uint32_t byte_count, uint32_t slice_data);", nil,
			"func BytesArg(bytes []byte, sliceData_ uint32) error {"},
		{"field_method", "", map[string]string{"struct_size": "fill"}, "\tfill_ "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, ct := fixtureVariant(t, c.extra, c.rename)
			b, err := bindVariant(t, h, ct)
			if err != nil {
				t.Fatalf("Bind: %v", err)
			}
			src := checkGoNames(t, std, b)
			if !bytes.Contains(src, []byte(c.want)) {
				t.Errorf("the emitted package has no %q", c.want)
			}
		})
	}
}

// checkGoNames type-checks the Go package of b on amd64 and arm64, checks
// its names against the inventory (checkGoInventory), and checks that
// every typed function's signature in the emitted file is the one
// report.txt gives. It returns the emitted source.
func checkGoNames(t *testing.T, std types.Importer, b *Binding) []byte {
	t.Helper()
	name, src, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, arch, map[string][]byte{name: src}); err != nil {
			t.Fatalf("the Go package does not type-check on %s:\n%v", arch, err)
		}
	}
	checkGoInventory(t, b, src)
	report := string(Report(b))
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]string{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			fd.Body = nil
			var w bytes.Buffer
			if err := format.Node(&w, fset, fd); err != nil {
				t.Fatal(err)
			}
			decls[fd.Name.Name] = w.String()
		}
	}
	for _, f := range b.Funcs {
		if f.Sig == nil {
			continue
		}
		line := "  " + f.C.Name + ": " + f.Sig.String(f.GoName) + "\n"
		if !strings.Contains(report, line) {
			t.Errorf("report.txt has no line %q", line)
		}
		if decls[f.GoName] != f.Sig.String(f.GoName) {
			t.Errorf("%s: the emitted signature is %q, report.txt has %q", f.C.Name, decls[f.GoName], f.Sig.String(f.GoName))
		}
		seen := map[string]bool{}
		for _, p := range f.Sig.Params {
			if seen[p.Name] || isGoWord(p.Name) || isGoLocal(p.Name) || isGoPackageHelper(p.Name) || b.goNameOwner(p.Name) != "" {
				t.Errorf("%s: parameter %s is taken or reserved", f.C.Name, p.Name)
			}
			seen[p.Name] = true
		}
	}
	return src
}

// checkGoInventory checks that the emitted package src of b declares no
// unexported package-level name, import, method or typed-body local
// outside the inventory of names.go, so every scope's reservations cover
// every generated name.
func checkGoInventory(t *testing.T, b *Binding, src []byte) {
	t.Helper()
	file, err := goparser.ParseFile(gotoken.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgName := func(n string) {
		if n == "_" || unicode.IsUpper(rune(n[0])) {
			return // exported names are claimed in Bind (checkNames)
		}
		if !isGoPackageHelper(n) && !strings.HasPrefix(b.goNameOwner(n), "the raw function of ") {
			t.Errorf("the package declares %s, which is not in the inventory of names.go", n)
		}
	}
	typed := map[string]bool{}
	for _, f := range b.Funcs {
		if f.Sig != nil {
			typed[f.GoName] = true
		}
	}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.ImportSpec:
					p := strings.Trim(s.Path.Value, `"`)
					pkgName(p[strings.LastIndexByte(p, '/')+1:])
				case *ast.ValueSpec:
					for _, n := range s.Names {
						pkgName(n.Name)
					}
				case *ast.TypeSpec:
					pkgName(s.Name.Name)
				}
			}
		case *ast.FuncDecl:
			if d.Recv != nil {
				if n := d.Name.Name; !unicode.IsUpper(rune(n[0])) && !goMethods[n] {
					t.Errorf("the package declares the method %s, which is not in goMethods", n)
				}
				continue
			}
			pkgName(d.Name.Name)
			if !typed[d.Name.Name] {
				continue
			}
			local := func(e ast.Expr) {
				if id, ok := e.(*ast.Ident); ok && id.Name != "_" && !isGoLocal(id.Name) {
					t.Errorf("%s declares the local %s, which is not in the inventory of names.go", d.Name.Name, id.Name)
				}
			}
			ast.Inspect(d.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.AssignStmt:
					if n.Tok == gotoken.DEFINE {
						for _, e := range n.Lhs {
							local(e)
						}
					}
				case *ast.RangeStmt:
					if n.Tok == gotoken.DEFINE {
						local(n.Key)
						if n.Value != nil {
							local(n.Value)
						}
					}
				case *ast.ValueSpec:
					for _, id := range n.Names {
						local(id)
					}
				case *ast.FuncLit:
					t.Errorf("%s has a function literal; the inventory does not cover its names", d.Name.Name)
				}
				return true
			})
		}
	}
}

// TestGoNameInventory: the two golden Go packages declare no generated
// name outside the inventory of names.go.
func TestGoNameInventory(t *testing.T) {
	for _, h := range headers {
		m := parseHeader(t, h.dir, h.header)
		b := bindHeader(t, h.dir, h.header, m)
		for _, src := range goldenGoFiles(t, h.dir) {
			checkGoInventory(t, b, src)
		}
	}
}

// cKeywords are C11's keywords and the macros of the fixture's standard
// headers, which a C parameter or field may not be named.
var cKeywords = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`auto break case char const continue default do double else enum extern float for goto if
		inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void
		volatile while bool true false NULL offsetof size_t ptrdiff_t wchar_t max_align_t`) {
		cKeywords[k] = true
	}
}

// snake is the canonical C spelling of a lowerCamel Go name: "statusError"
// -> "status_error"; lowerCamel(snake(n)) == n for every n without "_".
func snake(n string) string {
	var b strings.Builder
	for i, r := range n {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// respell is a random C spelling of the same Go name: each part in lower,
// upper or title case, doubled underscores, a leading or trailing one.
func respell(r *rand.Rand, c string) string {
	parts := strings.Split(c, "_")
	for i, p := range parts {
		switch r.IntN(3) {
		case 1:
			parts[i] = strings.ToUpper(p)
		case 2:
			if p != "" {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
	}
	sep := "_"
	if r.IntN(3) == 0 {
		sep = "__"
	}
	s := strings.Join(parts, sep)
	for len(s) > 1 && s[0] == '_' && (s[1] == '_' || unicode.IsUpper(rune(s[1]))) {
		s = s[1:]
	}
	if r.IntN(4) == 0 && s[0] >= 'a' && s[0] <= 'z' {
		// C reserves _ followed by a capital or a second _.
		s = "_" + s
	}
	if r.IntN(4) == 0 {
		s += "_"
	}
	return s
}

// adversarialNames are C names that normalize to every name of the
// inventory of names.go that a C name can reach (lowerCamel never gives
// "_", so _r, _o<i> and _i<depth> are out of reach), to Go's keywords and
// predeclared names, to the call form and numbered families and their
// stems, plus "_arg" forms, trailing underscores, normalization collisions
// and ordinary names.
func adversarialNames(t *testing.T) []string {
	t.Helper()
	var goNames []string
	for _, m := range []map[string]bool{goPackageHelpers, goLocals, goMethods, goKeywords, goPredeclared} {
		for n := range m {
			goNames = append(goNames, n)
		}
	}
	goNames = append(goNames, "abiW", "abiW1", "abiW2", "abiW12", "abiF32x2", "abiF64x3", "abiF32x", "o1", "i0", "fill2")
	var out []string
	unreachable := 0
	for _, n := range goNames {
		if strings.Contains(n, "_") {
			unreachable++
			continue
		}
		c := snake(n)
		if lowerCamel(c) != n {
			t.Fatalf("snake(%q) = %q normalizes to %q", n, c, lowerCamel(c))
		}
		if !cKeywords[c] {
			out = append(out, c)
		}
	}
	if unreachable != 1 { // _r
		t.Fatalf("%d inventory names contain \"_\"; the list of unreachable names is out of date", unreachable)
	}
	out = append(out, "ready_arg", "type_arg", "context_arg", "ready_", "x_", "_x", "x", "X", "value", "count", "a_b", "A_B", "aB", "a")
	sort.Strings(out)
	return out
}

// TestGoNameProperty: for fixture variants with generated functions whose
// parameters (named and unnamed), struct fields and autofill fields take
// adversarial names, Bind refuses with a name error, or the Go package
// type-checks on amd64 and arm64, its names stay in the inventory, and
// each typed signature equals its report.txt line with every parameter
// off every reserved and taken name.
func TestGoNameProperty(t *testing.T) {
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	pool := adversarialNames(t)
	stats := map[string]int{}
	renames := []string{"fill", "fill_", "type", "ready", "err", "range", "status_error", "slice_data", "_r", "abi_w2"}
	n := max(4, *propCases/50)
	for i := 0; i < n; i++ {
		seed := *propSeed + uint64(i)
		r := rand.New(rand.NewPCG(seed, 0x4a3e))
		var extra strings.Builder
		name := func() string { return respell(r, pool[r.IntN(len(pool))]) }
		// A struct whose fields take adversarial names.
		put := func(format string, args ...any) { fmt.Fprintf(&extra, format, args...) }
		if r.IntN(2) == 0 {
			put("typedef struct fx_names {\n")
			used := map[string]bool{}
			for k, nf := 0, 1+r.IntN(4); k < nf; k++ {
				f := name()
				if used[f] {
					continue
				}
				used[f] = true
				put("    uint32_t %s;\n", f)
			}
			put("} fx_names;\n")
		}
		// Typedefs for unnamed parameters: their Go names come from the type.
		var tds []string
		for k := 0; k < 2; k++ {
			td := "fx_" + strings.TrimLeft(name(), "_")
			if !contains(tds, td) {
				tds = append(tds, td)
				put("typedef uint32_t %s;\n", td)
			}
		}
		shapes := []func(n string) string{
			func(n string) string { return "uint32_t " + n },
			func(n string) string { return "double " + n },
			func(n string) string { return "const uint8_t *" + n + ", uint32_t " + n + "_count" },
			func(n string) string { return "fx_point " + n },
			func(n string) string { return "const fx_context_options *" + n },
			func(n string) string { return "fx_context_options " + n },
			func(n string) string { return "fx_context *" + n },
			func(n string) string { return "const fx_node *" + n },
			func(n string) string { return tds[r.IntN(len(tds))] },
		}
		results := []string{"fx_status", "void", "uint32_t", "fx_point", "fx_node", "fx_context_options"}
		for k, nf := 0, 2+r.IntN(6); k < nf; k++ {
			used := map[string]bool{}
			var ps []string
			for p, np := 0, 1+r.IntN(5); p < np; p++ {
				pn := name()
				if used[pn] || used[pn+"_count"] {
					continue
				}
				used[pn], used[pn+"_count"] = true, true
				ps = append(ps, shapes[r.IntN(len(shapes))](pn))
			}
			put("%s fx_np%d(%s);\n", results[r.IntN(len(results))], k, strings.Join(ps, ", "))
		}
		rename := map[string]string{}
		if r.IntN(2) == 0 {
			rename["struct_size"] = renames[r.IntN(len(renames))]
		}
		h, ct := fixtureVariant(t, extra.String(), rename)
		b, err := bindVariant(t, h, ct)
		if err != nil {
			if !strings.Contains(err.Error(), "Go name") {
				t.Fatalf("seed %d: Bind refuses for a reason other than a name: %v\n%s", seed, err, extra.String())
			}
			stats["refused"]++
			continue
		}
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			checkGoNames(t, std, b)
		})
		stats["typechecked"]++
		for _, f := range b.Funcs {
			if f.Sig == nil {
				continue
			}
			for _, p := range f.Sig.Params {
				if strings.HasSuffix(p.Name, "_") || strings.Contains(p.Name, "_") {
					stats["renamed parameter"]++
				}
			}
		}
		if len(rename) > 0 {
			stats["renamed autofill field"]++
		}
	}
	t.Logf("%d cases: %v", n, stats)
	for _, k := range []string{"refused", "typechecked", "renamed parameter", "renamed autofill field"} {
		if stats[k] == 0 {
			t.Errorf("the generator made no case of %q", k)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

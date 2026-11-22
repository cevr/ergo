package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerTypesLawsRun runs the laws of compiler/check/types (capstone
// S3t) through `ergo test`: KindSolveWorkIsNearLinear, the counted guard of
// the kinds solve. Mutants of compiler/check/types/testdata/mutants.json it
// alone catches: find-without-path-halving, fails-as-type, linkhead-off.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerTypesLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/types")
}

// TestCompilerTypesAgreeWithChecker is S3t's corpus agreement: over every
// package folder of examples/ and std the Go checker checks, the bracket
// parameter kinds compiler/check/types solves (typedump -kinds) are the Go
// checker's TypeParam kinds, and the record field types it resolves
// (cmd/typedump) print as the Go checker's Type.String. The types of other
// packages come from the Go side as a table (EXT and FOREIGN files), until
// S3c's frozen interfaces. A folder the Go checker refuses is skipped; the
// test fails when it skips every folder. It kills ext-kinds-ignored and
// prelude-qualified, which the property cannot reach (both need a std or
// prelude package as the one under test).
//
// It builds Go because typedump is the Ergo program under
// test: compiler/check/types is Ergo source, and only its lowered, built
// form runs over a corpus.
func TestCompilerTypesAgreeWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	td := typesDump(t, ctx)
	tmp := t.TempDir()
	checked := 0
	for _, dir := range corpusDirs(t) {
		pkg := typesPackage(dir)
		if pkg == nil {
			continue
		}
		checked++
		files := ergoFiles(t, dir)
		kinds, ext := goKinds(pkg)
		fields, foreign := goTypes(pkg)
		e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		if d := setDiff(kinds, runDump(t, ctx, td, append([]string{"-std", stdDir(), "-kinds", e, f}, files...)...)); d != "" {
			t.Errorf("%s: kinds differ (go -, ergo +):\n%s", rel(dir), d)
		}
		if d := setDiff(fields, dropCharged(runDump(t, ctx, td, append([]string{"-std", stdDir(), e, f}, files...)...))); d != "" {
			t.Errorf("%s: types differ (go -, ergo +):\n%s", rel(dir), d)
		}
	}
	if checked == 0 {
		t.Fatal("the Go checker checked no corpus folder")
	}
}

// TestCompilerTypesProperty is S3t's seeded property, the Go checker the
// reference model. Each kinds case is a package of generic records, unions
// and operation types whose parameters stand in type, error-row and
// capability-row positions and link through each other's brackets and
// through receiver functions; each types case is a package of records
// whose field types use every form resolve handles. typedump's two dumps
// must print what the Go checker gives, conflicts included. The run fails
// unless every kind and every form is reached. It kills the kinds and
// resolve mutants the corpus does not reach.
//
// It builds Go because typedump is the program under test.
func TestCompilerTypesProperty(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	td := typesDump(t, ctx)
	base := uint32(1)
	if s := os.Getenv(seedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, s, err)
		}
		base = uint32(n)
	}
	replay := func(seed uint32) string {
		return "replay: " + seedVariable + "=" + strconv.FormatUint(uint64(seed), 10) + " go test -count=1 -run '^TestCompilerTypesProperty$' ./internal/selfhostcheck (the first case is the failing one)"
	}
	tmp := t.TempDir()
	file := filepath.Join(tmp, "p.ergo")
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	failed := 0
	report := func(seed uint32, src string, want, got []string) {
		failed++
		if failed <= 3 {
			t.Errorf("seed %d: the dump differs from the Go checker (go -, ergo +):\n%s--- source\n%s\n%s", seed, setDiff(want, got), src, replay(seed))
		}
	}
	reachKinds := map[string]int{"type": 0, "errors": 0, "uses": 0, "conflict": 0, "cycle": 0}
	for seed := base; seed < base+300; seed++ {
		src := genKinds(seed)
		pkg, ds := check.CheckSource(src)
		if pkg == nil {
			continue
		}
		want, ext := goKinds(pkg)
		want = append(want, cycleLines(ds)...)
		for _, l := range want {
			for _, w := range strings.FieldsFunc(l, func(r rune) bool { return r == ' ' || r == ',' }) {
				if _, ok := reachKinds[w]; ok {
					reachKinds[w]++
				}
			}
		}
		_, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		writeLines(t, file, []string{src})
		if got := runDump(t, ctx, td, "-std", stdDir(), "-kinds", e, f, file); setDiff(want, got) != "" {
			report(seed, src, want, got)
		}
	}
	forms := typeForms()
	reach := map[string]int{}
	ran := 0
	for seed := base; seed < base+200; seed++ {
		src := genTypes(seed)
		pkg, ds := check.CheckSource(src)
		if pkg == nil || len(ds) > 0 {
			continue // an invalid type's text is S3c's (diagnostics)
		}
		ran++
		_, ext := goKinds(pkg)
		want, foreign := goTypes(pkg)
		for name, re := range forms {
			for _, l := range want {
				if re.MatchString(l) {
					reach[name]++
					break
				}
			}
		}
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		writeLines(t, file, []string{src})
		if got := dropCharged(runDump(t, ctx, td, "-std", stdDir(), e, f, file)); setDiff(want, got) != "" {
			report(seed, src, want, got)
		}
	}
	for k, n := range reachKinds {
		if n == 0 {
			t.Errorf("the kinds cases reached no %s parameter; the generator lost a form (%s)", k, replay(base))
		}
	}
	if ran < 100 {
		t.Errorf("the Go checker refused %d of 200 types cases", 200-ran)
	}
	for name := range forms {
		if reach[name] == 0 {
			t.Errorf("the types cases reached no %s; the generator lost a form (%s)", name, replay(base))
		}
	}
}

// cycleRE reads an instantiation-cycle diagnostic (mono.go).
var cycleRE = regexp.MustCompile(`^(\d+:\d+): S108 instantiation cycle: (\S+) is instantiated with (.+), which grows on every round`)

// cycleLines is the Go checker's instantiation cycles in typedump -kinds'
// form, "cycle LINE:COL INST ARG".
func cycleLines(ds []check.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if m := cycleRE.FindStringSubmatch(fmt.Sprint(d)); m != nil {
			out = append(out, "cycle "+m[1]+" "+m[2]+" "+m[3])
		}
	}
	return out
}

// typesDump builds compiler/cmd/typedump.
func typesDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "typedump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/typedump"); err != nil {
		t.Fatalf("ergo build ./cmd/typedump: %v\n%s", err, o)
	}
	return bin
}

// stdDir is the std the dumps load imports from.
func stdDir() string { return filepath.Join(repoRoot(), "internal", "sketch", "std") }

// runDump runs a dump in its own process group under the kernel memory
// limit and returns its lines.
func runDump(t *testing.T, ctx context.Context, bin string, args ...string) []string {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := proclimit.Run(cmd, limit); err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(bin), args, err, errOut.String())
	}
	return lines(out.String())
}

func writeLines(t *testing.T, path string, ls []string) {
	t.Helper()
	text := strings.Join(ls, "\n")
	if len(ls) > 0 && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// typesPackage is the checked package of dir, or nil: a std folder's from
// the std importer (std is not a module root), the prelude's through a use
// of one of its types, any other folder's as a program root.
func typesPackage(dir string) *check.Package {
	std := filepath.Join(repoRoot(), "internal", "sketch", "std")
	if dir == std {
		outside, ds := check.CheckSource("func uses(_ LiveScheduler) {}\n")
		if len(ds) != 0 {
			return nil
		}
		for id, obj := range outside.Info.Uses {
			if tn, ok := obj.(*check.TypeName); ok && id.Name == "LiveScheduler" {
				return tn.Pkg
			}
		}
		return nil
	}
	if r, err := filepath.Rel(std, dir); err == nil && !strings.HasPrefix(r, "..") {
		p, err := check.Std.Import(filepath.ToSlash(r))
		if err != nil {
			return nil
		}
		return p
	}
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		return nil
	}
	return a.Root().Package
}

// fieldOf reads field name of the struct v points to.
func fieldOf(v reflect.Value, name string) reflect.Value {
	f := v.Elem().FieldByName(name)
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

func scopeNames(s *check.Scope) (map[string]check.Object, *check.Scope) {
	v := reflect.ValueOf(s)
	return fieldOf(v, "names").Interface().(map[string]check.Object), fieldOf(v, "parent").Interface().(*check.Scope)
}

func kindWord(tp *check.TypeParam) string {
	if c := reflect.ValueOf(tp).Elem().FieldByName("conflict"); c.IsValid() && c.Bool() {
		return "conflict"
	}
	switch tp.Kind {
	case check.KindErrors:
		return "errors"
	case check.KindUses:
		return "uses"
	}
	return "type"
}

func paramKinds(n *check.Named) string {
	ws := make([]string, len(n.TParams))
	for i, tp := range n.TParams {
		ws[i] = kindWord(tp)
	}
	return strings.Join(ws, ",")
}

// visible calls f for each type name pkg can name that is not its own
// and that typedump does not resolve itself: the prelude's and the
// universe's (qualifier "-") and a Go import's (its local name). An Ergo
// import's types are XRefs into the import's interface, which typedump
// computes from the import's source (-std).
func visible(pkg *check.Package, f func(qual, name string, tn *check.TypeName)) {
	own, parent := scopeNames(pkg.Scope)
	var prelude *check.Package
	for s := parent; s != nil; {
		ns, next := scopeNames(s)
		for name, o := range ns {
			if tn, ok := o.(*check.TypeName); ok && tn.Pkg != nil && tn.Pkg.Path == "prelude" {
				prelude = tn.Pkg
			}
			if _, shadowed := own[name]; shadowed {
				continue
			}
			if tn, ok := o.(*check.TypeName); ok {
				f("-", name, tn)
			}
		}
		s = next
	}
	// The prelude's hashTable is in no scope, yet a Map with a provider is
	// an instance of it (Map[K, V, H]; types.preludeQual).
	if prelude != nil && pkg != prelude {
		ns, _ := scopeNames(prelude.Scope)
		if tn, ok := ns["hashTable"].(*check.TypeName); ok {
			f("@prelude", "hashTable", tn)
		}
	}
	seen := map[string]bool{}
	for id, o := range pkg.Info.Uses {
		pn, ok := o.(*check.PkgName)
		if !ok || pn.Pkg == nil || seen[id.Name] {
			continue
		}
		if spec, _ := fieldOf(reflect.ValueOf(pn), "spec").Interface().(*syntax.ImportSpec); spec != nil && spec.Kind != syntax.ImportGo {
			continue
		}
		seen[id.Name] = true
		ns, _ := scopeNames(pn.Pkg.Scope)
		for name, o := range ns {
			if tn, ok := o.(*check.TypeName); ok {
				f(id.Name, name, tn)
			}
		}
	}
}

// goKinds is the Go checker's kinds in typedump -kinds' form, "NAME KIND,…" per
// generic type declaration of pkg, and the EXT table: "HEAD KIND,…" per
// generic type pkg can name that is not its own.
func goKinds(pkg *check.Package) (local, ext []string) {
	for _, tn := range pkg.Types {
		if n, ok := tn.Type.(*check.Named); ok && len(n.TParams) > 0 {
			local = append(local, tn.Name+" "+paramKinds(n))
		}
	}
	for _, f := range pkg.Funcs {
		if f.Decl.Recv == nil && f.Sig != nil && len(f.Sig.TParams) > 0 {
			ws := make([]string, len(f.Sig.TParams))
			for i, tp := range f.Sig.TParams {
				ws[i] = kindWord(tp)
			}
			local = append(local, "func "+f.Name+" "+strings.Join(ws, ","))
		}
	}
	visible(pkg, func(qual, name string, tn *check.TypeName) {
		n, ok := tn.Type.(*check.Named)
		if !ok || len(n.TParams) == 0 {
			return
		}
		head := name
		if qual != "-" {
			head = qual + "." + name
		}
		ext = append(ext, head+" "+paramKinds(n))
	})
	sort.Strings(local)
	sort.Strings(ext)
	return local, ext
}

// goTypes is the Go checker's record field types in typedump's form,
// "REC [P,…]" per record and "REC.FIELD TYPE" per written field (a field
// an include splices in is not written), and the FOREIGN table: first
// "@pkg NAME PATH QUALIFY", then "QUAL NAME ARITY PATH PRINT KEY" per type
// pkg can name that is not its own, PRINT and KEY with spaces as ^: the
// type's text and sort key as a use of the name gives them (an alias's are
// its target's).
// takesParams reports a variant written with the union's own parameters,
// in their order, as its type arguments.
func takesParams(v *check.Named, ps []*check.TypeParam) bool {
	if len(v.TArgs) != len(ps) {
		return false
	}
	for i, a := range v.TArgs {
		if p, ok := a.(*check.TypeParam); !ok || p != ps[i] {
			return false
		}
	}
	return true
}

func goTypes(pkg *check.Package) (fields, foreign []string) {
	for _, tn := range pkg.Types {
		n, ok := tn.Type.(*check.Named)
		if !ok {
			continue
		}
		if def, _ := fieldOf(reflect.ValueOf(n), "def").Interface().(check.Type); def != nil {
			fields = append(fields, "def "+tn.Name+" "+def.String())
			continue
		}
		u, _ := fieldOf(reflect.ValueOf(n), "under").Interface().(check.Type)
		s, ok := u.(*check.Struct)
		if !ok {
			continue
		}
		ps := make([]string, len(n.TParams))
		for i, tp := range n.TParams {
			ps[i] = tp.Name
		}
		fields = append(fields, tn.Name+" ["+strings.Join(ps, ",")+"]")
		for _, f := range s.Fields {
			o := fieldOf(reflect.ValueOf(f), "origin")
			if !o.IsNil() && o.Pointer() != reflect.ValueOf(f).Pointer() {
				continue
			}
			fields = append(fields, tn.Name+"."+f.Name+" "+f.Type.String())
		}
	}
	for _, f := range pkg.Funcs {
		switch {
		case f.Decl.Recv != nil || f.Sig == nil:
		case wiredOfType(f):
			fields = append(fields, "func "+f.Name+" ?")
		default:
			fields = append(fields, "func "+f.Name+" "+f.Sig.String())
		}
	}
	fields = dropCharged(fields)
	visible(pkg, func(qual, name string, tn *check.TypeName) {
		arity, path := 0, "-"
		if n, ok := tn.Type.(*check.Named); ok {
			arity = len(n.TParams)
		}
		if tn.Pkg != nil {
			path = tn.Pkg.Path
		}
		foreign = append(foreign, fmt.Sprintf("%s %s %d %s %s", qual, name, arity, path, printKey(tn)))
	})
	visible(pkg, func(qual, name string, tn *check.TypeName) {
		n, ok := tn.Type.(*check.Named)
		if !ok {
			return
		}
		u, ok := fieldOf(reflect.ValueOf(n), "under").Interface().(*check.Union)
		if !ok || u == nil || len(u.Variants) == 0 {
			return
		}
		vs := make([]string, 0, len(u.Variants))
		for _, v := range u.Variants {
			vn, ok := v.(*check.Named)
			if !ok || vn.Obj == nil || vn.Obj.Pkg != tn.Pkg {
				return
			}
			// A generic union lists only the variants that take its
			// parameters in their order, and those that take none (None):
			// Option's Some[T] and None. The Ergo side
			// instantiates one that takes them with the union's arguments when
			// a bare name's context fixes them (types.Elided).
			if len(n.TParams) > 0 && len(vn.TArgs) > 0 && !takesParams(vn, n.TParams) {
				continue
			}
			vs = append(vs, vn.Obj.Name)
		}
		if len(vs) == 0 {
			return
		}
		foreign = append(foreign, "@union "+qual+" "+name+" "+strings.Join(vs, ","))
	})
	sort.Strings(fields)
	sort.Strings(foreign)
	qual := "1"
	if !fieldOf(reflect.ValueOf(pkg), "qualify").Bool() {
		qual = "0"
	}
	foreign = append([]string{"@pkg " + pkg.Name + " " + pkg.Path + " " + qual}, foreign...)
	return fields, foreign
}

// chargedRe matches go.Foreign in a capability row. The body check
// charges it to a function whose body calls Go (expr.go charge), so it is
// S3d's, not the written signature's; both sides drop it.
func dropCharged(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		if strings.HasPrefix(l, "func ") {
			for _, c := range []string{"go.Foreign | ", " | go.Foreign", " needs go.Foreign"} {
				l = strings.ReplaceAll(l, c, "")
			}
		}
		out[i] = l
	}
	return out
}

// wiredOfType reports whether f's signature writes wired[T] of a T that
// is not one of its bracket parameters: that row is the codec rows of
// what T reaches (wirecall.go wireRow), which S3t does not resolve yet,
// so both sides print the signature as "?".
func wiredOfType(f *check.Func) bool {
	params := map[string]bool{}
	if f.Decl.TypeParams != nil {
		for _, fl := range f.Decl.TypeParams.List {
			for _, n := range fl.Names {
				params[n.Name] = true
			}
		}
	}
	found := false
	syntax.Inspect(f.Decl.Type, func(n syntax.Node) bool {
		ix, ok := n.(*syntax.IndexExpr)
		if !ok {
			return true
		}
		if id, ok := ix.X.(*syntax.Ident); ok && id.Name == "wired" {
			if a, ok := unparenX(ix.Indices[0]).(*syntax.Ident); !ok || len(ix.Indices) != 1 || !params[a.Name] {
				found = true
			}
		}
		return true
	})
	return found
}

func unparenX(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

func printKey(tn *check.TypeName) string {
	t := tn.Type
	if n, ok := t.(*check.Named); ok {
		if a, ok := fieldOf(reflect.ValueOf(n), "alias").Interface().(check.Type); ok && a != nil {
			t = a
		}
	}
	key := t.String()
	if n, ok := t.(*check.Named); ok && len(n.TArgs) == 0 {
		key = n.Obj.Name
		if n.Obj.Pkg != nil {
			key = n.Obj.Pkg.Path + "." + key
		}
	}
	return strings.ReplaceAll(t.String(), " ", "^") + " " + strings.ReplaceAll(key, " ", "^")
}

// rng is mulberry32, a small seeded generator, so a seed replays.
type rng struct{ s uint32 }

func (r *rng) next() float64 {
	r.s += 0x6d2b79f5
	t := r.s
	t = (t ^ (t >> 15)) * (1 | t)
	t = (t + (t^(t>>7))*(61|t)) ^ t
	return float64(t^(t>>14)) / 4294967296
}

func (r *rng) n(k int) int { return int(r.next() * float64(k)) }

func pick[T any](r *rng, xs []T) T { return xs[r.n(len(xs))] }

// genKinds is one kinds case.
func genKinds(seed uint32) string {
	r := &rng{s: seed}
	type decl struct {
		name   string
		params []string
		shape  string
	}
	var decls []decl
	for i := range 1 + r.n(4) {
		k := 1 + r.n(3)
		decls = append(decls, decl{fmt.Sprintf("G%d", i), []string{"A", "B", "C"}[:k], pick(r, []string{"record", "record", "union", "ops"})})
	}
	ownMap := r.next() < 0.3
	if ownMap {
		// The package's own Map: Map[K, V] is its instance.
		decls = append(decls, decl{"Map", []string{"A", "B"}, "record"})
	}
	inMap := false
	// cur is the declaration whose body is generated: its own instances
	// grow more often, so a cycle meets a plain edge it makes heavier and
	// a component holds more than one growing edge.
	cur := ""
	var ty func(ps []string, d int) string
	ty = func(ps []string, d int) string {
		leaf := func() string {
			if r.next() < 0.75 {
				return pick(r, ps)
			}
			return pick(r, []string{"int", "string", "Fate"})
		}
		if d <= 0 {
			return leaf()
		}
		switch r.n(11) {
		case 0:
			return "[]" + ty(ps, d-1)
		case 1:
			return "*" + ty(ps, d-1)
		case 2:
			return "Option[" + ty(ps, d-1) + "]"
		case 3:
			return "func() int fails " + pick(r, ps)
		case 4:
			return "func() int needs " + pick(r, ps)
		case 5:
			return "func(x " + ty(ps, d-1) + ") " + ty(ps, d-1)
		case 6:
			if inMap {
				return leaf()
			}
			return "Map[string, " + ty(ps, d-1) + "]"
		case 7, 8:
			g := pick(r, decls)
			plain, grow := 0.75, 0.85
			if g.name == cur {
				plain, grow = 0.5, 0.9
			}
			args := make([]string, len(g.params))
			for i := range args {
				switch x := r.next(); {
				case x < 0.2:
					args[i] = pick(r, ps) + " | " + pick(r, ps)
				case x < plain:
					args[i] = pick(r, ps)
				case x < grow:
					// A larger type: on a cycle of the parameter graph
					// the instances grow (S108). A builtin Map's key
					// names a local type the cycle's text prints.
					if !ownMap && r.next() < 0.5 {
						args[i] = "Map[Fate, " + pick(r, ps) + "]"
					} else {
						args[i] = "[]" + pick(r, ps)
					}
				default:
					args[i] = pick(r, []string{"int", "Fate"})
				}
			}
			return g.name + "[" + strings.Join(args, ", ") + "]"
		case 9:
			return "func() " + ty(ps, d-1) + " fails " + pick(r, ps) + " needs " + pick(r, ps)
		}
		return leaf()
	}
	lines := []string{"type Fate { Text string }", ""}
	for _, g := range decls {
		bs := make([]string, len(g.params))
		for i, p := range g.params {
			bs[i] = p
			if r.next() < 0.1 {
				bs[i] = p + " comparable"
			}
		}
		bracket := strings.Join(bs, ", ")
		switch g.shape {
		case "record":
			// The package's own Map nests no instance of itself: the Go
			// checker does not end on Map[A, B] { f0 func() int fails B,
			// f1 Map[string, Map[string, B]] }, which grows through the
			// error row B (S3t follow-up CK-ROWGROW; the type-parameter
			// form, CK-MAPGROW, is S108). Growing instances of the other
			// declarations come from their []P arguments.
			inMap = g.name == "Map"
			cur = g.name
			fs := make([]string, len(g.params))
			for i := range fs {
				fs[i] = fmt.Sprintf("f%d %s", i, ty(g.params, 2))
			}
			if r.next() < 0.3 {
				// A plain instance of itself first: a later growing one
				// makes its edges heavier.
				fs = append([]string{"s " + g.name + "[" + strings.Join(g.params, ", ") + "]"}, fs...)
			}
			inMap = false
			cur = ""
			lines = append(lines, "type "+g.name+"["+bracket+"] { "+strings.Join(fs, ", ")+" }", "")
		case "ops":
			var ms []string
			for i := range g.params {
				ms = append(ms, fmt.Sprintf("\tOp%d(x %s) %s,", i, ty(g.params, 1), ty(g.params, 1)))
			}
			var inc []decl
			for _, h := range decls {
				if h.shape == "ops" && h.name != g.name {
					inc = append(inc, h)
				}
			}
			if len(inc) > 0 && r.next() < 0.5 {
				h := pick(r, inc)
				args := make([]string, len(h.params))
				for i := range args {
					args[i] = pick(r, g.params)
				}
				ms = append(ms, "\t.."+h.name+"["+strings.Join(args, ", ")+"],")
			}
			lines = append(lines, "type "+g.name+"["+bracket+"] {")
			lines = append(lines, ms...)
			lines = append(lines, "}", "")
		default:
			var vs []string
			for v := range 1 + r.n(3) {
				t := ty(g.params, 1)
				var used []string
				for _, p := range g.params {
					if regexp.MustCompile(`\b` + p + `\b`).MatchString(t) {
						used = append(used, p)
					}
				}
				head := fmt.Sprintf("%sV%d", g.name, v)
				if len(used) > 0 {
					head += "[" + strings.Join(used, ", ") + "]"
				}
				vs = append(vs, head+"{x "+t+"}")
			}
			lines = append(lines, "type "+g.name+"["+bracket+"] = "+strings.Join(vs, " | "), "")
		}
	}
	for i := range r.n(5) {
		g := pick(r, decls)
		own := []string{"X", "Y"}[:1+r.n(2)]
		args := make([]string, len(g.params))
		for j := range args {
			switch x := r.next(); {
			case x < 0.7:
				args[j] = pick(r, own)
			case x < 0.85:
				// Not a bare name: walked with the slot's kind, a
				// conflicted slot's as a type.
				args[j] = "[]" + pick(r, own)
			default:
				args[j] = "int"
			}
		}
		bracket := strings.Join(own, ", ")
		if r.next() < 0.2 {
			// A constrained parameter is a type, whatever its positions.
			bracket += " comparable"
		}
		h := ty(own, 1)
		if r.next() < 0.3 {
			// The receiver's instance grown: a cycle only through the
			// function's receiver link (monoLinkSelf), which ties g's
			// arguments back to the function's parameters.
			grown := make([]string, len(args))
			for j, a := range args {
				grown[j] = a
				if slices.Contains(own, a) {
					grown[j] = "[]" + a
				}
			}
			h = g.name + "[" + strings.Join(grown, ", ") + "]"
		}
		lines = append(lines, fmt.Sprintf("func use%d[%s](g %s[%s], h %s) int { 0 }", i, bracket, g.name, strings.Join(args, ", "), h), "")
	}
	return strings.Join(lines, "\n")
}

// genTypes is one types case.
func genTypes(seed uint32) string {
	r := &rng{s: seed}
	ownMap := r.next() < 0.2
	recs := []string{"R0", "R1", "R2"}[:1+r.n(3)]
	errs := []string{"E0", "E1", "E2"}
	leaves := append(append([]string{"int", "string", "bool", "byte", "rune", "float64", "uint16", "duration"}, recs...), append(errs, "fs.NotFound", "time.Time", "Al")...)
	errRow := func() string {
		xs := make([]string, 1+r.n(3))
		for i := range xs {
			// EU and flag.Failure are unions: a row lists their variants.
			xs[i] = pick(r, append(append([]string{}, errs...), "fs.NotFound", "fs.Denied", "fs.Failed", "EU", "flag.Failure"))
		}
		return strings.Join(xs, " | ")
	}
	var ty func(d int, ps []string) string
	ty = func(d int, ps []string) string {
		base := func() string {
			if len(ps) > 0 && r.next() < 0.3 {
				return pick(r, ps)
			}
			return pick(r, leaves)
		}
		if d <= 0 {
			return base()
		}
		switch r.n(13) {
		case 0:
			return "[]" + ty(d-1, ps)
		case 1:
			return fmt.Sprintf("[%d]%s", 1+r.n(9), ty(d-1, ps))
		case 2:
			return "Map[" + pick(r, []string{"string", "int"}) + ", " + ty(d-1, ps) + "]"
		case 3:
			return "Option[" + ty(d-1, ps) + "]"
		case 4:
			return "Box[" + ty(d-1, ps) + "]"
		case 5:
			return "Job[" + ty(d-1, ps) + ", " + errRow() + "]"
		case 6:
			if r.next() < 0.5 {
				return "Cap[" + ty(d-1, ps) + "]"
			}
			return "Cap[" + ty(d-1, ps) + ", " + errRow() + "]"
		case 7:
			var params []string
			for i := range r.n(3) {
				switch k := r.next(); {
				case k < 0.15:
					params = append(params, fmt.Sprintf("f%d own func() int", i))
				case k < 0.3:
					params = append(params, fmt.Sprintf("f%d entry func(int)", i))
				case k < 0.4:
					params = append(params, fmt.Sprintf("p%d *%s", i, ty(0, ps)))
				case k < 0.55:
					params = append(params, fmt.Sprintf("x%d, y%d %s", i, i, ty(d-1, ps)))
				default:
					params = append(params, fmt.Sprintf("x%d %s", i, ty(d-1, ps)))
				}
			}
			res := ""
			switch x := r.next(); {
			case x < 0.2:
			case r.next() < 0.2:
				res = " (" + ty(0, ps) + ", " + ty(0, ps) + ")"
			default:
				res = " " + ty(d-1, ps)
			}
			fails := ""
			if r.next() < 0.4 {
				fails = " fails " + errRow()
			}
			return "func(" + strings.Join(params, ", ") + ")" + res + fails
		case 8:
			return "sync.Ref[" + ty(d-1, ps) + "]"
		case 9:
			// A row written for a type parameter: its value type, a set.
			xs := make([]string, 2+r.n(2))
			for i := range xs {
				xs[i] = pick(r, append(append([]string{}, errs...), "fs.NotFound", "fs.Denied", "time.Time"))
			}
			return "Box[" + strings.Join(xs, " | ") + "]"
		}
		return base()
	}
	lines := []string{"import flag", "import fs", "import sync", "import time", ""}
	for _, e := range errs {
		lines = append(lines, "type "+e+" { Why string }")
	}
	lines = append(lines,
		"type Uses { a fs.NotFound, b sync.Ref[int], c time.Time, d flag.Help }",
		"type Box[T] { v T }",
		"type EU = EU0{Why string} | EU1{Why string} | E2",
		"type Job[T, F] { run func() T fails F }",
		"type Cap[T, F, U] { run func() T fails F needs U }",
		// A row whose order by package-qualified key ($zz, fs., main.)
		// is not its order by text (fs., main., zz): Row.list sorts by key.
		"type Rz[zz] { run func() int fails zz | "+pick(r, errs)+" | fs.Denied }",
		"type Al = "+pick(r, []string{"Box[int]", "Job[string, E0]", "fs.NotFound", "Option[E1]"}))
	if ownMap {
		lines = append(lines, "type Map[K, V] { k K, v V }")
	}
	lines = append(lines, "")
	for _, name := range recs {
		if r.next() < 0.3 {
			var vs []string
			for v := range 2 {
				t := ty(1, []string{"T"})
				if regexp.MustCompile(`\bT\b`).MatchString(t) {
					vs = append(vs, fmt.Sprintf("%sV%d[T]{x %s}", name, v, t))
				} else {
					vs = append(vs, fmt.Sprintf("%sV%d{x %s}", name, v, t))
				}
			}
			lines = append(lines, "type "+name+"U[T] = "+strings.Join(vs, " | "))
		}
		fs := make([]string, 1+r.n(4))
		for i := range fs {
			fs[i] = fmt.Sprintf("f%d %s", i, ty(2, nil))
		}
		lines = append(lines, "type "+name+" { "+strings.Join(fs, ", ")+" }")
	}
	// Defined types, a refined one (its derived ParseD), and functions
	// whose parameters are marked, whose rows name unions and wired[T].
	lines = append(lines, "",
		"type D0 "+pick(r, []string{"[]int", "Map[string, R0]", "func(int) string fails E0", "[3]byte"}),
		"type D1 int where it > 0",
		"type d2 string where len(it) > 0")
	for i := range 1 + r.n(4) {
		tps := pick(r, []string{"", "[T]", "[T wire]", "[T, F]"})
		var ps []string
		if strings.Contains(tps, "T") {
			ps = []string{"T"}
		}
		var params []string
		for j := range 1 + r.n(3) {
			switch k := r.next(); {
			case k < 0.15:
				params = append(params, fmt.Sprintf("g%d own func(int) int", j))
			case k < 0.35:
				params = append(params, fmt.Sprintf("g%d entry func(int)", j))
			case k < 0.45:
				params = append(params, fmt.Sprintf("r%d R0", j))
			default:
				params = append(params, fmt.Sprintf("p%d %s", j, ty(1, ps)))
			}
		}
		res, body := "", "{}"
		if r.next() < 0.5 {
			// Not a function type: a non-escaping one is never returned.
			params = append([]string{"x " + ty(0, ps)}, params...)
			res, body = " "+strings.SplitN(params[0], " ", 2)[1], "{ x }"
		}
		clauses := ""
		if r.next() < 0.6 {
			row := errRow()
			if tps == "[T, F]" && r.next() < 0.5 {
				row = "F | " + row
			}
			if tps == "[T wire]" && r.next() < 0.6 {
				row += " | wired[T]"
			}
			clauses += " fails " + row
		}
		if r.next() < 0.4 {
			if tps == "[T wire]" && r.next() < 0.6 {
				clauses += " needs wired[T]"
			} else {
				clauses += " needs " + pick(r, []string{"fs.Files", "time.Clock", "fs.Files | time.Clock"})
			}
		}
		lines = append(lines, fmt.Sprintf("func f%d%s(%s)%s%s %s", i, tps, strings.Join(params, ", "), res, clauses, body))
	}
	lines = append(lines, "", "func main() {}", "")
	return strings.Join(lines, "\n")
}

// typeForms are the forms the types cases must reach, by the Go checker's
// text of a field or a record line.
func typeForms() map[string]*regexp.Regexp {
	return map[string]*regexp.Regexp{
		"own":                      regexp.MustCompile(`own func`),
		"entry":                    regexp.MustCompile(`entry func`),
		"array":                    regexp.MustCompile(`\[\d+\]\S`),
		"slice":                    regexp.MustCompile(`\[\]\S`),
		"map type":                 regexp.MustCompile(`(^| |\[)Map\[`),
		"own Map":                  regexp.MustCompile(`main\.Map\[`),
		"option":                   regexp.MustCompile(`Option\[`),
		"pointer":                  regexp.MustCompile(`\*`),
		"tuple":                    regexp.MustCompile(`\) \(`),
		"row":                      regexp.MustCompile(`\| `),
		"byte":                     regexp.MustCompile(`uint8`),
		"rune":                     regexp.MustCompile(`int32`),
		"instance":                 regexp.MustCompile(`main\.(Box|Job|Cap)\[`),
		"empty row":                regexp.MustCompile(`, \]`),
		"variant":                  regexp.MustCompile(`^R\dV\d \[`),
		"variant with a parameter": regexp.MustCompile(`^R\dV\d \[T\]`),
		"func":                     regexp.MustCompile(`^func f\d `),
		"own param":                regexp.MustCompile(`^func f\d func\(.*own func\(int\) int`),
		"own record param":         regexp.MustCompile(`^func f\d func\((.*, )?main\.R0`),
		"def":                      regexp.MustCompile(`^def D0 `),
		"refined parse":            regexp.MustCompile(`^func ParseD1 func\(int\) main\.D1 fails Unsatisfied$`),
		"lower refined parse":      regexp.MustCompile(`^func parseD2 `),
		"local union row":          regexp.MustCompile(`fails .*main\.EU0`),
		"foreign union row":        regexp.MustCompile(`fails .*flag\.Help`),
		"wired":                    regexp.MustCompile(`fails .*WireWrongKind.* wired\[T\]`),
		"wired needs":              regexp.MustCompile(`needs wired\[T\]`),
		"row param":                regexp.MustCompile(`fails F \| `),
		"row by key":               regexp.MustCompile(`fails zz \| fs\.Denied \| main\.E\d$`),
	}
}

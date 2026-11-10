package std_test

import (
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// hostExtern reports whether the extern f of std reaches host state: a
// runtime/v1 body that its declaration marks //ergo:host (v1.Body.Host;
// BodyOf refuses an unmarked body that reaches a host member, so an error
// counts as host too), or a body of another Go package outside the pure
// allowlist (runtime/rt's Sleep). An impure body that is not marked
// reaches no host state: it makes or changes a value of the program, runs
// code the program gives it, or reads the running task (stdlib.md, Host
// packages 7).
func hostExtern(f *check.Func) bool {
	target, name := f.Decl.Extern.Split()
	if target == v1Path {
		body, err := v1.BodyOf(name)
		return err != nil || body.Host
	}
	return !check.PureGoPackage(target)
}

// impureExtern reports an extern whose Go body is not pure: a runtime/v1
// body not marked pure, or a Go package outside the pure allowlist.
func impureExtern(f *check.Func) bool {
	target, name := f.Decl.Extern.Split()
	if target == v1Path {
		body, err := v1.BodyOf(name)
		return err != nil || !body.Pure
	}
	return !check.PureGoPackage(target)
}

// stdSurface is the prelude and every std package, and a program package
// that imports none of them.
func stdSurface(t *testing.T) ([]*check.Package, *check.Package) {
	t.Helper()
	outside, ds := check.CheckSource("func uses(_ LiveScheduler) {}\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	var prelude *check.Package
	for id, obj := range outside.Info.Uses {
		if tn, ok := obj.(*check.TypeName); ok && id.Name == "LiveScheduler" {
			prelude = tn.Pkg
		}
	}
	if prelude == nil {
		t.Fatal("no prelude package behind LiveScheduler")
	}
	pkgs := []*check.Package{prelude}
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs, outside
}

// heldTypes is every host provider of pkgs and each record another
// package needs to build one (check.Authorities).
func heldTypes(pkgs []*check.Package, outside *check.Package) []*check.TypeName {
	var held []*check.TypeName
	for _, p := range pkgs {
		for _, tn := range hostProviderTypes(p) {
			for _, e := range append([]*check.TypeName{tn}, check.Authorities(tn.Type, outside)...) {
				if !slices.Contains(held, e) {
					held = append(held, e)
				}
			}
		}
	}
	return held
}

// externCallerViolations are the functions of pkgs that reach the host with
// no authority (stdlib.md, Host packages 7). A function reaches when its
// body names an extern that host reports, or a function of its package
// that reaches; a fixed point. A function that holds the authority (its
// written row lists Ambient, or a parameter's type is held) does not
// reach: its caller supplies the provider. An inferred row is no
// authority. A closure is judged in the function around it, unless the
// closure holds the authority itself. Each reaching function with a body
// is a violation, and so is an exported reaching extern, which another
// package calls directly. In source order per package.
func externCallerViolations(pkgs []*check.Package, held []*check.TypeName, host func(*check.Func) bool) []string {
	var out []string
	holds := func(sig *check.Signature, written bool) bool {
		if sig == nil {
			return false
		}
		if written && rowHasAmbient(sig.Uses) {
			return true
		}
		for _, p := range sig.Params {
			if n, ok := p.Type.(*check.Named); ok && slices.Contains(held, n.Obj) {
				return true
			}
		}
		return false
	}
	authorized := func(f *check.Func) bool {
		return holds(f.Sig, f.Decl != nil && f.Decl.Type != nil && f.Decl.Type.Needs != nil)
	}
	for _, p := range pkgs {
		// The closures that hold the authority: their bodies are not
		// asked. A trailing block has no row of its own.
		closed := map[syntax.Node]bool{}
		for lit, sig := range p.Info.Literals {
			written := false
			if fl, ok := lit.(*syntax.FuncLit); ok {
				written = fl.Type != nil && fl.Type.Needs != nil
			}
			if holds(sig, written) {
				closed[lit] = true
			}
		}
		reach := map[*check.Func]bool{}
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Extern != nil && host(f) && !authorized(f) {
				reach[f] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, f := range p.Funcs {
				if reach[f] || f.Decl == nil || f.Decl.Body == nil || authorized(f) {
					continue
				}
				syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
					if closed[n] {
						return false
					}
					if id, ok := n.(*syntax.Ident); ok {
						if g, ok := p.Info.Uses[id].(*check.Func); ok && g.Pkg == p && reach[g] {
							reach[f] = true
						}
					}
					return !reach[f]
				})
				changed = changed || reach[f]
			}
		}
		for _, f := range p.Funcs {
			switch {
			case !reach[f]:
			case f.Decl.Body != nil:
				out = append(out, p.Name+"."+f.Name+" reaches a host extern and neither lists Ambient in its written row nor takes a parameter of a held type")
			case isExportedName(f.Name):
				out = append(out, p.Name+"."+f.Name+" is an exported host extern and neither lists Ambient in its written row nor takes a parameter of a held type")
			}
		}
	}
	return out
}

// providerExterns are the externs of pkg that a function whose first
// parameter is a host provider of pkg names, directly or through
// functions of pkg: the host bodies the providers stand for.
func providerExterns(pkg *check.Package) []string {
	providers := hostProviderTypes(pkg)
	var out []string
	seen := map[*check.Func]bool{}
	var visit func(f *check.Func)
	visit = func(f *check.Func) {
		if seen[f] || f.Decl == nil {
			return
		}
		seen[f] = true
		if f.Decl.Extern != nil {
			out = append(out, pkg.Name+"."+f.Name)
			return
		}
		if f.Decl.Body == nil {
			return
		}
		syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok {
				if g, ok := pkg.Info.Uses[id].(*check.Func); ok && g.Pkg == pkg {
					visit(g)
				}
			}
			return true
		})
	}
	for _, f := range pkg.Funcs {
		if f.Sig == nil || len(f.Sig.Params) == 0 {
			continue
		}
		if n, ok := f.Sig.Params[0].Type.(*check.Named); ok && slices.Contains(providers, n.Obj) {
			visit(f)
		}
	}
	return out
}

// TestStdExternCallersHoldAuthority: every std function whose body reaches
// a host extern, directly, through functions of its package or in a
// closure, lists Ambient in its written row or takes a parameter of a held
// type (batch-10 r7 On base: `func Peek(path string) string … { try
// hostReadFile(path) }` in fs passed every check). The real std has no
// violation; the rows check the walk.
func TestStdExternCallersHoldAuthority(t *testing.T) {
	pkgs, outside := stdSurface(t)
	held := heldTypes(pkgs, outside)
	for _, v := range externCallerViolations(pkgs, held, hostExtern) {
		t.Errorf("%s", v)
	}
	// Each std extern's runtime/v1 body passes BodyOf, which refuses an
	// unmarked body that reaches a host member; and an impure body that a
	// host provider's function reaches is marked //ergo:host.
	externs := map[string]*check.Func{}
	var reached []string
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Extern != nil {
				externs[p.Name+"."+f.Name] = f
			}
		}
		reached = append(reached, providerExterns(p)...)
	}
	for name, f := range externs {
		if target, body := f.Decl.Extern.Split(); target == v1Path {
			if _, err := v1.BodyOf(body); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		if slices.Contains(reached, name) && impureExtern(f) && !hostExtern(f) {
			t.Errorf("a host provider's function reaches %s, whose body is not marked %s", name, v1.HostMark)
		}
	}
	// The host externs found today, so a new one is a decision.
	var hosts []string
	for name, f := range externs {
		if hostExtern(f) {
			hosts = append(hosts, name)
		}
	}
	slices.Sort(hosts)
	want := []string{"exec.hostRun", "fs.OpenRoot", "fs.hostAppendFile", "fs.hostChmod", "fs.hostMkdirAll", "fs.hostMkdirAllMode",
		"fs.hostReadDir", "fs.hostReadFile", "fs.hostRemove", "fs.hostRename", "fs.hostStat", "fs.hostWriteFile", "fs.hostWriteFileMode",
		"http.Listen", "http.fetch", "http.send", "http.serve",
		"io.hostEprint", "io.hostIsTerminal", "io.hostLines", "io.hostPrint", "io.hostReadAll",
		"os.hostArgs", "os.hostGetenv", "os.hostHasEnv", "prelude.Sleep",
		"rand.hostFloat64", "rand.hostIntN", "rand.hostUint64", "rpc.call", "time.hostNow"}
	if !slices.Equal(hosts, want) {
		t.Errorf("host externs %v, want %v", hosts, want)
	}

	const base = `type Host { live int }

type Gone { Path string }

func NewHost() Host needs Ambient { Host{live: 1} }

func touch(_ Host) {}

func Read(_ Host) string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone

`
	reaches := func(names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, "fixture."+n+" reaches a host extern and neither lists Ambient in its written row nor takes a parameter of a held type")
		}
		return out
	}
	for _, row := range []struct {
		name, decls string
		want        []string
	}{
		{"control: only the provider", "", nil},
		{"Peek: an exported function calls the extern", `func Peek(path string) string fails Gone { try hostRead(path) }
`, reaches("Peek")},
		{"an unexported function calls the extern", `func peek(path string) string fails Gone { try hostRead(path) }
`, reaches("peek")},
		{"an unexported helper chain", `func Peek(path string) string fails Gone { try peek(path) }

func peek(path string) string fails Gone { try deeper(path) }

func deeper(path string) string fails Gone { try hostRead(path) }
`, reaches("Peek", "peek", "deeper")},
		{"a chain declared callee first", `func deeper(path string) string fails Gone { try hostRead(path) }

func peek(path string) string fails Gone { try deeper(path) }

func Peek(path string) string fails Gone { try peek(path) }
`, reaches("deeper", "peek", "Peek")},
		{"a closure", `func Peek(path string) string fails Gone {
	let f = func() string fails Gone { try hostRead(path) }
	try f()
}
`, reaches("Peek")},
		{"a closure that never runs", `func Peek(path string) func() string fails Gone {
	func() string fails Gone { try hostRead(path) }
}
`, reaches("Peek")},
		{"control: a closure that takes the provider", `func Peek(path string) func(h Host) string fails Gone {
	func(h Host) string fails Gone { try hostRead(path) }
}
`, nil},
		// An extern is no value (S108), so the value is of a helper.
		{"control: a closure whose written row lists Ambient", `func Peek(path string) func() string fails Gone needs Ambient {
	func() string fails Gone needs Ambient { try hostRead(path) }
}
`, nil},
		{"a closure whose inferred row holds Ambient", `func peek(path string) string fails Gone {
	let g = func() string fails Gone {
		touch(NewHost())
		try hostRead(path)
	}
	try g()
}
`, reaches("peek")},
		{"a function value", `func Peek(path string) string fails Gone {
	let f = peek
	try f(path)
}

func peek(path string) string fails Gone { try hostRead(path) }
`, reaches("Peek", "peek")},
		{"control: the chain holds the provider", `func Peek(h Host, path string) string fails Gone { try peek(h, path) }

func peek(_ Host, path string) string fails Gone { try hostRead(path) }
`, nil},
		{"control: a helper that holds the provider stops the chain", `func peek(_ Host, path string) string fails Gone { try hostRead(path) }

func Peek(path string) string fails Gone needs Ambient { try peek(NewHost(), path) }
`, nil},
		{"control: a written Ambient", `func Peek(path string) string fails Gone needs Ambient { try hostRead(path) }
`, nil},
		{"an inferred Ambient is no authority", `func peek(path string) string fails Gone {
	touch(NewHost())
	try hostRead(path)
}
`, reaches("peek")},
		{"a borrow of the provider is no authority", `func Peek(_ *Host, path string) string fails Gone { try hostRead(path) }
`, reaches("Peek")},
		{"an exported host extern", `extern go ergo.local/prototype/runtime/v1.FSStat func HostStat(path string) string fails Gone
`, []string{"fixture.HostStat is an exported host extern and neither lists Ambient in its written row nor takes a parameter of a held type"}},
		{"control: an exported host extern that takes the provider", `extern go ergo.local/prototype/runtime/v1.FSStat func HostStat(_ Host, path string) string fails Gone

func Stat(path string) string fails Gone needs Ambient { try HostStat(NewHost(), path) }
`, nil},
		{"control: an extern of the pure allowlist", `func Up(s string) string { up(s) }

extern go strings.ToUpper func up(s string) string
`, nil},
	} {
		src := base + row.decls
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		fake := checkFixture(t, row.name, f, src)
		got := externCallerViolations([]*check.Package{fake}, heldTypes([]*check.Package{fake}, outside), hostExtern)
		if !slices.Equal(got, row.want) {
			t.Errorf("%s: %q, want %q\n%s", row.name, got, row.want, src)
		}
	}
}

// checkFixture checks a fixture package, which may bind the Ergo runtime.
func checkFixture(t *testing.T, name string, f *syntax.File, src string) *check.Package {
	t.Helper()
	fake, ds := check.Check("fixture", []*syntax.File{f}, nil)
	for _, d := range ds {
		if !strings.Contains(d.Message, "only std binds the Ergo runtime") {
			t.Fatalf("%s: %v\n%s", name, ds, src)
		}
	}
	return fake
}

// A chain link is how f_i reaches f_(i+1) (or the extern, at the end).
var chainLinks = []string{"direct", "closure", "closure-held", "closure-ambient", "value"}

// A chain authority is what f_i holds: nothing, a written Ambient, a
// parameter of the provider, or an inferred Ambient (which is nothing).
var chainAuths = []string{"none", "written", "held", "inferred"}

type chainFunc struct {
	name, auth, link string
	needs            bool // it needs Ambient through an inferred row
}

// chainPackage is the fixture of one seed: a chain f_1 → … → f_d →
// hostRead of depth d in 1..3, each function with a random authority,
// link and visibility, and the functions the oracle reports.
func chainPackage(seed uint64) (string, []string, map[string]int) {
	r := rand.New(rand.NewPCG(seed, 0xe47e))
	hits := map[string]int{}
	d := 1 + r.IntN(3)
	hits["depth "+strconv.Itoa(d)]++
	fs := make([]chainFunc, d)
	for i := range fs {
		// An extern is no value (S108): the last link is not "value".
		links := chainLinks
		if i == d-1 {
			links = chainLinks[:len(chainLinks)-1]
		}
		fs[i] = chainFunc{
			name: "f" + strconv.Itoa(i+1),
			auth: chainAuths[r.IntN(len(chainAuths))],
			link: links[r.IntN(len(links))],
		}
		// An exported function with an inferred Ambient is S102, so an
		// inferred one stays unexported.
		if fs[i].auth != "inferred" && r.IntN(2) == 0 {
			fs[i].name = "F" + strconv.Itoa(i+1)
		}
	}
	// A function that needs Ambient and does not write it infers it, so
	// it is unexported (an exported one would be S102). It needs Ambient
	// when it makes a Host (it has no Host parameter and its callee or
	// its closure takes one), when it calls a function that needs Ambient,
	// or when its authority is the inferred one. From the extern up, since
	// a caller's need follows its callee's final row.
	for i := d - 1; i >= 0; i-- {
		f := &fs[i]
		makesHost := f.auth != "held" && (f.link == "closure-held" || i+1 < d && fs[i+1].auth == "held")
		calleeNeeds := i+1 < d && (fs[i+1].auth == "written" || fs[i+1].auth == "inferred" || fs[i+1].needs)
		f.needs = f.auth != "written" && (makesHost || calleeNeeds || f.auth == "inferred" || f.link == "closure-ambient")
		if f.needs {
			if f.auth == "none" {
				f.auth = "inferred"
			}
			f.name = strings.ToLower(f.name)
		}
	}
	for _, f := range fs {
		hits["auth "+f.auth]++
		hits["link "+f.link]++
	}
	// The oracle, from the extern up: f_i reaches unless it holds the
	// authority or its link is a closure that does.
	reached := true // the extern
	var want []string
	reachesAt := make([]bool, d)
	for i := d - 1; i >= 0; i-- {
		f := fs[i]
		reachesAt[i] = f.auth != "written" && f.auth != "held" && f.link != "closure-held" && f.link != "closure-ambient" && reached
		reached = reachesAt[i]
	}
	for i := range fs {
		if reachesAt[i] {
			want = append(want, "fixture."+fs[i].name+" reaches a host extern and neither lists Ambient in its written row nor takes a parameter of a held type")
		}
	}
	hits["verdict "+strconv.FormatBool(len(want) > 0)]++
	var b strings.Builder
	b.WriteString(`type Host { live int }

type Gone { Path string }

func NewHost() Host needs Ambient { Host{live: 1} }

func touch(_ Host) {}

func Read(_ Host) string fails Gone { try hostRead("x") }

extern go ergo.local/prototype/runtime/v1.FSReadFile func hostRead(path string) string fails Gone
`)
	// A Host for a call: the parameter, or a new one under Ambient (a
	// written row, or an unexported function's inferred one).
	for i, f := range fs {
		params, row := "path string", ""
		host := "NewHost()"
		switch f.auth {
		case "written":
			row = " needs Ambient"
		case "held":
			params, host = "h Host, path string", "h"
		}
		callee, args := "hostRead", "path"
		if i+1 < d {
			callee = fs[i+1].name
			if fs[i+1].auth == "held" {
				args = "H, path"
			}
		}
		var body string
		call := func(h string) string { return callee + "(" + strings.ReplaceAll(args, "H", h) + ")" }
		switch f.link {
		case "direct":
			body = "\ttry " + call(host) + "\n"
		case "closure":
			body = "\tlet g = func() string fails Gone { try " + call(host) + " }\n\ttry g()\n"
		case "closure-held":
			body = "\tlet g = func(k Host) string fails Gone { try " + call("k") + " }\n\ttry g(" + host + ")\n"
		case "closure-ambient":
			body = "\tlet g = func() string fails Gone needs Ambient { try " + call(host) + " }\n\ttry g()\n"
		case "value":
			body = "\tlet g = " + callee + "\n\ttry g(" + strings.ReplaceAll(args, "H", host) + ")\n"
		}
		if f.auth == "inferred" {
			body = "\ttouch(NewHost())\n" + body
		}
		b.WriteString("\nfunc " + f.name + "(" + params + ") string fails Gone" + row + " {\n" + body + "}\n")
	}
	return b.String(), want, hits
}

// TestExternCallerRuleAgreesWithOracle is the property over call chains
// of depth 1 to 3: on every seed, the functions the rule reports are the
// ones the chain's oracle reports. A failing seed prints its source and
// both verdicts; ERGO_EXTERN_SEED=<n> replays one seed.
func TestExternCallerRuleAgreesWithOracle(t *testing.T) {
	outside, ds := check.CheckSource("let x = 1\n")
	if len(ds) != 0 {
		t.Fatalf("outside: %v", ds)
	}
	seeds := make([]uint64, 0, 600)
	if s := os.Getenv("ERGO_EXTERN_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for n := range uint64(600) {
			seeds = append(seeds, n+1)
		}
	}
	hits := map[string]int{}
	failed := 0
	for _, seed := range seeds {
		src, want, h := chainPackage(seed)
		for k, v := range h {
			hits[k] += v
		}
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		fake := checkFixture(t, "seed "+strconv.FormatUint(seed, 10), f, src)
		got := externCallerViolations([]*check.Package{fake}, heldTypes([]*check.Package{fake}, outside), hostExtern)
		if len(seeds) == 1 {
			t.Logf("seed %d: the rule reports %q, the oracle %q\n%s", seed, got, want, src)
		}
		if !slices.Equal(got, want) {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (ERGO_EXTERN_SEED=%d): the rule reports %q, the oracle %q\n%s", seed, seed, got, want, src)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d seeds disagree", failed, len(seeds))
	}
	if len(seeds) > 1 {
		var miss []string
		for d := 1; d <= 3; d++ {
			if hits["depth "+strconv.Itoa(d)] == 0 {
				miss = append(miss, "depth "+strconv.Itoa(d))
			}
		}
		for _, a := range chainAuths {
			if hits["auth "+a] == 0 {
				miss = append(miss, "auth "+a)
			}
		}
		for _, l := range chainLinks {
			if hits["link "+l] == 0 {
				miss = append(miss, "link "+l)
			}
		}
		if hits["verdict true"] == 0 || hits["verdict false"] == 0 {
			miss = append(miss, "a verdict")
		}
		if len(miss) > 0 {
			t.Errorf("the generator never reached %v", miss)
		}
	}
}

package rpcgen_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// cliSeedVariable replays one seed of TestCliArgsRoundTrip.
const cliSeedVariable = "ERGO_CLI_SEED"

// TestCliArgsRoundTrip is AP1's acceptance property: for generated
// contracts (records nested two deep, types defined over a basic, slices
// and Options of a basic, positional parameters, unit results and failure
// rows of zero to three records), the package ergo gen cli writes checks,
// and its law ArgsRoundTrip holds over the Requests the law's generator
// draws (100 cases a contract): Parse reads what Print writes as the same
// Request. An odd contract has its rpc package too, so its cli package
// names the rpc package's Request. For each operation, Run of the printed
// Request against a provider that answers a fixed value or failure is what
// Run's rule says: the value's JSON with --json, its text when it is a
// basic value, the failure's tagged JSON with status 1. Contract c0 always
// has an operation with a positional int, a record with a bool and a nested
// record, so a Print that writes a bool flag with no value, drops a nested
// field or reads a negative int as a flag is refuted on every seed, and an
// operation whose provider always fails. A
// failing seed prints the Ergo source, the test binary's output and the
// replay command.
//
// It builds Go because the law runs on values the native test runner
// generates, and Parse calls std strings and strconv, which check-time
// evaluation does not run ("a Go function, even a pure one").
func TestCliArgsRoundTrip(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv(cliSeedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", cliSeedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	cache := t.TempDir()
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { roundTrip(t, seed, cache) })
	}
}

// needsBuild marks a test that builds or runs a Go program: the fast tier
// (-short) skips it, the full tier runs it, as lower's needsBuild does.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

func roundTrip(t *testing.T, seed uint64, cache string) {
	p := newProgram(seed)
	dir := t.TempDir()
	files := p.files()
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestCliArgsRoundTrip$' ./internal/rpcgen", cliSeedVariable, seed)
	for _, c := range p.contracts {
		pkgDir := filepath.Join(dir, c.pkg())
		a, err := sketchfront.CheckDir(pkgDir)
		if err != nil || a.Root().Package == nil {
			t.Fatalf("seed %d: contract %s does not check: %v %v\n%s", seed, c.pkg(), err, a.Diagnostics, sources(files))
		}
		path := "loop/" + c.pkg()
		rpcPath := ""
		if c.withRPC {
			out, err := rpcgen.Generate(a.Root().Package, path, "Svc")
			if err != nil {
				t.Fatalf("seed %d: ergo gen rpc %s: %v\n%s", seed, c.pkg(), err, sources(files))
			}
			for name, src := range out.Files {
				writeFile(t, filepath.Join(pkgDir, out.Dir, name), src)
			}
			rpcPath = path + "/" + out.Dir
		}
		out, err := cligen.Generate(a.Root().Package, path, "Svc", rpcPath)
		if err != nil {
			t.Fatalf("seed %d: ergo gen cli %s: %v\n%s\n%s", seed, c.pkg(), err, sources(files), replay)
		}
		for name, src := range out.Files {
			writeFile(t, filepath.Join(pkgDir, out.Dir, name), src)
			files[c.pkg()+"/"+out.Dir+"/"+name] = src
		}
	}
	probe := filepath.Join(dir, "probe")
	a, err := sketchfront.CheckDir(probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		var ds []string
		for _, d := range a.Diagnostics {
			ds = append(ds, fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message))
		}
		t.Fatalf("seed %d: the generated packages or the probe do not check:\n%s\n%s\n%s", seed, strings.Join(ds, "\n"), sources(files), replay)
	}
	exe := filepath.Join(dir, "probe.test")
	var stderr bytes.Buffer
	if _, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    probe,
		Output:    exe,
		Cache:     build.Cache{Root: cache},
		Toolchain: build.HostToolchain(),
		Stderr:    &stderr,
	}); err != nil {
		t.Fatalf("seed %d: build: %v\n%s\n%s", seed, err, stderr.String(), sources(files))
	}
	o := runOnce(t, exe)
	t.Logf("seed %d: %d contracts, %d obligations; %s", seed, len(p.contracts), len(p.obligations()), strings.TrimSpace(o.stdout[strings.LastIndex(strings.TrimSpace(o.stdout), "\n")+1:]))
	var missing []string
	for _, name := range p.obligations() {
		if !hasLine(o.stdout, "ok "+name) {
			missing = append(missing, name)
		}
	}
	if o.code != 0 || len(missing) > 0 {
		t.Errorf("seed %d: the probe failed (exit %d); not passed: %s\n--- output\n%s%s\n%s\n%s", seed, o.code, strings.Join(missing, ", "), o.stdout, o.stderr, sources(files), replay)
	}
}

// hasLine reports a line of out that is want, or want and " (…)".
func hasLine(out, want string) bool {
	for _, l := range strings.Split(out, "\n") {
		if l == want || strings.HasPrefix(l, want+" (") {
			return true
		}
	}
	return false
}

func sources(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "--- %s\n%s", n, files[n])
	}
	return b.String()
}

func writeFile(t testing.TB, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

type outcome struct {
	stdout, stderr string
	code           int
}

// runOnce runs the program to its end under the memory bound.
func runOnce(t *testing.T, exe string, args ...string) outcome {
	t.Helper()
	n, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = proclimit.Run(cmd, n)
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s: %v\n%s", exe, err, stderr.String())
		}
		code = ee.ExitCode()
	}
	return outcome{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// The generated program.

// ty is a type of a generated contract.
type ty struct {
	kind string // string int int64 bool named slice option record
	// name is a defined type's or a record's name, in its contract's
	// package.
	name   string
	elem   *ty // a defined type's basic, a slice's or an Option's element
	fields []field
}

type field struct {
	name string
	t    *ty
}

// src is the type in source; q qualifies a contract's own type ("c0.").
func (t *ty) src(q string) string {
	switch t.kind {
	case "named", "record":
		return q + t.name
	case "slice":
		return "[]" + t.elem.src(q)
	case "option":
		return "Option[" + t.elem.src(q) + "]"
	case "jsonopt":
		return "json.Optional[" + t.elem.src(q) + "]"
	}
	return t.kind
}

// scalar reports a type with a text form: a basic or a type defined over
// one.
func (t *ty) scalar() bool {
	return t.kind != "slice" && t.kind != "option" && t.kind != "record" && t.kind != "jsonopt"
}

type op struct {
	name   string
	params []field
	result *ty // nil: unit
	fails  []*ty
	// fail is the failure the provider answers, an index of fails, or -1
	// for its value; answer is the value or failure, in the contract's
	// source and in the probe's.
	fail         int
	answer, want string
	args         []string // the Request's fields, in the probe's source
}

type contract struct {
	i       int
	withRPC bool
	records []*ty
	fails   []*ty
	ops     []*op
}

func (c *contract) pkg() string { return fmt.Sprintf("c%d", c.i) }

type program struct {
	seed      uint64
	r         *rand.Rand
	contracts []*contract
	named     []*ty
	// optional lets a field be a json.Optional of a string or an int
	// (TestMcpArgumentsRoundTrip: the CLI has no form for one).
	optional bool
}

var fieldNames = []string{"Name", "Count", "IsOn", "RetryAfterMs", "Tags", "Weights", "Extra", "Note", "UserID"}

// paramNames avoid the names ergo gen rpc's code uses for its locals.
var paramNames = []string{"key", "count", "label", "opts", "items", "flag", "userID"}

func newProgram(seed uint64) *program { return newProgramOf(seed, false) }

// newProgramOf is newProgram, with json.Optional fields when optional.
func newProgramOf(seed uint64, optional bool) *program {
	p := &program{seed: seed, r: rand.New(rand.NewPCG(seed, 0xc11)), optional: optional}
	for _, n := range [][2]string{{"Name", "string"}, {"Count", "int"}, {"Big", "int64"}, {"On", "bool"}} {
		p.named = append(p.named, &ty{kind: "named", name: n[0], elem: &ty{kind: n[1]}})
	}
	n := 1 + p.r.IntN(3)
	for i := 0; i < n; i++ {
		p.contracts = append(p.contracts, p.contract(i))
	}
	return p
}

func (p *program) basic() *ty {
	return &ty{kind: []string{"string", "int", "int64", "bool"}[p.r.IntN(4)]}
}

// scalarType is a basic or a type defined over one.
func (p *program) scalarType() *ty {
	if p.r.IntN(3) == 0 {
		n := p.named[p.r.IntN(len(p.named))]
		// ergo gen rpc's int walker converts a type defined over int to
		// int64 in one step, which the checker refuses (S108); every mcp
		// contract has an rpc package, so it draws no such type until the
		// rpc lane fixes the walker.
		if !p.optional || n.elem.kind != "int" {
			return n
		}
	}
	return p.basic()
}

// leaf is a field's type: a scalar, a slice or an Option of a basic, or a
// record declared before (records lists them).
func (p *program) leaf(records []*ty) *ty {
	switch k := p.r.IntN(8); {
	case k == 0:
		return &ty{kind: "slice", elem: &ty{kind: []string{"string", "int", "bool"}[p.r.IntN(3)]}}
	case k == 1:
		return &ty{kind: "option", elem: &ty{kind: []string{"string", "int"}[p.r.IntN(2)]}}
	case k == 2 && len(records) > 0:
		return records[p.r.IntN(len(records))]
	case k == 3 && p.optional:
		return &ty{kind: "jsonopt", elem: &ty{kind: []string{"string", "int"}[p.r.IntN(2)]}}
	}
	return p.scalarType()
}

func (p *program) fieldsOf(n int, records []*ty) []field {
	names := append([]string{}, fieldNames...)
	p.r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	var fs []field
	for i := 0; i < n; i++ {
		fs = append(fs, field{name: names[i], t: p.leaf(records)})
	}
	return fs
}

func (p *program) contract(i int) *contract {
	c := &contract{i: i, withRPC: i%2 == 1}
	if i == 0 {
		// The fixed shape every seed has: a bool flag, a nested record
		// and a positional int.
		leaf := &ty{kind: "record", name: "Leaf", fields: []field{{"On", &ty{kind: "bool"}}, {"Note", &ty{kind: "string"}}}}
		if p.optional {
			// A key that can be absent, every seed (ArgumentsFitTheSchema).
			leaf.fields = append(leaf.fields, field{"Extra", &ty{kind: "jsonopt", elem: &ty{kind: "string"}}})
		}
		deep := &ty{kind: "record", name: "Deep", fields: []field{{"Leaf", leaf}, {"Count", &ty{kind: "int"}}}}
		c.records = append(c.records, leaf, deep)
	}
	for k := 1 + p.r.IntN(3); k > 0; k-- {
		c.records = append(c.records, &ty{kind: "record", name: fmt.Sprintf("Rec%d", len(c.records)), fields: p.fieldsOf(p.r.IntN(4), c.records)})
	}
	for k := 0; k < 3; k++ {
		fs := p.fieldsOf(p.r.IntN(3), nil)
		for j := range fs {
			fs[j].t = p.basic()
		}
		c.fails = append(c.fails, &ty{kind: "record", name: fmt.Sprintf("Bad%d", k), fields: fs})
	}
	if i == 0 {
		c.ops = append(c.ops, &op{name: "Fixed", params: []field{{"label", &ty{kind: "string"}}, {"opts", c.records[1]}, {"count", &ty{kind: "int"}}}, result: &ty{kind: "int"}})
		// An operation whose provider always fails: Run's failure rule.
		c.ops = append(c.ops, &op{name: "Refuse", params: []field{{"key", &ty{kind: "string"}}}, fails: []*ty{c.fails[0]}})
		if p.optional {
			// An operation whose only parameter is a record: its fields are
			// the tool's arguments.
			c.ops = append(c.ops, &op{name: "Flat", params: []field{{"opts", c.records[1]}}, result: &ty{kind: "int"}})
		}
	}
	for k := 1 + p.r.IntN(3); k > 0; k-- {
		o := &op{name: fmt.Sprintf("C%dOp%d", i, len(c.ops))}
		names := append([]string{}, paramNames...)
		p.r.Shuffle(len(names), func(a, b int) { names[a], names[b] = names[b], names[a] })
		for j := p.r.IntN(4); j > 0; j-- {
			var t *ty
			if p.r.IntN(2) == 0 {
				t = c.records[p.r.IntN(len(c.records))]
			} else {
				t = p.scalarType()
			}
			o.params = append(o.params, field{name: names[j], t: t})
		}
		if p.r.IntN(4) > 0 {
			o.result = p.leaf(c.records)
		}
		perm := p.r.Perm(3)
		for _, f := range perm[:p.r.IntN(4)] {
			o.fails = append(o.fails, c.fails[f])
		}
		c.ops = append(c.ops, o)
	}
	q := c.pkg() + "."
	for _, o := range c.ops {
		o.fail = -1
		if o.name == "Refuse" || len(o.fails) > 0 && p.r.IntN(2) == 0 {
			o.fail = p.r.IntN(len(o.fails))
		}
		switch {
		case o.fail >= 0:
			o.answer, o.want = p.pair(o.fails[o.fail], q)
		case o.result != nil:
			o.answer, o.want = p.pair(o.result, q)
		}
		for _, prm := range o.params {
			o.args = append(o.args, fieldName(prm.name)+": "+p.lit(prm.t, q, 0))
		}
	}
	return c
}

func fieldName(param string) string { return strings.ToUpper(param[:1]) + param[1:] }

// pair is one value of t in the contract's source and in the probe's: a
// draw with a fixed seed of its own, so both texts are one value.
func (p *program) pair(t *ty, q string) (string, string) {
	s := p.r.Uint64()
	in := (&program{r: rand.New(rand.NewPCG(s, 1))}).lit(t, "", 0)
	out := (&program{r: rand.New(rand.NewPCG(s, 1))}).lit(t, q, 0)
	return in, out
}

// stringPool holds texts a command line could misread: a flag, "--", an
// "=", a help request, an empty text.
var stringPool = []string{"", "a", "key-1", "-x", "--", "-h", "--help", "a=b", "=", `q"uote`, "tab\there", "line\nbreak", "é", "日本"}

// lit is a value of t in source; q qualifies the contract's types.
func (p *program) lit(t *ty, q string, depth int) string {
	switch t.kind {
	case "int", "int64":
		return []string{"0", "1", "-1", "-42", "9007199254740993", "-9223372036854775807", strconv.Itoa(p.r.IntN(1000000) - 500000)}[p.r.IntN(7)]
	case "string":
		return strconv.Quote(stringPool[p.r.IntN(len(stringPool))])
	case "bool":
		return []string{"true", "false"}[p.r.IntN(2)]
	case "named":
		return p.lit(t.elem, q, depth)
	case "slice":
		var es []string
		for k := p.r.IntN(3); k > 0; k-- {
			es = append(es, p.lit(t.elem, q, depth+1))
		}
		return t.src(q) + "{" + strings.Join(es, ", ") + "}"
	case "option":
		if p.r.IntN(2) == 0 {
			return "None"
		}
		return "Some[" + t.elem.src(q) + "]{Value: " + p.lit(t.elem, q, depth+1) + "}"
	case "jsonopt":
		if p.r.IntN(2) == 0 {
			return "json.Absent"
		}
		return "json.Present[" + t.elem.src(q) + "]{Value: " + p.lit(t.elem, q, depth+1) + "}"
	case "record":
		if len(t.fields) == 0 {
			return q + t.name
		}
		var fs []string
		for _, f := range t.fields {
			fs = append(fs, f.name+": "+p.lit(f.t, q, depth+1))
		}
		return q + t.name + "{" + strings.Join(fs, ", ") + "}"
	}
	panic(t.kind)
}

func declFields(fs []field) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.name+" "+f.t.src(""))
	}
	return strings.Join(parts, ", ")
}

// files are the module's sources, by path: each contract's package (its
// types, its operation type Svc and the provider Ref) and the probe.
func (p *program) files() map[string]string {
	out := map[string]string{"ergo.mod": "module loop\n"}
	for _, c := range p.contracts {
		out[c.pkg()+"/contract.ergo"] = p.contractSource(c)
	}
	out["probe/probe.ergo"] = p.probeSource()
	return out
}

func sig(o *op, first string) string {
	params := []string{}
	if first != "" {
		params = append(params, first)
	}
	for _, prm := range o.params {
		params = append(params, prm.name+" "+prm.t.src(""))
	}
	s := o.name + "(" + strings.Join(params, ", ") + ")"
	if o.result != nil {
		s += " " + o.result.src("")
	}
	if len(o.fails) > 0 {
		var fs []string
		for _, f := range o.fails {
			fs = append(fs, f.src(""))
		}
		s += " fails " + strings.Join(fs, " | ")
	}
	return s
}

func (p *program) contractSource(c *contract) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Package %s is a generated contract (seed %d) and a provider that\n// answers each operation with a fixed value or failure.\n\n", c.pkg(), p.seed)
	for _, n := range p.named {
		fmt.Fprintf(&b, "type %s %s\n", n.name, n.elem.kind)
	}
	for _, r := range c.records {
		fmt.Fprintf(&b, "type %s { %s }\n", r.name, declFields(r.fields))
	}
	for _, f := range c.fails {
		fmt.Fprintf(&b, "type %s { %s }\n", f.name, declFields(f.fields))
	}
	b.WriteString("\ntype Svc {\n")
	for _, o := range c.ops {
		fmt.Fprintf(&b, "\t%s,\n", sig(o, ""))
	}
	b.WriteString("}\n\ntype Ref {}\n")
	for _, o := range c.ops {
		fmt.Fprintf(&b, "\nfunc %s {\n", sig(o, "r Ref"))
		switch {
		case o.fail >= 0:
			fmt.Fprintf(&b, "\treturn %s\n", o.answer)
		case o.result != nil:
			fmt.Fprintf(&b, "\t%s\n", o.answer)
		}
		b.WriteString("}\n")
	}
	out := b.String()
	if strings.Contains(out, "json.") {
		head, rest, _ := strings.Cut(out, "\n\n")
		out = head + "\n\nimport json\n\n" + rest
	}
	return out
}

// probeSource is the probe package: per contract, a law that runs the cli
// package's RoundTrips, and per operation two tests of Run against Ref,
// with --json and without.
func (p *program) probeSource() string {
	var b strings.Builder
	b.WriteString("// Package probe runs the generated cli packages' round trip and Run.\n\nimport rpc\n")
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/%s\nimport loop/%s/%scli\n", c.pkg(), c.pkg(), c.pkg())
		if c.withRPC {
			fmt.Fprintf(&b, "import loop/%s/%srpc\n", c.pkg(), c.pkg())
		}
	}
	for _, c := range p.contracts {
		cli := c.pkg() + "cli"
		q := c.pkg() + "."
		fmt.Fprintf(&b, "\nlaw C%dArgsRoundTrip(r %s.Request) { %s.RoundTrips(r) }\n", c.i, cli, cli)
		variants := cli + "."
		if c.withRPC {
			variants = c.pkg() + "rpc."
		}
		for _, o := range c.ops {
			req := variants + o.name + "Request"
			if len(o.args) > 0 {
				req += "{" + strings.Join(o.args, ", ") + "}"
			}
			var want, text string
			switch {
			case o.fail >= 0:
				f := o.fails[o.fail]
				want = fmt.Sprintf("let want %s = %s\n\tlet tagged = rpc.Tagged(%q, rpc.Encode(want))\n", f.src(q), o.want, f.name)
				text = fmt.Sprintf("%s.Output{Out: \"\", Err: %q + tagged + \"\\n\", Code: 1}", cli, f.name+": ")
				want += fmt.Sprintf("\tlet json = %s.Output{Out: tagged + \"\\n\", Err: \"\", Code: 1}\n", cli)
			case o.result == nil:
				want = fmt.Sprintf("let json = %s.Output{Out: \"null\\n\", Err: \"\", Code: 0}\n", cli)
				text = fmt.Sprintf("%s.Output{Out: \"\", Err: \"\", Code: 0}", cli)
			default:
				want = fmt.Sprintf("let want %s = %s\n\tlet json = %s.Output{Out: rpc.Encode(want) + \"\\n\", Err: \"\", Code: 0}\n", o.result.src(q), o.want, cli)
				switch {
				case !o.result.scalar():
					text = fmt.Sprintf("%s.Output{Out: rpc.Encode(want) + \"\\n\", Err: \"\", Code: 0}", cli)
				case o.want == `""`:
					text = fmt.Sprintf("%s.Output{Out: \"\", Err: \"\", Code: 0}", cli)
				default:
					text = fmt.Sprintf("%s.Output{Out: \"\\(want)\\n\", Err: \"\", Code: 0}", cli)
				}
			}
			fmt.Fprintf(&b, "\ntest C%d%sRuns {\n\tlet r %s.Request = %s\n\t%s", c.i, o.name, cli, req, want)
			fmt.Fprintf(&b, "\tlet mut args = []string{\"--json\"}\n\tfor a in %s.Print(r) {\n\t\targs = args.Append(a)\n\t}\n", cli)
			fmt.Fprintf(&b, "\tlet (j, t) = with context { %sSvc: %sRef } { (%s.Run(args), %s.Run(%s.Print(r))) }\n", q, q, cli, cli, cli)
			fmt.Fprintf(&b, "\tassert j == json\n\tassert t == %s\n}\n", text)
		}
	}
	return b.String()
}

// obligations are the probe's laws and tests, by the names the test
// runner prints.
func (p *program) obligations() []string {
	var out []string
	for _, c := range p.contracts {
		out = append(out, fmt.Sprintf("law C%dArgsRoundTrip", c.i))
		for _, o := range c.ops {
			out = append(out, fmt.Sprintf("C%d%sRuns", c.i, o.name))
		}
	}
	return out
}

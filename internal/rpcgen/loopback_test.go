package rpcgen

import (
	"bufio"
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

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// seedVariable replays one seed of TestRpcLoopbackAgrees.
const seedVariable = "ERGO_RPC_SEED"

// TestRpcLoopbackAgrees is R1's acceptance property: for generated
// contracts (records, a union, Option, slices, Maps, unit results and
// failure rows of zero to three records), a generated call sequence runs
// once against the reference provider directly and once through the
// generated client, a loopback HTTP server and the generated Handle to the
// same provider. Every outcome (value, failure variant and payload), the
// arguments each provider received, in order (its log), and a final
// defect agree; a final int beyond +-(2^53-1), in the arguments or the
// value, runs directly and is refused on the wire with the Unrepresentable
// defect (the int rule). The default seeds 1, 2 and 3 end in a defect, a
// wide int and neither. A failing seed prints the Ergo source, both
// outputs and the replay command.
//
// It builds Go because the wire is runtime behaviour: the HTTP server, the
// task per call and the client run only in a built program; go/types on
// the lowered Go says only that it compiles.
func TestRpcLoopbackAgrees(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv(seedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { loopback(t, seed) })
	}
}

func loopback(t *testing.T, seed uint64) {
	p := newProgram(seed)
	t.Logf("seed %d: %d contracts, %d calls, ends in a defect %v, a wide int %v", seed, len(p.contracts), len(p.order), p.dies, p.wide)
	dir := t.TempDir()
	files := p.files()
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	for i := range p.contracts {
		pkgDir := filepath.Join(dir, fmt.Sprintf("c%d", i))
		a, err := sketchfront.CheckDir(pkgDir)
		if err != nil || a.Root().Package == nil {
			t.Fatalf("seed %d: contract c%d does not check: %v %v\n%s", seed, i, err, a.Diagnostics, files[fmt.Sprintf("c%d/contract.ergo", i)])
		}
		out, err := Generate(a.Root().Package, fmt.Sprintf("loop/c%d", i), "Svc", fmt.Sprintf("c%d.Svc", i))
		if err != nil {
			t.Fatalf("seed %d: generate c%d: %v", seed, i, err)
		}
		for name, src := range out.Files {
			writeFile(t, filepath.Join(pkgDir, out.Dir, name), src)
			files[fmt.Sprintf("c%d/%s/%s", i, out.Dir, name)] = src
		}
	}
	exe := buildProgram(t, seed, dir, files)
	direct := runOnce(t, exe, "direct")
	srv := startServer(t, exe)
	viaRPC := runOnce(t, exe, "http://"+srv.addr+"/rpc")
	srv.stop(t)
	agree := direct.stdout == viaRPC.stdout && direct.code == viaRPC.code
	switch {
	case p.dies:
		// Both end in the provider's defect.
		agree = agree && strings.Contains(direct.stderr, p.boom()) && strings.Contains(viaRPC.stderr, p.boom())
	case p.wide:
		// The direct run carries the int; the wire refuses it: the
		// client's output is the direct run's but its last line, and it
		// ends in the Unrepresentable defect.
		cut := strings.LastIndex(strings.TrimSuffix(direct.stdout, "\n"), "\n") + 1
		agree = direct.code == 0 && viaRPC.code == 2 && viaRPC.stdout == direct.stdout[:cut] && strings.Contains(viaRPC.stderr, "Unrepresentable")
	}
	if !agree {
		t.Errorf("seed %d: the call sequence differs through the wire\n%s\n--- direct (exit %d)\n%s%s--- through the client and the server (exit %d)\n%s%s--- server stderr\n%s\nreplay: %s=%d go test -count=1 -run '^TestRpcLoopbackAgrees$' ./internal/rpcgen",
			seed, sources(files), direct.code, direct.stdout, direct.stderr, viaRPC.code, viaRPC.stdout, viaRPC.stderr, srv.stderr.String(), seedVariable, seed)
	}
	if !strings.Contains(direct.stdout, "is ") && !strings.Contains(direct.stdout, "fails ") {
		t.Errorf("seed %d: the sequence made no call:\n%s", seed, direct.stdout)
	}
}

func sources(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		if strings.HasSuffix(n, ".ergo") && !strings.Contains(n, "rpc/") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "--- %s\n%s", n, files[n])
	}
	return b.String()
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildProgram checks and links cmd/loop into one binary: the direct run,
// the server and the client are its three modes.
func buildProgram(t *testing.T, seed uint64, dir string, files map[string]string) string {
	t.Helper()
	main := filepath.Join(dir, "cmd", "loop")
	a, err := sketchfront.CheckDir(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		var ds []string
		for _, d := range a.Diagnostics {
			ds = append(ds, fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message))
		}
		t.Fatalf("seed %d: the program does not check:\n%s\n%s", seed, strings.Join(ds, "\n"), sourcesAll(files))
	}
	exe := filepath.Join(dir, "loop.exe")
	var stderr bytes.Buffer
	if _, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    main,
		Output:    exe,
		Cache:     build.Cache{Root: filepath.Join(dir, "cache")},
		Toolchain: build.HostToolchain(),
		Stderr:    &stderr,
	}); err != nil {
		t.Fatalf("seed %d: build: %v\n%s", seed, err, stderr.String())
	}
	return exe
}

func sourcesAll(files map[string]string) string {
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

type outcome struct {
	stdout, stderr string
	code           int
}

func limit(t *testing.T) uint64 {
	t.Helper()
	n, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// runOnce runs the program to its end under the memory bound.
func runOnce(t *testing.T, exe string, arg string) outcome {
	t.Helper()
	cmd := exec.Command(exe, arg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := proclimit.Run(cmd, limit(t))
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s %s: %v\n%s", exe, arg, err, stderr.String())
		}
		code = ee.ExitCode()
	}
	return outcome{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

type server struct {
	cmd    *exec.Cmd
	addr   string
	stderr bytes.Buffer
}

// startServer starts the program's server and waits for its first line,
// "listening ADDR".
func startServer(t *testing.T, exe string) *server {
	t.Helper()
	s := &server{cmd: exec.Command(exe, "serve")}
	out, err := s.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s.cmd.Stderr = &s.stderr
	if err := proclimit.Start(s.cmd, limit(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.cmd.ProcessState == nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "listening ")
	if err != nil || !ok {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		t.Fatalf("the server did not say where it listens: %q %v\n%s", line, err, s.stderr.String())
	}
	s.addr = addr
	return s
}

// stop interrupts the server, as a terminal's ^C does, and waits for it.
func (s *server) stop(t *testing.T) {
	t.Helper()
	_ = s.cmd.Process.Signal(os.Interrupt)
	err := s.cmd.Wait()
	if _, ok := err.(*exec.ExitError); !ok {
		t.Errorf("the server's exit after an interrupt: %v\n%s", err, s.stderr.String())
	}
}

// The generated program.

// ty is a type of a generated contract.
type ty struct {
	kind string // int string bool float64 slice map option record union
	elem *ty
	// name is a record's or a union's name, in its contract's package.
	name     string
	fields   []field
	variants []*ty // a union's: records
}

type field struct {
	name string
	t    *ty
}

// src is the type in source; q qualifies a contract's own type ("c0.").
func (t *ty) src(q string) string {
	switch t.kind {
	case "slice":
		return "[]" + t.elem.src(q)
	case "map":
		return "Map[string, " + t.elem.src(q) + "]"
	case "option":
		return "Option[" + t.elem.src(q) + "]"
	case "record", "union":
		return q + t.name
	}
	return t.kind
}

type op struct {
	name   string
	params []field
	result *ty // nil: unit
	fails  []*ty
}

// call is one call of the sequence: its operation, arguments, and the
// outcome the provider gives: -1 a value, i the failure fails[i], -2 a
// defect.
type call struct {
	op      *op
	args    []string // in the main package's source
	outcome int
	value   string // the provider's value or failure, in the contract's source
	n       int    // the provider's count when it is called
}

type contract struct {
	i       int
	records []*ty
	union   *ty
	fails   []*ty
	ops     []*op
	calls   []*call
}

type program struct {
	seed      uint64
	r         *rand.Rand
	contracts []*contract
	order     []*call // the calls in the order main makes them
	dies      bool
	// wide: the last call carries an int the wire refuses.
	wide bool
}

var fieldNames = []string{"Name", "Count", "IsOn", "RetryAfterMs", "Tags", "Weights", "Extra", "Note"}
var paramNames = []string{"key", "count", "label", "opts", "items", "flag"}

func newProgram(seed uint64) *program {
	p := &program{seed: seed, r: rand.New(rand.NewPCG(seed, 0x5eed))}
	n := 1 + p.r.IntN(3)
	for i := 0; i < n; i++ {
		p.contracts = append(p.contracts, p.contract(i))
	}
	// The seed picks how the sequence ends, so any three seeds in a row
	// reach each ending: seed%3 == 1 a defect, 2 a wide int, 0 neither.
	// A wide int needs an operation of the last contract that reaches an
	// int; one is added when none does.
	last := p.contracts[len(p.contracts)-1]
	if seed%3 == 2 && p.wideOp(last) == nil {
		last.ops = append(last.ops, &op{name: fmt.Sprintf("C%dOp%d", last.i, len(last.ops)), params: []field{{name: "count", t: &ty{kind: "int"}}}, result: &ty{kind: "int"}})
	}
	// The calls, contract by contract, each operation one to three times.
	for _, c := range p.contracts {
		for _, o := range c.ops {
			for k := 1 + p.r.IntN(3); k > 0; k-- {
				cl := &call{op: o, n: len(c.calls), outcome: -1}
				for _, prm := range o.params {
					cl.args = append(cl.args, p.lit(prm.t, fmt.Sprintf("c%d.", c.i), 0))
				}
				if len(o.fails) > 0 && p.r.IntN(2) == 0 {
					cl.outcome = p.r.IntN(len(o.fails))
					cl.value = p.lit(o.fails[cl.outcome], "", 0)
				} else if o.result != nil {
					cl.value = p.lit(o.result, "", 0)
				}
				c.calls = append(c.calls, cl)
				p.order = append(p.order, cl)
			}
		}
	}
	// The last call may end the program: its provider raises a defect, or
	// an int beyond +-(2^53-1) is in its arguments or its value, which the
	// wire refuses (the direct run carries it).
	o := last.ops[p.r.IntN(len(last.ops))]
	if seed%3 == 2 {
		o = p.wideOp(last)
	}
	cl := &call{op: o, n: len(last.calls), outcome: -1}
	q := fmt.Sprintf("c%d.", last.i)
	for _, prm := range o.params {
		cl.args = append(cl.args, p.lit(prm.t, q, 0))
	}
	switch seed % 3 {
	case 1:
		cl.outcome = -2
		p.dies = true
	case 2:
		for i, prm := range o.params {
			if s, ok := p.wideLit(prm.t, q); ok && !p.wide {
				cl.args[i] = s
				p.wide = true
			}
		}
		if o.result != nil {
			if s, ok := p.wideLit(o.result, ""); ok && !p.wide {
				cl.value = s
				p.wide = true
			}
		}
	}
	if p.dies || p.wide {
		if cl.outcome == -1 && o.result != nil && cl.value == "" {
			cl.value = p.lit(o.result, "", 0)
		}
		last.calls = append(last.calls, cl)
		p.order = append(p.order, cl)
	}
	return p
}

// wideOp is an operation of c whose parameters or value reach an int.
func (p *program) wideOp(c *contract) *op {
	for _, o := range c.ops {
		for _, prm := range o.params {
			if reachesInt(prm.t) {
				return o
			}
		}
		if o.result != nil && reachesInt(o.result) {
			return o
		}
	}
	return nil
}

func reachesInt(t *ty) bool {
	switch t.kind {
	case "int":
		return true
	case "slice", "map", "option":
		return reachesInt(t.elem)
	case "record":
		for _, f := range t.fields {
			if reachesInt(f.t) {
				return true
			}
		}
	case "union":
		for _, v := range t.variants {
			if reachesInt(v) {
				return true
			}
		}
	}
	return false
}

// wideLit is a value of t that holds one int beyond +-(2^53-1), when t
// reaches an int.
func (p *program) wideLit(t *ty, q string) (string, bool) {
	switch t.kind {
	case "int":
		return []string{"9007199254740992", "-9007199254740993", "9223372036854775807"}[p.r.IntN(3)], true
	case "slice":
		if s, ok := p.wideLit(t.elem, q); ok {
			return t.src(q) + "{" + s + "}", true
		}
	case "map":
		if s, ok := p.wideLit(t.elem, q); ok {
			return t.src(q) + `{"w": ` + s + "}", true
		}
	case "option":
		if s, ok := p.wideLit(t.elem, q); ok {
			return "Some[" + t.elem.src(q) + "]{Value: " + s + "}", true
		}
	case "record":
		for i, f := range t.fields {
			if s, ok := p.wideLit(f.t, q); ok {
				var fs []string
				for j, g := range t.fields {
					v := s
					if j != i {
						v = p.lit(g.t, q, 1)
					}
					fs = append(fs, g.name+": "+v)
				}
				return q + t.name + "{" + strings.Join(fs, ", ") + "}", true
			}
		}
	case "union":
		for _, v := range t.variants {
			if s, ok := p.wideLit(v, q); ok {
				return s, true
			}
		}
	}
	return "", false
}

func (p *program) boom() string { return fmt.Sprintf("boom %d", p.seed) }

func (p *program) basic() *ty {
	return &ty{kind: []string{"int", "string", "bool", "float64"}[p.r.IntN(4)]}
}

// anyType is a type drawn from the contract's: a basic, a record, the
// union, or a slice, Map or Option over one.
func (p *program) anyType(c *contract, depth int) *ty {
	switch k := p.r.IntN(8); {
	case k < 2 || depth > 1:
		return p.basic()
	case k == 2 && len(c.records) > 0:
		return c.records[p.r.IntN(len(c.records))]
	case k == 3 && c.union != nil:
		return c.union
	case k == 4:
		return &ty{kind: "slice", elem: p.anyType(c, depth+1)}
	case k == 5:
		return &ty{kind: "map", elem: p.anyType(c, depth+1)}
	case k == 6:
		return &ty{kind: "option", elem: p.anyType(c, depth+1)}
	}
	return p.basic()
}

func (p *program) fieldsOf(c *contract, n int) []field {
	names := append([]string{}, fieldNames...)
	p.r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	var fs []field
	for i := 0; i < n; i++ {
		fs = append(fs, field{name: names[i], t: p.anyType(c, 1)})
	}
	return fs
}

func (p *program) contract(i int) *contract {
	c := &contract{i: i}
	for k := p.r.IntN(3); k > 0; k-- {
		c.records = append(c.records, &ty{kind: "record", name: fmt.Sprintf("Rec%d", len(c.records)), fields: p.fieldsOf(c, p.r.IntN(3))})
	}
	u := &ty{kind: "union", name: "Shape"}
	for k, name := range []string{"Dot", "Line", "Box"}[:1+p.r.IntN(3)] {
		u.variants = append(u.variants, &ty{kind: "record", name: name, fields: p.fieldsOf(c, k)})
	}
	c.union = u
	// A failure's fields are basics, slices and Options of them: an error
	// type with a Map or a record field has no derived text yet (#49), so
	// it does not lower.
	for k := 0; k < 3; k++ {
		fs := p.fieldsOf(c, p.r.IntN(3))
		for i := range fs {
			switch k := p.r.IntN(4); k {
			case 0:
				fs[i].t = &ty{kind: "slice", elem: p.basic()}
			case 1:
				fs[i].t = &ty{kind: "option", elem: p.basic()}
			default:
				fs[i].t = p.basic()
			}
		}
		c.fails = append(c.fails, &ty{kind: "record", name: fmt.Sprintf("Bad%d", k), fields: fs})
	}
	for k := 1 + p.r.IntN(3); k > 0; k-- {
		o := &op{name: fmt.Sprintf("C%dOp%d", i, len(c.ops))}
		names := append([]string{}, paramNames...)
		p.r.Shuffle(len(names), func(a, b int) { names[a], names[b] = names[b], names[a] })
		for j := p.r.IntN(3); j > 0; j-- {
			o.params = append(o.params, field{name: names[j], t: p.anyType(c, 0)})
		}
		if p.r.IntN(4) > 0 {
			o.result = p.anyType(c, 0)
		}
		perm := p.r.Perm(3)
		for _, f := range perm[:p.r.IntN(4)] {
			o.fails = append(o.fails, c.fails[f])
		}
		c.ops = append(c.ops, o)
	}
	return c
}

var stringPool = []string{"", "a", "key-1", `q"uote`, `back\slash`, "tab\there", "line\nbreak", "é", "日本", "<&>", "\u2028"}

// lit is a value of t in source; q qualifies the contract's types.
func (p *program) lit(t *ty, q string, depth int) string {
	switch t.kind {
	case "int":
		return []string{"0", "1", "-1", "42", "9007199254740991", "-9007199254740991", strconv.Itoa(p.r.IntN(1000000) - 500000)}[p.r.IntN(7)]
	case "string":
		return strconv.Quote(stringPool[p.r.IntN(len(stringPool))])
	case "bool":
		return []string{"true", "false"}[p.r.IntN(2)]
	case "float64":
		return []string{"0.0", "1.5", "-0.25", "1000000.0", "3.0e-7"}[p.r.IntN(5)]
	case "slice":
		var es []string
		for k := p.r.IntN(3); k > 0 && depth < 3; k-- {
			es = append(es, p.lit(t.elem, q, depth+1))
		}
		return t.src(q) + "{" + strings.Join(es, ", ") + "}"
	case "map":
		var es []string
		for k := 0; k < p.r.IntN(3) && depth < 3; k++ {
			es = append(es, fmt.Sprintf("%q: %s", fmt.Sprintf("k%d", k), p.lit(t.elem, q, depth+1)))
		}
		return t.src(q) + "{" + strings.Join(es, ", ") + "}"
	case "option":
		if p.r.IntN(2) == 0 || depth >= 3 {
			return "None"
		}
		return "Some[" + t.elem.src(q) + "]{Value: " + p.lit(t.elem, q, depth+1) + "}"
	case "record":
		return p.record(t, q, depth)
	case "union":
		return p.record(t.variants[p.r.IntN(len(t.variants))], q, depth)
	}
	panic(t.kind)
}

func (p *program) record(t *ty, q string, depth int) string {
	if len(t.fields) == 0 {
		return q + t.name
	}
	var fs []string
	for _, f := range t.fields {
		fs = append(fs, f.name+": "+p.lit(f.t, q, depth+1))
	}
	return q + t.name + "{" + strings.Join(fs, ", ") + "}"
}

func declFields(fs []field) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.name+" "+f.t.src(""))
	}
	return strings.Join(parts, ", ")
}

// files are the module's sources, by path: each contract's package (its
// types, its operation type Svc and the reference provider Ref) and
// cmd/loop.
func (p *program) files() map[string]string {
	out := map[string]string{"ergo.mod": "module loop\n"}
	for _, c := range p.contracts {
		out[fmt.Sprintf("c%d/contract.ergo", c.i)] = p.contractSource(c)
	}
	out["cmd/loop/main.ergo"] = p.mainSource()
	return out
}

func (p *program) contractSource(c *contract) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Package c%d is a generated RPC contract (seed %d) and its reference\n// provider.\n\nimport json\nimport sync\n\n", c.i, p.seed)
	for _, r := range c.records {
		fmt.Fprintf(&b, "type %s { %s }\n", r.name, declFields(r.fields))
	}
	var vs []string
	for _, v := range c.union.variants {
		vs = append(vs, v.name+"{"+declFields(v.fields)+"}")
	}
	fmt.Fprintf(&b, "type Shape = %s\n", strings.Join(vs, " | "))
	for _, f := range c.fails {
		fmt.Fprintf(&b, "type %s { %s }\n", f.name, declFields(f.fields))
	}
	b.WriteString("\ntype Svc {\n")
	for _, o := range c.ops {
		fmt.Fprintf(&b, "\t%s,\n", p.sig(c, o, ""))
	}
	fmt.Fprintf(&b, "\tC%dLog() []string,\n}\n\n", c.i)
	b.WriteString(`// Ref is the reference provider: it notes each call's arguments and
// gives the outcome the sequence planned for its count.
type Ref { log sync.Ref[[]string], n sync.Ref[int] }

func NewRef() Ref { Ref{log: sync.NewRef[[]string]([]string{}), n: sync.NewRef[int](0)} }

func enc[T wire](v T) string { json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, v) }

func next(r Ref) int { sync.Modify(r.n, func(c int) (int, int) { (c, c + 1) }) }

func note(r Ref, s string) { r.log.Update(func(l []string) []string { l.Append(s) }) }

`)
	fmt.Fprintf(&b, "func C%dLog(r Ref) []string { r.log.Get() }\n", c.i)
	for _, o := range c.ops {
		fmt.Fprintf(&b, "\nfunc %s {\n", p.providerSig(o))
		note := fmt.Sprintf("%q", o.name)
		for _, prm := range o.params {
			note += " + \" \" + enc(" + prm.name + ")"
		}
		fmt.Fprintf(&b, "\tnote(r, %s)\n\tmatch next(r) {\n", note)
		for _, cl := range c.calls {
			if cl.op != o {
				continue
			}
			fmt.Fprintf(&b, "\tcase %d:\n", cl.n)
			switch {
			case cl.outcome == -2:
				fmt.Fprintf(&b, "\t\tUnreachable(%q)\n", p.boom())
			case cl.outcome >= 0:
				fmt.Fprintf(&b, "\t\treturn %s\n", cl.value)
			case o.result != nil:
				fmt.Fprintf(&b, "\t\t%s\n", cl.value)
			}
		}
		b.WriteString("\tcase _:\n\t\tUnreachable(\"a call the sequence did not plan\")\n\t}\n}\n")
	}
	return b.String()
}

// sig is o's signature as an operation of Svc.
func (p *program) sig(c *contract, o *op, q string) string {
	var params []string
	for _, prm := range o.params {
		params = append(params, prm.name+" "+prm.t.src(q))
	}
	s := o.name + "(" + strings.Join(params, ", ") + ")"
	if o.result != nil {
		s += " " + o.result.src(q)
	}
	if len(o.fails) > 0 {
		var fs []string
		for _, f := range o.fails {
			fs = append(fs, f.src(q))
		}
		s += " fails " + strings.Join(fs, " | ")
	}
	return s
}

// providerSig is the provider function's signature: Ref first.
func (p *program) providerSig(o *op) string {
	params := []string{"r Ref"}
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

func (p *program) mainSource() string {
	var b strings.Builder
	b.WriteString("import json\nimport os\nimport rpc\nimport strings\n")
	var caps []string
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/c%d\nimport loop/c%d/c%drpc\n", c.i, c.i, c.i)
		caps = append(caps, fmt.Sprintf("c%d.Svc", c.i))
	}
	needs := strings.Join(caps, " | ")
	b.WriteString("\nfunc enc[T wire](v T) string { json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, v) }\n")
	for k, cl := range p.order {
		c := p.contractOf(cl)
		q := fmt.Sprintf("c%d.", c.i)
		fmt.Fprintf(&b, "\nfunc call%d() string needs %sSvc {\n\t", k, q)
		invoke := fmt.Sprintf("%sSvc.%s(%s)", q, cl.op.name, strings.Join(cl.args, ", "))
		if cl.op.result != nil {
			b.WriteString("let v = ")
		}
		b.WriteString(invoke)
		if len(cl.op.fails) > 0 {
			b.WriteString(" catch {\n")
			for _, f := range cl.op.fails {
				fmt.Fprintf(&b, "\tcase %s%s as e:\n\t\treturn \"%s fails %s \" + enc(e)\n", q, f.name, cl.op.name, f.name)
			}
			b.WriteString("\t}")
		}
		if cl.op.result != nil {
			fmt.Fprintf(&b, "\n\t\"%s is \" + enc(v)\n}\n", cl.op.name)
		} else {
			fmt.Fprintf(&b, "\n\t\"%s is unit\"\n}\n", cl.op.name)
		}
	}
	fmt.Fprintf(&b, "\nfunc script() needs Console | %s {\n", needs)
	last := len(p.order)
	if p.dies || p.wide {
		last--
	}
	for k := 0; k < last; k++ {
		fmt.Fprintf(&b, "\tConsole.Println(call%d())\n", k)
	}
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "\tfor l in c%d.Svc.C%dLog() {\n\t\tConsole.Println(l)\n\t}\n", c.i, c.i)
	}
	if p.dies || p.wide {
		fmt.Fprintf(&b, "\tConsole.Println(call%d())\n", last)
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "\nfunc handle(tag string, payload string) string fails rpc.Failed needs %s {\n", needs)
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "\tif strings.HasPrefix(tag, \"C%d\") {\n\t\tlet s = try c%drpc.Handle(tag, payload)\n\t\treturn s\n\t}\n", c.i, c.i)
	}
	b.WriteString("\trpc.Die[string](\"RpcError\", \"no contract has \\(tag)\")\n}\n")
	var refs, clients []string
	for _, c := range p.contracts {
		refs = append(refs, fmt.Sprintf("c%d.Svc: c%d.NewRef()", c.i, c.i))
		clients = append(clients, fmt.Sprintf("c%d.Svc: c%drpc.NewClient(conn)", c.i, c.i))
	}
	fmt.Fprintf(&b, `
func main() fails rpc.ListenFailed needs Console | Ambient {
	let mode = os.NewHost().Args()[0]
	match mode {
	case "serve":
		let l = try rpc.Listen("127.0.0.1:0")
		Console.Println("listening \(l.Addr())")
		with context { %s } {
			rpc.Serve(l, "/rpc", handle)
		}
	case "direct":
		with context { %s } {
			script()
		}
	case _:
		let conn = rpc.Dial(mode)
		with context { %s } {
			script()
		}
	}
}
`, strings.Join(refs, ", "), strings.Join(refs, ", "), strings.Join(clients, ", "))
	return b.String()
}

func (p *program) contractOf(cl *call) *contract {
	for _, c := range p.contracts {
		for _, x := range c.calls {
			if x == cl {
				return c
			}
		}
	}
	panic("call of no contract")
}

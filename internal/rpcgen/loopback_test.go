package rpcgen

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
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
// defect agree (an odd seed ends in one). Then, for each argument, value
// and failure of each operation that reaches an int, a call that carries
// 2^53 or -2^53 there runs alone: directly it ends with its outcome, on
// the wire in the Unrepresentable defect (the int rule). The checked
// functions (<Op>Checked, R-chk) run the same sequence to the same output,
// fail the typed rpc.Unrepresentable with the int's path and value for
// each wide call, and fail rpc.Transport for every call to a URL with no
// RPC mount and to a stopped server. A failing seed prints the Ergo
// source, both outputs and the replay command.
//
// It builds Go because the wire is runtime behaviour: the HTTP server, the
// task per call and the client run only in a built program; go/types on
// the lowered Go says only that it compiles.
func TestRpcLoopbackAgrees(t *testing.T) {
	needsBuild(t)
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

// needsBuild marks a test that builds or runs a Go program: the fast tier
// (-short) skips it, the full tier runs it, as lower's needsBuild does.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

func loopback(t *testing.T, seed uint64) {
	p := newProgram(seed)
	t.Logf("seed %d: %d contracts, %d calls, ends in a defect %v, %d wide calls", seed, len(p.contracts), len(p.order), p.dies, len(p.wides))
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
		out, err := Generate(a.Root().Package, fmt.Sprintf("loop/c%d", i), "Svc")
		if err != nil {
			t.Fatalf("seed %d: generate c%d: %v", seed, i, err)
		}
		for name, src := range out.Files {
			writeFile(t, filepath.Join(pkgDir, out.Dir, name), src)
			files[fmt.Sprintf("c%d/%s/%s", i, out.Dir, name)] = src
		}
	}
	exe := buildProgram(t, seed, dir, files)
	replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestRpcLoopbackAgrees$' ./internal/rpcgen", seedVariable, seed)
	direct := runOnce(t, exe, "direct")
	srv := startServer(t, exe)
	viaRPC := runOnce(t, exe, "http://"+srv.addr+"/rpc")
	srv.stop(t)
	// A fresh server for the checked run: the provider counts its calls.
	srv = startServer(t, exe)
	checked := runOnce(t, exe, "checked", "http://"+srv.addr+"/rpc")
	refused := runOnce(t, exe, "checked", "http://"+srv.addr+"/nope")
	srv.stop(t)
	lost := runOnce(t, exe, "checked", "http://"+srv.addr+"/rpc")
	// Servers that answer what no contract holds: a failure the row does
	// not name, and a value that is not the operation's.
	liar := startServer(t, exe, "lie-fail")
	unnamed := runOnce(t, exe, "checked", "http://"+liar.addr+"/rpc")
	liar.stop(t)
	liar = startServer(t, exe, "lie-value")
	badValue := runOnce(t, exe, "checked", "http://"+liar.addr+"/rpc")
	liar.stop(t)
	for _, run := range []struct {
		name string
		o    outcome
	}{{"the client and the server", viaRPC}, {"the checked functions and the server", checked}} {
		agree := direct.stdout == run.o.stdout && direct.code == run.o.code
		if p.dies {
			// Both end in the provider's defect: a defect of the provider
			// is no failure of the wire, so the checked form raises it too.
			agree = agree && strings.Contains(direct.stderr, p.boom()) && strings.Contains(run.o.stderr, p.boom())
		}
		if !agree {
			t.Errorf("seed %d: the call sequence differs through the wire\n%s\n--- direct (exit %d)\n%s%s--- through %s (exit %d)\n%s%s--- server stderr\n%s\n%s",
				seed, sources(files), direct.code, direct.stdout, direct.stderr, run.name, run.o.code, run.o.stdout, run.o.stderr, srv.stderr.String(), replay)
		}
	}
	// A URL that names no RPC mount (a refused call, 404) and a server
	// that is gone: through the checked functions every call, the
	// provider's defect's included, fails rpc.Transport.
	calls := len(p.order) + len(p.contracts)
	for _, run := range []struct {
		name string
		o    outcome
	}{{"a URL with no RPC mount", refused}, {"a stopped server", lost}, {"a server that fails what no row names", unnamed}} {
		lines := strings.Split(strings.TrimSuffix(run.o.stdout, "\n"), "\n")
		ok := run.o.code == 0 && len(lines) == calls
		for _, l := range lines {
			ok = ok && strings.HasSuffix(l, " transport")
		}
		if !ok {
			t.Errorf("seed %d: through the checked functions to %s, %d calls do not each fail rpc.Transport (exit %d)\n%s%s\n%s\n%s",
				seed, run.name, calls, run.o.code, run.o.stdout, run.o.stderr, sources(files), replay)
		}
	}
	// A value that is not the operation's is rpc.Transport; an operation
	// with no value reads none, so it ends as unit.
	var want strings.Builder
	line := func(o *op) {
		if o.result == nil {
			fmt.Fprintf(&want, "%s is unit\n", o.name)
		} else {
			fmt.Fprintf(&want, "%s transport\n", o.name)
		}
	}
	for _, cl := range p.order[:p.scripted()] {
		line(cl.op)
	}
	for _, c := range p.contracts {
		fmt.Fprintf(&want, "C%dLog transport\n", c.i)
	}
	if p.dies {
		line(p.order[len(p.order)-1].op)
	}
	if badValue.code != 0 || badValue.stdout != want.String() {
		t.Errorf("seed %d: through the checked functions to a server that answers values no operation holds (exit %d)\n%s%swant\n%s\n%s\n%s",
			seed, badValue.code, badValue.stdout, badValue.stderr, want.String(), sources(files), replay)
	}
	if !strings.Contains(direct.stdout, "is ") && !strings.Contains(direct.stdout, "fails ") {
		t.Errorf("seed %d: the sequence made no call:\n%s", seed, direct.stdout)
	}
	if t.Failed() {
		return
	}
	effectView(t, p, exe, files, replay)
	// Each wide call runs directly to its outcome; through the wire it
	// ends in the Unrepresentable defect, with no output.
	for k, w := range p.wides {
		key := fmt.Sprintf("w%d", k)
		direct := runOnce(t, exe, "direct", key)
		srv := startServer(t, exe, key)
		viaRPC := runOnce(t, exe, "http://"+srv.addr+"/rpc", key)
		// A value or a failure is refused by the server itself, for a
		// client that does not check (Effect's): its answer is a Die.
		if !strings.HasPrefix(w.where, "argument") {
			pl := runOnce(t, exe, "payload", key)
			body := `{"_tag":"Request","id":0,"tag":"` + w.call.op.name + `","payload":` + strings.TrimSpace(pl.stdout) + `,"headers":[]}`
			resp, err := http.Post("http://"+srv.addr+"/rpc", "application/json", strings.NewReader(body))
			var answer []byte
			if err == nil {
				answer, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
			if err != nil || !bytes.Contains(answer, []byte(`"_tag":"Die","defect":{"name":"Unrepresentable"`)) {
				t.Errorf("seed %d: the server sends the wide %s of %s (call %s): %s %v\nrequest %s\n%s\n%s", seed, w.where, w.call.op.name, key, answer, err, body, sources(files), replay)
			}
		}
		// Through the checked function the refusal is the typed
		// rpc.Unrepresentable, with the int's path and value.
		chk := runOnce(t, exe, "checked", "http://"+srv.addr+"/rpc", key)
		if want := fmt.Sprintf("%s unrepresentable %s %s\n", w.call.op.name, w.path, w.value); chk.code != 0 || chk.stdout != want {
			t.Errorf("seed %d: the wide call %s (in its %s of %s) through the checked function: exit %d\n%s%swant %s\n%s\n%s",
				seed, key, w.where, w.call.op.name, chk.code, chk.stdout, chk.stderr, want, sources(files), replay)
		}
		srv.stop(t)
		if direct.code != 0 || strings.Count(direct.stdout, "\n") != 1 || viaRPC.code != 2 || viaRPC.stdout != "" || !strings.Contains(viaRPC.stderr, "Unrepresentable") {
			t.Errorf("seed %d: the wide call %s (an int beyond +-(2^53-1) in its %s of %s) is not refused on the wire\n%s\n--- direct (exit %d)\n%s%s--- through the client and the server (exit %d)\n%s%s--- server stderr\n%s\n%s",
				seed, key, w.where, w.call.op.name, sources(files), direct.code, direct.stdout, direct.stderr, viaRPC.code, viaRPC.stdout, viaRPC.stderr, srv.stderr.String(), replay)
		}
	}
}

// effectView replays the sequence as a client that reads the wire with
// Effect's Schemas would: each call's payload, as the generated client
// encodes it, posted to a fresh server; the payload and the answer must
// hold Effect's form for their types (records with lowered keys, unions
// and failures tagged, Option as {"_tag":"Some","value":…} or
// {"_tag":"None"}, ints exact numbers, unit as null), and each answer is
// the outcome the sequence planned.
func effectView(t *testing.T, p *program, exe string, files map[string]string, replay string) {
	t.Helper()
	pls := runOnce(t, exe, "payloads")
	lines := strings.Split(strings.TrimSuffix(pls.stdout, "\n"), "\n")
	if pls.code != 0 || len(lines) != len(p.order) {
		t.Fatalf("the payloads: exit %d, %d lines for %d calls\n%s%s", pls.code, len(lines), len(p.order), pls.stdout, pls.stderr)
	}
	srv := startServer(t, exe)
	defer srv.stop(t)
	for k, cl := range p.order {
		params := &ty{kind: "record", name: cl.op.name + "Request", fields: cl.op.params}
		body := `{"_tag":"Request","id":` + strconv.Itoa(k) + `,"tag":"` + cl.op.name + `","payload":` + lines[k] + `,"headers":[]}`
		answer, err := postJSON("http://"+srv.addr+"/rpc", body)
		var exits []struct {
			Tag       string          `json:"_tag"`
			RequestID json.Number     `json:"requestId"`
			Exit      json.RawMessage `json:"exit"`
		}
		if err == nil {
			err = json.Unmarshal(answer, &exits)
		}
		if err == nil && len(exits) != 1 {
			err = fmt.Errorf("%d exits", len(exits))
		}
		if err == nil {
			err = checkForm(params, json.RawMessage(lines[k]), "payload", false)
		}
		if err == nil {
			err = cl.checkExit(exits[0].Exit)
		}
		if err != nil {
			t.Fatalf("seed %d: call %d (%s), as Effect's client reads it: %v\nrequest %s\nanswer %s\n%s\n%s", p.seed, k, cl.op.name, err, body, answer, sources(files), replay)
		}
	}
}

func postJSON(url, body string) ([]byte, error) {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// checkExit holds the exit to the call's planned outcome and the form of
// its value or failure.
func (cl *call) checkExit(raw json.RawMessage) error {
	var exit struct {
		Tag   string          `json:"_tag"`
		Value json.RawMessage `json:"value"`
		Cause []struct {
			Tag    string          `json:"_tag"`
			Error  json.RawMessage `json:"error"`
			Defect json.RawMessage `json:"defect"`
		} `json:"cause"`
	}
	if err := json.Unmarshal(raw, &exit); err != nil {
		return err
	}
	switch {
	case cl.outcome == -1:
		if exit.Tag != "Success" {
			return fmt.Errorf("exit %s, want Success", exit.Tag)
		}
		if cl.op.result == nil {
			if string(exit.Value) != "null" {
				return fmt.Errorf("a unit value is %s, want null", exit.Value)
			}
			return nil
		}
		return checkForm(cl.op.result, exit.Value, "value", false)
	case exit.Tag != "Failure" || len(exit.Cause) != 1:
		return fmt.Errorf("exit %s, want a Failure of one cause", raw)
	case cl.outcome == -2:
		if exit.Cause[0].Tag != "Die" || !bytes.Contains(exit.Cause[0].Defect, []byte(`"message":`)) {
			return fmt.Errorf("cause %s, want a Die with a message", raw)
		}
		return nil
	}
	f := cl.op.fails[cl.outcome]
	if exit.Cause[0].Tag != "Fail" {
		return fmt.Errorf("cause %s, want Fail", raw)
	}
	return checkForm(&ty{kind: "union", variants: []*ty{f}}, exit.Cause[0].Error, "failure", true)
}

// checkForm reports where raw, a JSON value of t, departs from Effect's
// form. tagged: a record is a tagged one (a union variant or a failure).
func checkForm(t *ty, raw json.RawMessage, at string, tagged bool) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return fmt.Errorf("%s: %v", at, err)
	}
	return form(t, v, at, tagged)
}

func form(t *ty, v any, at string, tagged bool) error {
	bad := func() error { return fmt.Errorf("%s: %v is not a %s in Effect's form", at, v, t.src("")) }
	switch t.kind {
	case "int":
		n, ok := v.(json.Number)
		if !ok {
			return bad()
		}
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil || i > 1<<53-1 || i < -(1<<53-1) {
			return bad()
		}
	case "float64":
		if _, ok := v.(json.Number); !ok {
			return bad()
		}
	case "string":
		if _, ok := v.(string); !ok {
			return bad()
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return bad()
		}
	case "slice":
		xs, ok := v.([]any)
		if !ok {
			return bad()
		}
		for i, x := range xs {
			if err := form(t.elem, x, fmt.Sprintf("%s[%d]", at, i), false); err != nil {
				return err
			}
		}
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		for k, x := range m {
			if err := form(t.elem, x, at+"."+k, false); err != nil {
				return err
			}
		}
	case "option":
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		switch m["_tag"] {
		case "None":
			if len(m) != 1 {
				return bad()
			}
		case "Some":
			if _, has := m["value"]; !has || len(m) != 2 {
				return bad()
			}
			return form(t.elem, m["value"], at+".value", false)
		default:
			return bad()
		}
	case "union":
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		for _, r := range t.variants {
			if m["_tag"] == r.name {
				return form(r, v, at, true)
			}
		}
		return bad()
	case "record":
		m, ok := v.(map[string]any)
		if !ok {
			return bad()
		}
		want := len(t.fields)
		if tagged {
			if m["_tag"] != t.name {
				return bad()
			}
			want++
		}
		if len(m) != want {
			return bad()
		}
		for _, f := range t.fields {
			// Effect's key: the name with its first letter lowered.
			key := strings.ToLower(f.name[:1]) + f.name[1:]
			x, has := m[key]
			if !has {
				return fmt.Errorf("%s: %v lacks the key %q", at, v, key)
			}
			if err := form(f.t, x, at+"."+key, false); err != nil {
				return err
			}
		}
	default:
		return bad()
	}
	return nil
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

func writeFile(t testing.TB, path, src string) {
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

func limit(t testing.TB) uint64 {
	t.Helper()
	n, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// runOnce runs the program to its end under the memory bound.
func runOnce(t *testing.T, exe string, args ...string) outcome {
	t.Helper()
	arg := strings.Join(args, " ")
	cmd := exec.Command(exe, args...)
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
func startServer(t *testing.T, exe string, key ...string) *server {
	t.Helper()
	s := &server{cmd: exec.Command(exe, append([]string{"serve"}, key...)...)}
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
	// wides are the calls that carry an int the wire refuses; each runs
	// alone.
	wides []wideCall
}

var fieldNames = []string{"Name", "Count", "IsOn", "RetryAfterMs", "Tags", "Weights", "Extra", "Note"}
var paramNames = []string{"key", "count", "label", "opts", "items", "flag"}

func newProgram(seed uint64) *program {
	p := &program{seed: seed, r: rand.New(rand.NewPCG(seed, 0x5eed))}
	n := 1 + p.r.IntN(3)
	for i := 0; i < n; i++ {
		p.contracts = append(p.contracts, p.contract(i))
	}
	// The wide calls need an operation that reaches an int; one is added
	// to the last contract when none does.
	last := p.contracts[len(p.contracts)-1]
	if !p.anyWideOp() {
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
	// An odd seed ends the sequence in a defect that the last call's
	// provider raises.
	if seed%2 == 1 {
		o := last.ops[p.r.IntN(len(last.ops))]
		cl := &call{op: o, n: len(last.calls), outcome: -2}
		for _, prm := range o.params {
			cl.args = append(cl.args, p.lit(prm.t, fmt.Sprintf("c%d.", last.i), 0))
		}
		last.calls = append(last.calls, cl)
		p.order = append(p.order, cl)
		p.dies = true
	}
	// The wide calls: for each argument, value and failure of each
	// operation that reaches an int, a call that carries one beyond
	// +-(2^53-1) there. Each runs alone, against the Wide provider.
	for _, c := range p.contracts {
		q := fmt.Sprintf("c%d.", c.i)
		for _, o := range c.ops {
			normal := func() *call {
				cl := &call{op: o, outcome: -1}
				for _, prm := range o.params {
					cl.args = append(cl.args, p.lit(prm.t, q, 0))
				}
				if o.result != nil {
					cl.value = p.lit(o.result, "", 0)
				}
				return cl
			}
			// The edges, 2^53 and -2^53, alternate.
			w := func() string { return []string{"9007199254740992", "-9007199254740992"}[len(p.wides)%2] }
			for i, prm := range o.params {
				n := w()
				if s, at, ok := p.wideLit(prm.t, q, n); ok {
					cl := normal()
					cl.args[i] = s
					p.wides = append(p.wides, wideCall{c: c, call: cl, where: "argument " + prm.name, path: under(prm.name, at), value: n})
				}
			}
			if o.result != nil {
				n := w()
				if s, at, ok := p.wideLit(o.result, "", n); ok {
					cl := normal()
					cl.value = s
					p.wides = append(p.wides, wideCall{c: c, call: cl, where: "value", path: at, value: n})
				}
			}
			for i, f := range o.fails {
				n := w()
				if s, at, ok := p.wideLit(f, "", n); ok {
					cl := normal()
					cl.outcome, cl.value = i, s
					p.wides = append(p.wides, wideCall{c: c, call: cl, where: "failure " + f.name, path: at, value: n})
				}
			}
		}
	}
	return p
}

// wideCall is a call that carries an int beyond +-(2^53-1) at where.
type wideCall struct {
	c     *contract
	call  *call
	where string
	// path and value are the rpc.Unrepresentable the checked call fails.
	path, value string
}

// anyWideOp reports an operation, of any contract, whose arguments, value
// or failures reach an int.
func (p *program) anyWideOp() bool {
	for _, c := range p.contracts {
		for _, o := range c.ops {
			for _, prm := range o.params {
				if reachesInt(prm.t) {
					return true
				}
			}
			if o.result != nil && reachesInt(o.result) {
				return true
			}
			for _, f := range o.fails {
				if reachesInt(f) {
					return true
				}
			}
		}
	}
	return false
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

// wideLit is a value of t that holds the int w, one beyond +-(2^53-1),
// when t reaches an int, and the int's path in t as rpc.Unrepresentable
// names it: wire keys and positions joined by ".", "" for t itself.
func (p *program) wideLit(t *ty, q, w string) (string, string, bool) {
	switch t.kind {
	case "int":
		return w, "", true
	case "slice":
		if s, at, ok := p.wideLit(t.elem, q, w); ok {
			return t.src(q) + "{" + s + "}", under("0", at), true
		}
	case "map":
		if s, at, ok := p.wideLit(t.elem, q, w); ok {
			return t.src(q) + `{"w": ` + s + "}", under("w", at), true
		}
	case "option":
		if s, at, ok := p.wideLit(t.elem, q, w); ok {
			return "Some[" + t.elem.src(q) + "]{Value: " + s + "}", under("value", at), true
		}
	case "record":
		for i, f := range t.fields {
			if s, at, ok := p.wideLit(f.t, q, w); ok {
				var fs []string
				for j, g := range t.fields {
					v := s
					if j != i {
						v = p.lit(g.t, q, 1)
					}
					fs = append(fs, g.name+": "+v)
				}
				return q + t.name + "{" + strings.Join(fs, ", ") + "}", under(check.WireFieldName(&check.Field{Name: f.name}), at), true
			}
		}
	case "union":
		for _, v := range t.variants {
			if s, at, ok := p.wideLit(v, q, w); ok {
				return s, at, true
			}
		}
	}
	return "", "", false
}

// under is the path at inside the value at prefix, as rpc.Under joins it.
func under(prefix, at string) string {
	if at == "" {
		return prefix
	}
	return prefix + "." + at
}

func (p *program) boom() string { return fmt.Sprintf("boom %d", p.seed) }

// scripted is the number of calls before the logs: every call, or all
// but the last when the sequence ends in the provider's defect.
func (p *program) scripted() int {
	if p.dies {
		return len(p.order) - 1
	}
	return len(p.order)
}

// opWide reports an operation whose arguments, value or failures reach an
// int: its checked function's row names rpc.Unrepresentable.
func opWide(o *op) bool {
	for _, prm := range o.params {
		if reachesInt(prm.t) {
			return true
		}
	}
	if o.result != nil && reachesInt(o.result) {
		return true
	}
	for _, f := range o.fails {
		if reachesInt(f) {
			return true
		}
	}
	return false
}

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
// gives the outcome the sequence planned for its count; with a wide key
// ("w3"), the outcome of that wide call.
type Ref { log sync.Ref[[]string], n sync.Ref[int], wide string }

func NewRef(wide string) Ref { Ref{log: sync.NewRef[[]string]([]string{}), n: sync.NewRef[int](0), wide: wide} }

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
		fmt.Fprintf(&b, "\tnote(r, %s)\n", note)
		outcome := func(cl *call) {
			switch {
			case cl.outcome == -2:
				fmt.Fprintf(&b, "\t\tUnreachable(%q)\n", p.boom())
			case cl.outcome >= 0:
				fmt.Fprintf(&b, "\t\treturn %s\n", cl.value)
			case o.result != nil:
				fmt.Fprintf(&b, "\t\treturn %s\n", cl.value)
			default:
				b.WriteString("\t\treturn\n")
			}
		}
		var wides []int
		for k, w := range p.wides {
			if w.call.op == o {
				wides = append(wides, k)
			}
		}
		if len(wides) > 0 {
			b.WriteString("\tmatch r.wide {\n")
			for _, k := range wides {
				fmt.Fprintf(&b, "\tcase \"w%d\":\n", k)
				outcome(p.wides[k].call)
			}
			b.WriteString("\tcase _:\n\t}\n")
		}
		b.WriteString("\tmatch next(r) {\n")
		for _, cl := range c.calls {
			if cl.op != o {
				continue
			}
			fmt.Fprintf(&b, "\tcase %d:\n", cl.n)
			outcome(cl)
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
	b.WriteString("import http\nimport json\nimport os\nimport rpc\nimport strings\n")
	var caps []string
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/c%d\nimport loop/c%d/c%drpc\n", c.i, c.i, c.i)
		caps = append(caps, fmt.Sprintf("c%d.Svc", c.i))
	}
	needs := strings.Join(caps, " | ")
	b.WriteString("\nfunc enc[T wire](v T) string { json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, v) }\n")
	callFunc := func(name string, c *contract, cl *call) {
		q := fmt.Sprintf("c%d.", c.i)
		fmt.Fprintf(&b, "\nfunc %s() string needs %sSvc {\n\t", name, q)
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
	// checkedFunc is the call through the generated checked function: the
	// same line for a value or a failure of the row, and a line for each
	// failure of the wire.
	checkedFunc := func(name string, c *contract, cl *call) {
		q := fmt.Sprintf("c%d.", c.i)
		fmt.Fprintf(&b, "\nfunc %s(conn rpc.Conn) string {\n\t", name)
		args := append([]string{fmt.Sprintf("c%drpc.NewClient(conn)", c.i)}, cl.args...)
		if cl.op.result != nil {
			b.WriteString("let v = ")
		}
		fmt.Fprintf(&b, "c%drpc.%sChecked(%s) catch {\n", c.i, cl.op.name, strings.Join(args, ", "))
		for _, f := range cl.op.fails {
			fmt.Fprintf(&b, "\tcase %s%s as e:\n\t\treturn \"%s fails %s \" + enc(e)\n", q, f.name, cl.op.name, f.name)
		}
		if opWide(cl.op) {
			fmt.Fprintf(&b, "\tcase rpc.Unrepresentable as u:\n\t\treturn \"%s unrepresentable \\(u.Path) \\(u.Value)\"\n", cl.op.name)
		}
		fmt.Fprintf(&b, "\tcase rpc.Transport:\n\t\treturn \"%s transport\"\n\t}", cl.op.name)
		if cl.op.result != nil {
			fmt.Fprintf(&b, "\n\t\"%s is \" + enc(v)\n}\n", cl.op.name)
		} else {
			fmt.Fprintf(&b, "\n\t\"%s is unit\"\n}\n", cl.op.name)
		}
	}
	for k, cl := range p.order {
		callFunc(fmt.Sprintf("call%d", k), p.contractOf(cl), cl)
		checkedFunc(fmt.Sprintf("checked%d", k), p.contractOf(cl), cl)
	}
	for k, w := range p.wides {
		callFunc(fmt.Sprintf("wide%d", k), w.c, w.call)
		checkedFunc(fmt.Sprintf("checkedWide%d", k), w.c, w.call)
	}
	// checkedScript is script through the checked functions: the same
	// lines, the provider's defect included, over conn.
	b.WriteString("\nfunc checkedScript(conn rpc.Conn) needs Console {\n")
	for k := 0; k < p.scripted(); k++ {
		fmt.Fprintf(&b, "\tConsole.Println(checked%d(conn))\n", k)
	}
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "\tlet log%[1]d = c%[1]drpc.C%[1]dLogChecked(c%[1]drpc.NewClient(conn)) catch {\n\tcase rpc.Transport:\n\t\t[]string{\"C%[1]dLog transport\"}\n\t}\n\tfor l in log%[1]d {\n\t\tConsole.Println(l)\n\t}\n", c.i)
	}
	if p.dies {
		fmt.Fprintf(&b, "\tConsole.Println(checked%d(conn))\n", p.scripted())
	}
	b.WriteString("}\n")
	b.WriteString("\nfunc checkedWide(conn rpc.Conn, key string) needs Console {\n\tmatch key {\n")
	for k := range p.wides {
		fmt.Fprintf(&b, "\tcase \"w%d\":\n\t\tConsole.Println(checkedWide%d(conn))\n", k, k)
	}
	b.WriteString("\tcase _:\n\t\tUnreachable(\"no such wide call\")\n\t}\n}\n")
	fmt.Fprintf(&b, "\nfunc script() needs Console | %s {\n", needs)
	last := len(p.order)
	if p.dies {
		last--
	}
	for k := 0; k < last; k++ {
		fmt.Fprintf(&b, "\tConsole.Println(call%d())\n", k)
	}
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "\tfor l in c%d.Svc.C%dLog() {\n\t\tConsole.Println(l)\n\t}\n", c.i, c.i)
	}
	if p.dies {
		fmt.Fprintf(&b, "\tConsole.Println(call%d())\n", last)
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "\nfunc wide(key string) needs Console | %s {\n\tmatch key {\n", needs)
	for k := range p.wides {
		fmt.Fprintf(&b, "\tcase \"w%d\":\n\t\tConsole.Println(wide%d())\n", k, k)
	}
	b.WriteString("\tcase _:\n\t\tUnreachable(\"no such wide call\")\n\t}\n}\n")
	// payload is a wide call's Request payload, as a client that does not
	// check its answers (Effect's) sends it.
	request := func(c *contract, cl *call) string {
		var fs []string
		for i, prm := range cl.op.params {
			fs = append(fs, FieldName(prm.name)+": "+cl.args[i])
		}
		req := fmt.Sprintf("c%drpc.%sRequest", c.i, cl.op.name)
		if len(fs) > 0 {
			req += "{" + strings.Join(fs, ", ") + "}"
		}
		return req
	}
	b.WriteString("\nfunc payload(key string) string {\n\tmatch key {\n")
	for k, w := range p.wides {
		fmt.Fprintf(&b, "\tcase \"w%d\":\n\t\trpc.Encode(%s)\n", k, request(w.c, w.call))
	}
	b.WriteString("\tcase _:\n\t\tUnreachable(\"no such wide call\")\n\t}\n}\n")
	// payloads are the sequence's Request payloads, a line each.
	b.WriteString("\nfunc payloads() needs Console {\n")
	for _, cl := range p.order {
		fmt.Fprintf(&b, "\tConsole.Println(rpc.Encode(%s))\n", request(p.contractOf(cl), cl))
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "\nfunc handle(tag string, payload string) string fails rpc.Failed needs %s {\n", needs)
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "\tif strings.HasPrefix(tag, \"C%d\") {\n\t\tlet s = try c%drpc.Handle(tag, payload)\n\t\treturn s\n\t}\n", c.i, c.i)
	}
	b.WriteString("\trpc.Die[string](\"RpcError\", \"no contract has \\(tag)\")\n}\n")
	// The lying servers: lieFail answers every call with a failure no
	// contract names; lieValue answers every call with a value that is not
	// its operation's (0 for a bool, true for any other type).
	b.WriteString("\nfunc lieFail(tag string, payload string) string fails rpc.Failed {\n\treturn rpc.Failed{Text: \"{\\\"_tag\\\":\\\"Unnamed\\\"}\"}\n}\n")
	b.WriteString("\nfunc lieValue(tag string, payload string) string fails rpc.Failed {\n\tmatch tag {\n")
	for _, c := range p.contracts {
		for _, o := range c.ops {
			if o.result != nil && o.result.kind == "bool" {
				fmt.Fprintf(&b, "\tcase %q:\n\t\t\"0\"\n", o.name)
			}
		}
	}
	b.WriteString("\tcase _:\n\t\t\"true\"\n\t}\n}\n")
	var refs, clients []string
	for _, c := range p.contracts {
		refs = append(refs, fmt.Sprintf("c%d.Svc: c%d.NewRef(key)", c.i, c.i))
		clients = append(clients, fmt.Sprintf("c%d.Svc: c%drpc.NewClient(conn)", c.i, c.i))
	}
	// Modes: serve [KEY]; direct [KEY]; URL [KEY]; checked URL [KEY]
	// (through the checked functions). A KEY ("w3") runs that wide call
	// alone; the server's provider gives its outcome.
	fmt.Fprintf(&b, `
func main() fails http.ListenFailed | http.InvalidLimits needs Console | Ambient {
	let args = os.NewHost().Args()
	let mode = args[0]
	let key = if len(args) > 1 { args[1] } else { "" }
	match mode {
	case "serve":
		let l = try http.Listen("127.0.0.1:0")
		Console.Println("listening \(l.Addr())")
		let limits = http.Limits{MaxBodyBytes: 16777216, MaxHeaderBytes: 1048576}
		if key == "lie-fail" {
			try rpc.Serve(l, limits, "/rpc", lieFail)
			return
		}
		if key == "lie-value" {
			try rpc.Serve(l, limits, "/rpc", lieValue)
			return
		}
		with context { %[1]s } {
			try rpc.Serve(l, http.Limits{MaxBodyBytes: 16777216, MaxHeaderBytes: 1048576}, "/rpc", handle)
		}
	case "payload":
		Console.Println(payload(key))
	case "payloads":
		payloads()
	case "direct":
		with context { %[1]s } {
			if key == "" { script() } else { wide(key) }
		}
	case "checked":
		let conn = rpc.Dial(key)
		if len(args) > 2 { checkedWide(conn, args[2]) } else { checkedScript(conn) }
	case _:
		let conn = rpc.Dial(mode)
		with context { %[2]s } {
			if key == "" { script() } else { wide(key) }
		}
	}
}
`, strings.Join(refs, ", "), strings.Join(clients, ", "))
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

package bindc

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// liveEnv is a temporary module with the real purego v0.11.3
// (testdata/purego.sum) and the golden Go packages, for the live callback
// tests.
type liveEnv struct {
	t         *testing.T
	root, mod string
	goTool    string
	env       []string
}

// newLiveEnv makes the module, or skips the test without ERGO_BINDC_LIVE=1.
func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to build against the real purego and drive C libraries")
	}
	root := t.TempDir()
	e := &liveEnv{t: t, root: root, mod: filepath.Join(root, "mod"), goTool: filepath.Join(runtime.GOROOT(), "bin", "go")}
	e.env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=readonly -modcacherw", "GOMODCACHE="+filepath.Join(root, "modcache"), "GOWORK=off")
	e.write(filepath.Join(e.mod, "go.mod"), []byte("module ergo.local/bind\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	e.write(filepath.Join(e.mod, "go.sum"), readInput(t, "testdata/purego.sum"))
	return e
}

func (e *liveEnv) write(path string, data []byte) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run runs name in dir with the module's environment and stdin, and fails
// the test on an error.
func (e *liveEnv) run(dir, stdin string, extra []string, name string, args ...string) string {
	e.t.Helper()
	out, err := e.try(dir, stdin, extra, name, args...)
	if err != nil {
		e.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func (e *liveEnv) try(dir, stdin string, extra []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, e.env...), extra...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fixtureDriver puts the golden fixture package and the callbacks driver
// (testdata/live/callbacks/main.go) in the module, builds the fixture
// library and the driver, and returns their paths.
func (e *liveEnv) fixtureDriver() (lib, driver string) {
	e.t.Helper()
	return e.fixtureDriverFrom("testdata/live/callbacks/main.go")
}

// fixtureDriverFrom is fixtureDriver for the driver source driverSrc.
func (e *liveEnv) fixtureDriverFrom(driverSrc string) (lib, driver string) {
	t := e.t
	t.Helper()
	cc := requireCC(t)
	for name, src := range goldenGoFiles(t, "testdata/fixture") {
		e.write(filepath.Join(e.mod, "fixture", name), src)
	}
	e.write(filepath.Join(e.mod, "cbdriver", "main.go"), readInput(t, driverSrc))
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	src, _ := filepath.Abs("testdata/fixture/fixture.c")
	lib = filepath.Join(e.root, "libfixture"+ext)
	e.run(e.root, "", nil, cc, "-std=c11", "-shared", "-fPIC", "-o", lib, src, "-lm", "-lpthread")
	driver = filepath.Join(e.root, "cbdriver-bin")
	e.run(e.mod, "", nil, e.goTool, "build", "-o", driver, "./cbdriver")
	return lib, driver
}

// okLines are the checks a driver printed as "ok: <check>".
func okLines(out string) []string {
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if c, ok := strings.CutPrefix(line, "ok: "); ok {
			got = append(got, c)
		}
	}
	return got
}

// TestLiveCallbacks (A5, ERGO_BINDC_LIVE=1) drives the fixture's callbacks
// through the typed layer (testdata/live/callbacks/main.go): a call-scoped
// borrowed buffer and int64_t result, a value, registered callbacks keyed
// by their node with an out array, a replacement, the set and clear
// probes, a failed set, an unknown key, ends by node and by context
// destroy (LiveCallbacks back to its value before), nested calls, and
// 10,000 registrations and invocations with fresh closures, after which
// the binding has used at most one purego slot per bound typedef. A second
// process uses up the slots first: Load returns *CallbackSlotsError, the
// package stays unloaded, and the library is never opened.
func TestLiveCallbacks(t *testing.T) {
	e := newLiveEnv(t)
	lib, driver := e.fixtureDriver()
	want := []string{
		"noload probe sees a loaded library",
		"call-scoped borrowed buffer round trip",
		"int64 result reaches C",
		"value copy",
		"registered callback keyed by its node; its out array reaches C",
		"replacement takes effect",
		"set probe sees the new func; clear probe sees the old",
		"failed set leaves the registry as it was",
		"unknown key gives C the default",
		"node destroy releases its entry",
		"context destroy releases its entries",
		"nested call-scoped calls",
		"at most one slot per bound typedef",
	}
	out, err := e.try(e.root, "", nil, driver, "callbacks", lib)
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the callbacks driver: %v\n%s\nwant the checks:\n%s", err, out, strings.Join(want, "\n"))
	}
	t.Log(strings.TrimSpace(out[strings.Index(out, "slots used"):]))
	want = []string{"load returns the slot error", "not loaded after the slot error", "no C function reached"}
	out, err = e.try(e.root, "", nil, driver, "exhaust", lib)
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the exhaust driver: %v\n%s\nwant the checks:\n%s", err, out, strings.Join(want, "\n"))
	}
}

// The reference model of TestLiveCallbackRegistryModel: the contexts, the
// nodes and the registry as the contract says they are. A context is its
// index; a key is (context, slot, generation).
type regModel struct {
	ctxs  []*regCtx
	nodes []regNode
	reg   map[regKey]regFunc
}

type regCtx struct {
	live  bool
	slots [4]struct {
		live bool
		gen  uint32
	}
}

type regNode struct {
	ctx, slot int
	gen       uint32
}

type regKey struct {
	ctx, slot int
	gen       uint32
}

// regFunc is a measure func: its id, and the nested repeat its invocation
// makes (nk times the writer nw).
type regFunc struct {
	id, nw string
	nk     int
}

func (n regNode) key() regKey { return regKey{n.ctx, n.slot, n.gen} }

// stale reports whether node n is not live in its context.
func (m *regModel) stale(n regNode) bool {
	s := m.ctxs[n.ctx].slots[n.slot]
	return !s.live || s.gen != n.gen
}

// run is what one invocation of f runs.
func (f regFunc) run() []string {
	out := []string{f.id}
	for range f.nk {
		out = append(out, f.nw)
	}
	return out
}

// step applies one command and returns the observation the driver must
// print: "ran=<ids> res=<result> live=<n>".
func (m *regModel) step(cmd string) string {
	f := strings.Fields(cmd)
	num := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	var ran []string
	res := ""
	const invalid, stale, oom = "err:-1", "err:-3", "err:-5"
	node := func() (regNode, string) {
		n := m.nodes[num(1)]
		switch {
		case !m.ctxs[n.ctx].live:
			return n, "err:destroyed"
		case m.stale(n):
			return n, stale
		}
		return n, ""
	}
	switch f[0] {
	case "reset":
		m.ctxs, m.nodes, m.reg = nil, nil, map[regKey]regFunc{}
	case "ctx":
		m.ctxs = append(m.ctxs, &regCtx{live: true})
		res = strconv.Itoa(len(m.ctxs) - 1)
	case "ctxd":
		c := num(1)
		if !m.ctxs[c].live {
			res = "err:destroyed"
			break
		}
		m.ctxs[c].live = false
		for k := range m.reg {
			if k.ctx == c {
				delete(m.reg, k)
			}
		}
	case "node":
		c := m.ctxs[num(1)]
		if !c.live {
			res = "err:destroyed"
			break
		}
		res = oom
		for i := range c.slots {
			if !c.slots[i].live {
				c.slots[i].live = true
				c.slots[i].gen++
				m.nodes = append(m.nodes, regNode{num(1), i, c.slots[i].gen})
				res = strconv.Itoa(len(m.nodes) - 1)
				break
			}
		}
	case "noded":
		n, err := node()
		if res = err; err == "" {
			m.ctxs[n.ctx].slots[n.slot].live = false
			delete(m.reg, n.key())
		}
	case "set":
		n, err := node()
		if res = err; err == "" {
			fn := regFunc{id: f[2], nk: num(3), nw: f[4]}
			m.reg[n.key()] = fn
			ran = fn.run()
		}
	case "clear":
		n, err := node()
		if res = err; err == "" {
			if fn, ok := m.reg[n.key()]; ok {
				ran = fn.run()
				delete(m.reg, n.key())
			}
		}
	case "inv", "as":
		n, err := node()
		if res = err; err != "" {
			break
		}
		fn, ok := m.reg[n.key()]
		if !ok {
			res = invalid
			break
		}
		if f[0] == "as" {
			fn, ok = m.reg[m.nodes[num(2)].key()]
			if !ok {
				res = "nan"
				break
			}
		}
		ran, res = fn.run(), fn.id
	case "all":
		c := m.ctxs[num(1)]
		if !c.live {
			res = "err:destroyed"
			break
		}
		count := 0
		for i, s := range c.slots {
			if fn, ok := m.reg[regKey{num(1), i, s.gen}]; ok && s.live {
				ran = append(ran, fn.run()...)
				count++
			}
		}
		res = strconv.Itoa(count)
	case "rep":
		if !m.ctxs[num(1)].live {
			res = "err:destroyed"
			break
		}
		for i := range num(2) {
			ran = append(ran, f[3])
			if i == 0 {
				for range num(4) {
					ran = append(ran, f[5])
				}
			}
		}
		res = strconv.Itoa(num(2))
	}
	return fmt.Sprintf("ran=%s res=%s live=%d", strings.Join(ran, ","), res, len(m.reg))
}

// regSequence generates one command sequence of up to 40 commands, and the
// observations the model gives. The commands favour the cases the
// registry can get wrong: replacements and clears, ends by node and by
// context destroy, stale handles, unknown keys, and call-scoped calls
// nested in registered and in call-scoped callbacks.
func regSequence(r *rand.Rand) (cmds, want []string) {
	m := &regModel{}
	id := 0
	next := func() string { id++; return strconv.Itoa(id) }
	add := func(c string) {
		cmds = append(cmds, c)
		want = append(want, m.step(c))
	}
	add("reset")
	add("ctx")
	n := 1 + r.IntN(40)
	for len(cmds) < n+2 {
		var live []int
		for i, c := range m.ctxs {
			if c.live {
				live = append(live, i)
			}
		}
		pickCtx := func() int {
			if len(live) == 0 || r.IntN(20) == 0 {
				return r.IntN(len(m.ctxs))
			}
			return live[r.IntN(len(live))]
		}
		pickNode := func() int { return r.IntN(len(m.nodes)) }
		nest := func() (int, string) {
			if r.IntN(3) == 0 {
				return 1 + r.IntN(3), next()
			}
			return 0, "0"
		}
		switch k := r.IntN(100); {
		case k < 6 && len(live) < 3:
			add("ctx")
		case k < 10 && len(m.ctxs) > 0:
			add(fmt.Sprintf("ctxd %d", pickCtx()))
		case k < 25 && len(m.ctxs) > 0:
			add(fmt.Sprintf("node %d", pickCtx()))
		case len(m.nodes) == 0 || len(m.ctxs) == 0:
			continue
		case k < 32:
			add(fmt.Sprintf("noded %d", pickNode()))
		case k < 50:
			nk, nw := nest()
			add(fmt.Sprintf("set %d %s %d %s", pickNode(), next(), nk, nw))
		case k < 57:
			add(fmt.Sprintf("clear %d", pickNode()))
		case k < 67:
			add(fmt.Sprintf("inv %d", pickNode()))
		case k < 74:
			add(fmt.Sprintf("as %d %d", pickNode(), pickNode()))
		case k < 82:
			add(fmt.Sprintf("all %d", pickCtx()))
		default:
			nk, nw := nest()
			add(fmt.Sprintf("rep %d %d %s %d %s", pickCtx(), r.IntN(5), next(), nk, nw))
		}
	}
	return cmds, want
}

// TestLiveCallbackRegistryModel (A5, ERGO_BINDC_LIVE=1, model-based):
// generated sequences (max(200, bindc.cases/5) of up to 40 commands) of
// context and node creates and destroys, sets, replacements and clears of
// a registered callback, invocations of one node, of a node with another
// node's key and of all nodes, and call-scoped calls (nested in registered
// and in call-scoped callbacks) run against the fixture and against the
// reference model regModel. After each command, the funcs that ran (or
// the default C got: nan, a -1 sum) and LiveCallbacks() equal the model's.
func TestLiveCallbackRegistryModel(t *testing.T) {
	e := newLiveEnv(t)
	lib, driver := e.fixtureDriver()
	nseq := max(200, *propCases/5)
	r := rand.New(rand.NewPCG(*propSeed, 0xb1dc2b))
	var in strings.Builder
	var seqs [][2][]string
	for range nseq {
		cmds, want := regSequence(r)
		seqs = append(seqs, [2][]string{cmds, want})
		for _, c := range cmds {
			in.WriteString(c + "\n")
		}
	}
	out, err := e.try(e.root, in.String(), nil, driver, "model", lib)
	if err != nil {
		t.Fatalf("the model driver: %v\n%s", err, tail(out, 2000))
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	i, commands := 0, 0
	for s, seq := range seqs {
		for j, c := range seq[0] {
			if i >= len(got) {
				t.Fatalf("the driver printed %d lines, want more\n%s", len(got), tail(out, 2000))
			}
			if got[i] != seq[1][j] {
				t.Fatalf("seed %d sequence %d command %d %q:\n  driver: %s\n  model:  %s\nthe sequence:\n%s",
					*propSeed, s, j, c, got[i], seq[1][j], strings.Join(seq[0][:j+1], "\n"))
			}
			i++
			commands++
		}
	}
	if i != len(got) {
		t.Fatalf("the driver printed %d lines, want %d", len(got), i)
	}
	t.Logf("%d sequences, %d commands", nseq, commands)
}

// tail is the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

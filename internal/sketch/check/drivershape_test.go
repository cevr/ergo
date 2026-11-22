package check_test

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
)

// drvGo is a driver-shaped Go package (GB1): a client that is a resource,
// a command result that keeps it (a go.Ref, no Close), a row set that
// keeps it and is a resource of its own, and Clone, whose result is the
// receiver itself, as go-redis's WithTimeout or mongo's options builders
// return theirs.
const drvGo = `package drv

type Client struct{ n int }
type Cmd struct{ c *Client }
type Rows struct{ c *Client }

func Dial(addr string) (*Client, error)        { return &Client{}, nil }
func (c *Client) Cmd(key string) (*Cmd, error)  { return &Cmd{c}, nil }
func (c *Client) Query(q string) (*Rows, error) { return &Rows{c}, nil }
func (c *Client) Clone() (*Client, error)       { return c, nil }
func (c *Client) Ping() error                   { return nil }
func (c *Client) Close() error                  { return nil }
func (k *Cmd) Result() (string, error)          { return "", nil }
func (r *Rows) Next() bool                      { return false }
func (r *Rows) Close() error                    { return nil }
`

type drvImporter struct{ set *check.GoBindings }

func (drvImporter) Import(path string) (*check.Package, error) {
	return nil, fmt.Errorf("no Ergo package %s", path)
}

func (d drvImporter) GoBindings() *check.GoBindings { return d.set }

// drvBindingFile is the binding ergo bind writes for drvGo from its
// source, checked in as gostd's bindings are, so TestDriverHandleIsLent
// starts no go command; TestDrvBindingIsCurrent keeps it current. Each
// result's Go-nil case is what the binder derives: Dial, Cmd and Query
// return a fresh pointer when the error is nil (go.Ref), and Clone
// returns its receiver, which a method called on a nil pointer may be
// (Option; GOBIND-NIL).
const drvBindingFile = "testdata/drv.binding"

// drvUpdate is the one command that writes drvBindingFile, and drvHeader
// the line it starts with.
const (
	drvUpdate = "go test ./internal/sketch/check -run '^TestDrvBindingIsCurrent$' -update-drv"
	drvHeader = "// Written by ergo bind (gobind.Bind) from drvGo; do not edit. Rewrite it with: " + drvUpdate + "\n"
)

var updateDrv = flag.Bool("update-drv", false, "rewrite "+drvBindingFile+" from drvGo with ergo bind")

// TestDrvBindingIsCurrent holds testdata/drv.binding to what ergo bind
// writes for drvGo now. It builds Go because gobind.Bind reads go list
// and the compiler's -m output (gobind.Derive), which no in-process
// check gives. -update-drv rewrites the file.
func TestDrvBindingIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	dir := t.TempDir()
	for name, text := range map[string]string{"go.mod": "module drv\n\ngo 1.27\n", "drv.go": drvGo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := gobind.Bind(dir, os.Environ(), "go1.27", ".")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := files["drv"]
	if !ok {
		t.Fatalf("ergo bind wrote no binding for drv: %v", slices.Sorted(maps.Keys(files)))
	}
	got = drvHeader + got
	if *updateDrv {
		if err := os.WriteFile(drvBindingFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(drvBindingFile)
	if err != nil || string(want) != got {
		t.Errorf("%s is not what ergo bind writes for drvGo; rewrite it with %s", drvBindingFile, drvUpdate)
	}
}

// newDrvImporter gives drvBindingFile's binding beside Go's standard
// library.
func newDrvImporter(t *testing.T) drvImporter {
	t.Helper()
	data, err := os.ReadFile(drvBindingFile)
	if err != nil {
		t.Fatalf("%v: the checked-in binding is missing; write it with %s", err, drvUpdate)
	}
	binding, ok := strings.CutPrefix(string(data), drvHeader)
	if !ok {
		t.Fatalf("%s does not start with ergo bind's header; rewrite it with %s", drvBindingFile, drvUpdate)
	}
	// The checker takes Clone's receiver whatever the binding writes
	// (hostTakesRecv): an old or edited binding without the own gobind
	// writes still moves it (interop.md, Resources 5).
	edited := strings.Replace(binding, "func (c own *Client) Clone", "func (c *Client) Clone", 1)
	if edited == binding {
		t.Fatalf("the binding no longer writes own on Clone's receiver:\n%s", binding)
	}
	binding = edited
	return drvImporter{check.NewGoBindings(func(path string) (string, bool) {
		if path == "drv" {
			return binding, true
		}
		return gostd.Binding(path)
	})}
}

// dsModel is the reference of the keeps rule: each handle has one owner;
// a call lends it; a Cmd or a Rows keeps the handle it came from until its
// scope ends (a Rows also until its Close); Close and a hand-off of a
// handle are refused while a keeper is live; Clone takes the handle,
// whose result is the same Go value.
type dsModel struct {
	r     *rand.Rand
	lines []string
	ind   string
	n     int
	// handles and keepers in scope, innermost scope last.
	scopes [][]dsVar
}

type dsVar struct {
	name string
	kind byte // 'c' a handle, 'k' a Cmd, 'r' a Rows
	of   string
	dead bool
	// closed: the handle's own Close took it (dead too); else Clone did.
	closed bool
}

func (m *dsModel) w(format string, args ...any) {
	m.lines = append(m.lines, m.ind+fmt.Sprintf(format, args...))
}

func (m *dsModel) vars(kind byte, live bool) []*dsVar {
	var out []*dsVar
	for i := range m.scopes {
		for j := range m.scopes[i] {
			v := &m.scopes[i][j]
			if v.kind == kind && (!live || !v.dead) {
				out = append(out, v)
			}
		}
	}
	return out
}

// kept reports whether a live keeper holds the handle c.
func (m *dsModel) kept(c string) bool {
	for _, k := range append(m.vars('k', true), m.vars('r', true)...) {
		if k.of == c {
			return true
		}
	}
	return false
}

func (m *dsModel) add(v dsVar) {
	m.scopes[len(m.scopes)-1] = append(m.scopes[len(m.scopes)-1], v)
}

func (m *dsModel) name(p string) string {
	m.n++
	return p + strconv.Itoa(m.n)
}

// block writes legal statements only.
func (m *dsModel) block(depth int) {
	for range 1 + m.r.IntN(6) {
		cs := m.vars('c', true)
		k := m.r.IntN(9)
		if len(cs) == 0 {
			k = 0
		}
		var c *dsVar
		if len(cs) > 0 {
			c = cs[m.r.IntN(len(cs))]
		}
		switch k {
		case 0:
			v := m.name("c")
			m.w("let %s = try drv.Dial(%q)", v, v)
			m.w("try %s.Ping()", v)
			m.add(dsVar{name: v, kind: 'c'})
		case 1:
			m.w("try %s.Ping()", c.name)
			m.w("let _ = try %s.Cmd(\"drop\")", c.name) // a keeper dropped where it is made
		case 2:
			v := m.name("k")
			m.w("let %s = try %s.Cmd(%q)", v, c.name, v)
			m.w("sink(try %s.Result())", v)
			m.add(dsVar{name: v, kind: 'k', of: c.name})
		case 3:
			v := m.name("r")
			m.w("let %s = try %s.Query(%q)", v, c.name, v)
			m.w("more(%s.Next())", v)
			m.add(dsVar{name: v, kind: 'r', of: c.name})
		case 4:
			if rs := m.vars('r', true); len(rs) > 0 {
				r := rs[m.r.IntN(len(rs))]
				m.w("try %s.Close()", r.name)
				r.dead = true
			}
		case 5:
			if !m.kept(c.name) {
				m.w("try %s.Close()", c.name)
				c.dead, c.closed = true, true
			}
		case 6:
			if !m.kept(c.name) {
				// Clone's result is Option (the receiver may be a nil
				// pointer); the clone is a handle in its Some arm only.
				v := m.name("c")
				m.w("match try %s.Clone() {", c.name)
				m.w("case None:")
				m.w("\tsink(\"none\")")
				m.w("case Some[go.Ref[drv.Client]] as %s:", v)
				m.w("\ttry %s.Value.Ping()", v)
				m.w("}")
				c.dead = true
			}
		case 7:
			if ks := m.vars('k', false); len(ks) > 0 {
				m.w("sink(try %s.Result())", ks[m.r.IntN(len(ks))].name)
			}
		case 8:
			if depth < 2 {
				m.w("if flag {")
				m.ind += "\t"
				m.scopes = append(m.scopes, nil)
				m.block(depth + 1)
				m.scopes = m.scopes[:len(m.scopes)-1]
				m.ind = m.ind[1:]
				m.w("}")
			}
		}
	}
}

// violation appends one statement the keeps rule refuses, or "" when the
// state has none of the chosen kind.
func (m *dsModel) violation() (string, string) {
	switch m.r.IntN(3) {
	case 0:
		for _, c := range m.vars('c', true) {
			if m.kept(c.name) {
				return "try " + c.name + ".Close()", "a release of a kept handle"
			}
		}
	case 1:
		for _, c := range m.vars('c', false) {
			if c.dead && !c.closed {
				return "try " + c.name + ".Ping()", "a use of a handle Clone took"
			}
		}
	case 2:
		if ks := m.vars('k', false); len(ks) > 0 {
			return "hold(" + ks[0].name + ")", "a kept result passed on"
		}
	}
	return "", ""
}

func dsSource(body []string) string {
	return "import go drv\n\nfunc sink(s string) {}\n\nfunc more(b bool) {}\n\nfunc hold(k go.Ref[drv.Cmd]) {}\n\n" +
		"func run(flag bool) fails go.Error needs go.Foreign {\n" + strings.Join(body, "\n") + "\n}\n\n" +
		"func main() needs go.Foreign {\n\trun(true) catch { case go.Error: sink(\"failed\") }\n}\n"
}

// dsHeader is the number of source lines before run's body.
const dsHeader = 9

// TestDriverHandleIsLent is GB1's checker property: generated bodies over
// a driver-shaped Go package (drvGo) whose handles are used many times,
// with results that keep their handle, check clean exactly when the model
// says so. A clean body uses one handle again and again (a Go method lends
// its receiver), closes a handle once its keepers' scopes ended or their
// Close ran, and clones a handle (taking it, since the result is the same
// Go value). The same body with one refused statement appended after a
// random prefix is S108 at that line: a Close of a handle a live Cmd or
// Rows keeps, a use of a handle Clone took, a kept Cmd passed to an Ergo
// function that may keep it. A failing seed prints the source and the
// replay command (ERGO_SEED=n).
//
// Mutants it alone catches (internal/sketch/check mutants.json, GB1): a Go
// method takes its receiver by default (gb1-recv-taken); no receiver is
// taken for a result that may hold it (gb1-no-alias-take); a Go method's
// result keeps nothing (gb1-no-keeps).
func TestDriverHandleIsLent(t *testing.T) {
	imp := newDrvImporter(t)
	first, n := 1, 160
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	kinds := map[string]bool{}
	for seed := first; seed < first+n; seed++ {
		m := &dsModel{r: rand.New(rand.NewPCG(uint64(seed), 0x6b1)), ind: "\t", scopes: [][]dsVar{nil}}
		m.block(0)
		clean := dsSource(m.lines)
		if ds := dsCheck(t, clean, imp); len(ds) > 0 {
			t.Fatalf("seed %d (ERGO_SEED=%d replays): the clean body: %v\n%s", seed, seed, ds, numberLines(clean))
		}
		bad, kind := m.violation()
		if bad == "" {
			continue
		}
		kinds[kind] = true
		src := dsSource(append(m.lines, "\t"+bad))
		line := dsHeader + len(m.lines) + 1
		ds := dsCheck(t, src, imp)
		if len(ds) == 0 || ds[0].Code != "S108" || ds[0].Line != line {
			t.Errorf("seed %d (ERGO_SEED=%d replays): %s: want S108 at line %d, got %v\n%s", seed, seed, bad, line, ds, numberLines(src))
		}
	}
	if n > 1 && len(kinds) < 3 {
		t.Errorf("the seeds reached the violations %v only; want a Close, a use after Clone and a hold", kinds)
	}
}

func dsCheck(t *testing.T, src string, imp drvImporter) []check.Diagnostic {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, numberLines(src))
	}
	_, ds := check.Check("main", []*syntax.File{f}, imp)
	return ds
}

func numberLines(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%3d %s\n", i+1, l)
	}
	return b.String()
}

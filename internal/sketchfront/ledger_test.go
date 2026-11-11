package sketchfront

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
)

// ledgerProgram is a generated program with one rewrite law, Counter.Joins,
// and its provider Tally, whose Add reaches a chain of depth helpers; its
// main has a window of two Add calls at each pair of sites. An edit flips
// one field.
type ledgerProgram struct {
	depth             int
	mode, extraMode   bool
	addSwap, deepSwap bool
	lawX              bool
	sites             [][2]int
	unrelated, note   int
}

func (p ledgerProgram) source() string {
	var b strings.Builder
	a, c := "a", "b"
	if p.lawX {
		a, c = "x", "y"
	}
	fmt.Fprintf(&b, "import sync\n\ntype Counter {\n\tAdd(n int),\n\tRead() int,\n\n\tlaw Joins(%s int, %s int) { Add(%s); Add(%s) } == { Add(%s + %s) },\n}\n\n", a, c, a, c, a, c)
	field, init := "", ""
	if p.mode {
		extra := ""
		if p.extraMode {
			extra = " | Idle{}"
		}
		fmt.Fprintf(&b, "type Mode = Fast{} | Slow{}%s\n\n", extra)
		field, init = ", m Mode", ", m: Fast"
	}
	fmt.Fprintf(&b, "type Tally { s sync.Ref[int]%s }\n\nfunc NewTally() Tally { Tally{s: sync.NewRef[int](0)%s} }\n\n", field, init)
	if p.addSwap {
		b.WriteString("func Add(t Tally, n int) { t.s.Set(h1(n, t.s.Get())) }\n\n")
	} else {
		b.WriteString("func Add(t Tally, n int) { t.s.Set(h1(t.s.Get(), n)) }\n\n")
	}
	for i := 1; i < p.depth; i++ {
		fmt.Fprintf(&b, "func h%d(a int, b int) int { h%d(a, b) }\n\n", i, i+1)
	}
	if p.deepSwap {
		fmt.Fprintf(&b, "func h%d(a int, b int) int { b + a }\n\n", p.depth)
	} else {
		fmt.Fprintf(&b, "func h%d(a int, b int) int { a + b }\n\n", p.depth)
	}
	fmt.Fprintf(&b, "func Read(t Tally) int { t.s.Get() }\n\nfunc unrelated() int { %d }\n\nfunc main() {\n\tlet t = NewTally()\n", p.unrelated)
	for _, s := range p.sites {
		fmt.Fprintf(&b, "\tt.Add(%d)\n\tt.Add(%d)\n\tlet _ = t.Read()\n", s[0], s[1])
	}
	fmt.Fprintf(&b, "\tlet _ = t.Read() + %d\n}\n", p.note)
	return b.String()
}

// genLedgerProgram is the program of seed: its shape (the helper depth,
// a listed type or none, one site or two) cycles with the seed, so 12
// seeds reach every shape; its constants are drawn.
func genLedgerProgram(seed uint64, r *rand.Rand) ledgerProgram {
	p := ledgerProgram{depth: 1 + int(seed%3), mode: seed/3%2 == 0, unrelated: r.IntN(100), note: r.IntN(100)}
	for range 1 + int(seed/6%2) {
		p.sites = append(p.sites, [2]int{r.IntN(50), r.IntN(50)})
	}
	return p
}

// ledgerEdit is a generated edit and the reference model's answer: whether
// the base ledger's entry is still current after it. The model is the
// plan's staleness rule (plan-2 §3), read from what the edit touches, not
// from LawDigest: an edit of a function the law's provider reaches, at any
// depth, of the law, of a type the provider lists, or a site constant the
// entry does not list, makes the entry stale; an edit elsewhere does not.
type ledgerEdit struct {
	name    string
	current bool
	apply   func(*ledgerProgram)
	// handEdit, when set, also edits the ledger's line by hand: it lists
	// the new site's constants without a fresh digest.
	handEdit bool
	// siteOnly marks an edit that makes the entry stale at the first
	// site only: every other site is still listed and current.
	siteOnly bool
}

func ledgerEdits(p ledgerProgram) []ledgerEdit {
	edits := []ledgerEdit{
		{name: "an operation's body", apply: func(q *ledgerProgram) { q.addSwap = true }},
		{name: fmt.Sprintf("the helper %d calls below the operation", p.depth), apply: func(q *ledgerProgram) { q.deepSwap = true }},
		{name: "the law", apply: func(q *ledgerProgram) { q.lawX = true }},
		{name: "a new site constant", siteOnly: true, apply: func(q *ledgerProgram) { q.sites = append([][2]int(nil), q.sites...); q.sites[0][0] += 50 }},
		{name: "a new site constant listed by hand", handEdit: true, apply: func(q *ledgerProgram) { q.sites = append([][2]int(nil), q.sites...); q.sites[0][0] += 50 }},
		{name: "an unrelated function", current: true, apply: func(q *ledgerProgram) { q.unrelated++ }},
		{name: "main away from the sites", current: true, apply: func(q *ledgerProgram) { q.note++ }},
	}
	if p.mode {
		edits = append(edits, ledgerEdit{name: "a listed type", apply: func(q *ledgerProgram) { q.extraMode = true }})
	}
	return edits
}

// fakeLawChecks stands in for the check build: every law the ledger run
// asks for is proved for Tally, so the test runs in process and the
// ledger's entries are the writer's.
func fakeLawChecks(BuildRequest, func() (build.Input, error)) func([]lower.File, []string) (string, error) {
	return func(_ []lower.File, laws []string) (string, error) {
		var b strings.Builder
		for _, l := range laws {
			b.WriteString("ok law " + l + " for Tally (proved: in process)\n")
		}
		return b.String(), nil
	}
}

// lowered is the run build of a's source with the ledger at path: the
// program's files (the producer record apart), the record, the lines it
// printed and its error.
func lowered(t *testing.T, a Analysis, path string, locked bool) (program, record string, notes []string, err error) {
	t.Helper()
	req := BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path, Locked: locked}
	unit, err := analysisUnit(a, req.Source)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	req.Stderr = &stderr
	ev, files, _, err := lowerWithLedger(a, unit, req)
	if err != nil {
		return "", "", nil, err
	}
	if ev == nil {
		t.Fatal("the program has no rewrite law the build reads")
	}
	var b strings.Builder
	for _, f := range files {
		if f.Path == "ergo_laws.go" {
			record = string(f.Data)
			continue
		}
		b.WriteString(f.Path + "\n" + string(f.Data))
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		notes = strings.Split(s, "\n")
	}
	return b.String(), record, notes, nil
}

func checked(t *testing.T, src string) Analysis {
	t.Helper()
	a := Check(src)
	if len(a.Diagnostics) > 0 {
		t.Fatalf("%v\n%s", a.Diagnostics, src)
	}
	return a
}

// TestLedgerGatesRewrites is the ledger's property (runtime.md, Tests 8;
// plan-2 §3): over generated programs and generated edits, the run build
// rewrites the law's sites exactly when the reference model says the base
// ledger's entry is current. With a stale entry, the program is
// byte-equal to the build with no ledger, the build prints one line that
// names the entry, a locked build refuses with it, and the build leaves
// the ledger as it was; ergo test's writer then changes one entry, and
// after an edit the model calls current it changes none. The same sources
// and ledger lower to the same program. In process: the ledger's check run
// is replaced (fakeLawChecks), and the lowered Go text is compared.
//
// ERGO_LEDGER_SEED=N replays one seed.
func TestLedgerGatesRewrites(t *testing.T) {
	saved, savedChecks := compilerIdentity, lawChecks
	compilerIdentity = func() (string, error) { return "test", nil }
	lawChecks = fakeLawChecks
	defer func() { compilerIdentity, lawChecks = saved, savedChecks }()
	seeds := []uint64{}
	if s := os.Getenv("ERGO_LEDGER_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(12) {
			seeds = append(seeds, i+1)
		}
	}
	const stale = "ergo.laws: main.Counter.Joins for main.Tally is stale; run ergo test"
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1ed6e7))
		p := genLedgerProgram(seed, r)
		src := p.source()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_LEDGER_SEED=%d go test -run TestLedgerGatesRewrites ./internal/sketchfront): %s\nbase source:\n%s", seed, seed, fmt.Sprintf(format, args...), src)
		}
		base := checked(t, src)
		dir := t.TempDir()
		path := filepath.Join(dir, lower.LedgerFile)
		req := BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path}
		if n, err := UpdateLedger(base, req); err != nil || n != 1 {
			fail("ergo test's writer: %d entries changed, %v; want 1", n, err)
		}
		ledger, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ref, _, missing, _ := lowered(t, base, "", false)
		want := "ergo.laws: main.Counter.Joins for main.Tally is missing; run ergo test"
		if len(missing) != 1 || missing[0] != want {
			fail("with no ledger the build prints %q, want [%q]", missing, want)
		}
		got, rec, notes, _ := lowered(t, base, path, false)
		again, _, _, _ := lowered(t, base, path, false)
		switch {
		case len(notes) > 0:
			fail("a current ledger prints %q", notes)
		case got == ref:
			fail("a current, proved entry does not rewrite:\n%s", got)
		case got != again:
			fail("the same sources and ledger lower to two programs")
		case !strings.Contains(rec, "ergo.laws sha256:"+sha256Hex(string(ledger))) || strings.Contains(rec, "rewrites applied 0"):
			fail("the producer record does not name the ledger and the rewrites applied:\n%s", rec)
		}
		for _, e := range ledgerEdits(p) {
			q := p
			e.apply(&q)
			edited := checked(t, q.source())
			at := path
			if e.handEdit {
				at = filepath.Join(t.TempDir(), lower.LedgerFile)
				line := strings.TrimSuffix(string(ledger), "\n")
				k := strings.LastIndex(line, " h1:")
				hand := line[:k] + fmt.Sprintf(" (%d, %d)", q.sites[0][0], q.sites[0][1]) + line[k:] + "\n"
				if !strings.Contains(line, " at (") {
					fail("the entry lists no site: %s", line)
				}
				if err := os.WriteFile(at, []byte(hand), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(at)
			ref, _, _, _ := lowered(t, edited, "", false)
			if e.siteOnly {
				// The other sites apply: the reference is the build with the
				// ledger of the program without the first site.
				rest := q
				rest.sites = q.sites[1:]
				other := filepath.Join(t.TempDir(), lower.LedgerFile)
				if _, err := UpdateLedger(checked(t, rest.source()), BuildRequest{Mode: build.ModeBuild, Source: req.Source, Ledger: other}); err != nil {
					t.Fatal(err)
				}
				ref, _, _, _ = lowered(t, edited, other, false)
			}
			got, _, notes, _ := lowered(t, edited, at, false)
			after, _ := os.ReadFile(at)
			if !bytes.Equal(before, after) {
				fail("edit %s: the build wrote the ledger", e.name)
			}
			if e.current {
				if got == ref || len(notes) > 0 {
					fail("edit %s: the model says current, the build applied %v and printed %q", e.name, got != ref, notes)
				}
				if n, err := UpdateLedger(edited, BuildRequest{Mode: build.ModeBuild, Source: req.Source, Ledger: at}); err != nil || n != 0 {
					fail("edit %s: the writer changed %d entries (%v), want 0", e.name, n, err)
				}
				continue
			}
			if got != ref {
				fail("edit %s: the model says stale, and the build differs from the build with no ledger:\n%s\n----\n%s", e.name, got, ref)
			}
			if len(notes) != 1 || notes[0] != stale {
				fail("edit %s: the build prints %q, want [%q]", e.name, notes, stale)
			}
			var refusal *LedgerError
			if _, _, _, err := lowered(t, edited, at, true); !errors.As(err, &refusal) || !strings.Contains(err.Error(), stale) {
				fail("edit %s: a locked build gives %v, want the refusal naming the entry", e.name, err)
			}
			fresh := filepath.Join(t.TempDir(), lower.LedgerFile)
			if err := os.WriteFile(fresh, before, 0o644); err != nil {
				t.Fatal(err)
			}
			if n, err := UpdateLedger(edited, BuildRequest{Mode: build.ModeBuild, Source: req.Source, Ledger: fresh}); err != nil || n != 1 {
				fail("edit %s: the writer changed %d entries (%v), want 1", e.name, n, err)
			}
			if got, _, notes, _ := lowered(t, edited, fresh, false); got == ref || len(notes) > 0 {
				fail("edit %s: after ergo test the build still keeps the reference (%q)", e.name, notes)
			}
		}
	}
}

// TestStdLedgerFresh: the embedded std ledger is the one a fresh run
// writes (go run ./scripts/stdlaws); std has no other evidence path.
func TestStdLedgerFresh(t *testing.T) {
	saved := compilerIdentity
	compilerIdentity = func() (string, error) { return "test", nil }
	defer func() { compilerIdentity = saved }()
	if testing.Short() {
		// A std package with a rewrite law needs its check build.
		savedChecks := lawChecks
		lawChecks = func(BuildRequest, func() (build.Input, error)) func([]lower.File, []string) (string, error) {
			return func([]lower.File, []string) (string, error) {
				t.Skip("a std rewrite law needs its check build (full tier)")
				return "", nil
			}
		}
		defer func() { lawChecks = savedChecks }()
	}
	root := t.TempDir()
	fresh, err := FreshStdLedger(BuildRequest{Cache: build.Cache{Root: root}, Toolchain: build.HostToolchain()})
	if err != nil {
		t.Fatal(err)
	}
	if fresh != std.Ledger() {
		t.Fatalf("internal/sketch/std/ergo.laws is not a fresh run's: run go run ./scripts/stdlaws\nfresh:\n%s\nfile:\n%s", fresh, std.Ledger())
	}
}

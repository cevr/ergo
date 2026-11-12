package sketchfront

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
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
	// lawName renames the law (Joins when empty); noLaw deletes it.
	lawName         string
	noLaw           bool
	unrelated, note int
}

func (p ledgerProgram) source() string {
	var b strings.Builder
	a, c := "a", "b"
	if p.lawX {
		a, c = "x", "y"
	}
	name := p.lawName
	if name == "" {
		name = "Joins"
	}
	law := fmt.Sprintf("\n\tlaw %s(%s int, %s int) { Add(%s); Add(%s) } == { Add(%s + %s) },\n", name, a, c, a, c, a, c)
	if p.noLaw {
		law = ""
	}
	fmt.Fprintf(&b, "import sync\n\ntype Counter {\n\tAdd(n int),\n\tRead() int,\n%s}\n\n", law)
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
	// handEdit, when set, also edits the ledger by hand: it adds a line
	// for the new site's constants with another site's digest.
	handEdit bool
	// siteOnly marks an edit that leaves the first site's entry missing
	// (or stale, with handEdit): every other site's entry is current.
	siteOnly bool
}

func ledgerEdits(p ledgerProgram) []ledgerEdit {
	edits := []ledgerEdit{
		{name: "an operation's body", apply: func(q *ledgerProgram) { q.addSwap = true }},
		{name: fmt.Sprintf("the helper %d calls below the operation", p.depth), apply: func(q *ledgerProgram) { q.deepSwap = true }},
		{name: "the law", apply: func(q *ledgerProgram) { q.lawX = true }},
		{name: "a new site constant", siteOnly: true, apply: func(q *ledgerProgram) { q.sites = append([][2]int(nil), q.sites...); q.sites[0][0] += 50 }},
		{name: "a new site constant listed by hand", siteOnly: true, handEdit: true, apply: func(q *ledgerProgram) { q.sites = append([][2]int(nil), q.sites...); q.sites[0][0] += 50 }},
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
	ev, files, err := lowerWithLedger(a, unit, req)
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

// siteInstances are p's distinct site instances, as a ledger names them.
func (p ledgerProgram) siteInstances() []string {
	var out []string
	for _, s := range p.sites {
		if in := fmt.Sprintf("(%d, %d)", s[0], s[1]); !slices.Contains(out, in) {
			out = append(out, in)
		}
	}
	return out
}

// ledgerNote is the line a build prints for the entry of instance in.
func ledgerNote(in, state string) string {
	return "ergo.laws: main.Counter.Joins for main.Tally at " + in + " is " + state + "; run ergo test"
}

// TestLedgerGatesRewrites is the ledger's property (runtime.md, Tests 10;
// plan-2 §3): over generated programs and generated edits, the run build
// rewrites the law's sites exactly when the reference model says the base
// ledger's entry is current. With a stale entry, the program is
// byte-equal to the build with no ledger, the build prints one line that
// names each entry that is stale or missing, a locked build refuses with
// it, and the build leaves the ledger as it was; ergo test's writer then
// changes exactly those entries (and the law's own), and after an edit
// the model calls current it changes none. The producer record names the
// entries the build read. The same sources
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
	notes := func(q ledgerProgram, state string) []string {
		var out []string
		for _, in := range q.siteInstances() {
			out = append(out, ledgerNote(in, state))
		}
		slices.Sort(out)
		return out
	}
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
		// One entry for the law and one per site instance.
		if n, err := UpdateLedger(base, req); err != nil || n != 1+len(p.siteInstances()) {
			fail("ergo test's writer: %d entries changed, %v; want %d", n, err, 1+len(p.siteInstances()))
		}
		ledger, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ref, _, missing, _ := lowered(t, base, "", false)
		if want := notes(p, "missing"); !slices.Equal(missing, want) {
			fail("with no ledger the build prints %q, want %q", missing, want)
		}
		got, rec, printed, _ := lowered(t, base, path, false)
		again, _, _, _ := lowered(t, base, path, false)
		// The build reads the entries of its sites: here every line but the
		// law's own.
		var read []string
		for _, line := range strings.SplitAfter(string(ledger), "\n") {
			if strings.Contains(line, " at (") {
				read = append(read, line)
			}
		}
		switch {
		case len(printed) > 0:
			fail("a current ledger prints %q", printed)
		case got == ref:
			fail("a current, proved entry does not rewrite:\n%s", got)
		case got != again:
			fail("the same sources and ledger lower to two programs")
		case !strings.Contains(rec, "ergo.laws entries sha256:"+sha256Hex(strings.Join(read, ""))) || strings.Contains(rec, "rewrites applied 0"):
			fail("the producer record does not name the entries read and the rewrites applied:\n%s", rec)
		}
		for _, e := range ledgerEdits(p) {
			q := p
			e.apply(&q)
			edited := checked(t, q.source())
			at := path
			if e.handEdit {
				at = filepath.Join(t.TempDir(), lower.LedgerFile)
				old := fmt.Sprintf(" at (%d, %d) ", p.sites[0][0], p.sites[0][1])
				i := strings.Index(string(ledger), old)
				if i < 0 {
					fail("the ledger has no entry for the first site:\n%s", ledger)
				}
				start := strings.LastIndex(string(ledger[:i]), "\n") + 1
				line := string(ledger[start:]) // the old site's line and the rest
				line = line[:strings.Index(line, "\n")+1]
				hand := string(ledger) + strings.Replace(line, old, fmt.Sprintf(" at (%d, %d) ", q.sites[0][0], q.sites[0][1]), 1)
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
			got, _, printed, _ := lowered(t, edited, at, false)
			after, _ := os.ReadFile(at)
			if !bytes.Equal(before, after) {
				fail("edit %s: the build wrote the ledger", e.name)
			}
			if e.current {
				if got == ref || len(printed) > 0 {
					fail("edit %s: the model says current, the build applied %v and printed %q", e.name, got != ref, printed)
				}
				if n, err := UpdateLedger(edited, BuildRequest{Mode: build.ModeBuild, Source: req.Source, Ledger: at}); err != nil || n != 0 {
					fail("edit %s: the writer changed %d entries (%v), want 0", e.name, n, err)
				}
				continue
			}
			if got != ref {
				fail("edit %s: the model says stale, and the build differs from the build with no ledger:\n%s\n----\n%s", e.name, got, ref)
			}
			// A site edit leaves one entry missing (stale, when listed by
			// hand) and one entry to write; any other edit makes every
			// site's entry and the law's own stale.
			want, changes := notes(q, "stale"), 1+len(q.siteInstances())
			if e.siteOnly {
				state := "missing"
				if e.handEdit {
					state = "stale"
				}
				want, changes = []string{ledgerNote(q.siteInstances()[0], state)}, 1
			}
			if !slices.Equal(printed, want) {
				fail("edit %s: the build prints %q, want %q", e.name, printed, want)
			}
			var refusal *LedgerError
			if _, _, _, err := lowered(t, edited, at, true); !errors.As(err, &refusal) || !strings.Contains(err.Error(), want[0]) {
				fail("edit %s: a locked build gives %v, want the refusal naming the entry", e.name, err)
			}
			fresh := filepath.Join(t.TempDir(), lower.LedgerFile)
			if err := os.WriteFile(fresh, before, 0o644); err != nil {
				t.Fatal(err)
			}
			if n, err := UpdateLedger(edited, BuildRequest{Mode: build.ModeBuild, Source: req.Source, Ledger: fresh}); err != nil || n != changes {
				fail("edit %s: the writer changed %d entries (%v), want %d", e.name, n, err, changes)
			}
			if got, _, printed, _ := lowered(t, edited, fresh, false); got == ref || len(printed) > 0 {
				fail("edit %s: after ergo test the build still keeps the reference (%q)", e.name, printed)
			}
		}
	}
}

// TestLedgerPrunes is the row of the writer's pruning (runtime.md, Tests
// 10): ergo test removes the entries of a law the sources no longer
// declare, so a renamed law's old entries and a deleted law's entries go,
// and each removal counts as a change.
//
// Mutant it kills (TestLedgerGatesRewrites never renames or deletes a
// law): ll1-prune-off, the writer keeps entries no law gives.
func TestLedgerPrunes(t *testing.T) {
	saved, savedChecks := compilerIdentity, lawChecks
	compilerIdentity = func() (string, error) { return "test", nil }
	lawChecks = fakeLawChecks
	defer func() { compilerIdentity, lawChecks = saved, savedChecks }()
	path := filepath.Join(t.TempDir(), lower.LedgerFile)
	write := func(p ledgerProgram) (int, string) {
		t.Helper()
		n, err := UpdateLedger(checked(t, p.source()), BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path})
		if err != nil {
			t.Fatal(err)
		}
		text, _ := os.ReadFile(path)
		return n, string(text)
	}
	p := ledgerProgram{depth: 1, sites: [][2]int{{2, 3}}}
	if n, text := write(p); n != 2 || !strings.HasPrefix(text, "law main.Counter.Joins for main.Tally ") {
		t.Fatalf("the first write: %d changed (want 2: the law's and its site's), ledger %q", n, text)
	}
	p.lawName = "Sums"
	if n, text := write(p); n != 4 || strings.Count(text, "\n") != 2 || strings.Contains(text, "Joins") {
		t.Fatalf("after a rename: %d changed (want 4: two added, two removed), ledger %q", n, text)
	}
	p.noLaw = true
	if n, text := write(p); n != 2 || text != "" {
		t.Fatalf("after the law is deleted: %d changed (want 2), ledger %q", n, text)
	}
}

// moduleBinary is the main package of a binary of the module m: its own
// provider Tally of m/count's law Counter.Joins, whose Add adds its
// argument first when swap is set, and a window of two Add calls at each
// pair of sites.
func moduleBinary(swap bool, sites [][2]int) string {
	var b strings.Builder
	sum := "t.s.Get() + n"
	if swap {
		sum = "n + t.s.Get()"
	}
	fmt.Fprintf(&b, "import sync\nimport m/count\n\ntype Tally { s sync.Ref[int] }\n\nfunc NewTally() Tally { Tally{s: sync.NewRef[int](0)} }\n\nfunc Add(t Tally, n int) { t.s.Set(%s) }\n\nfunc Read(t Tally) int { t.s.Get() }\n\nfunc main() {\n\tlet t = NewTally()\n", sum)
	for _, s := range sites {
		fmt.Fprintf(&b, "\tt.Add(%d)\n\tt.Add(%d)\n\tlet _ = t.Read()\n", s[0], s[1])
	}
	b.WriteString("\tlet _ = t.Read() + count.Zero()\n}\n")
	return b.String()
}

// TestLedgerModuleOrder is the module ledger's property (runtime.md, Tests
// 10; owner q116: ergo.laws is module-wide, as go.sum is): over generated
// modules of two binaries that share a law, each with its own provider
// and site constants, every order of ergo test over the binaries writes
// the same ledger, and each binary's run build (its program and its
// producer record) is the same after any other binary's ergo test as
// after its own alone. The writer removes the entries of a binary the
// module no longer has and of a law it no longer declares, and keeps an
// entry of a declared law that no run took. In process: the check run is
// replaced (fakeLawChecks).
//
// Mutants it kills (TestLedgerGatesRewrites and TestLedgerPrunes test one
// binary): ll1-prune-by-run, the writer drops each entry its run did not
// take, so the last binary tested wins; ll1-main-name, a binary's main
// package is named main, so two binaries' entries collide.
//
// ERGO_LEDGER_SEED=N replays one seed.
func TestLedgerModuleOrder(t *testing.T) {
	saved, savedChecks := compilerIdentity, lawChecks
	compilerIdentity = func() (string, error) { return "test", nil }
	lawChecks = fakeLawChecks
	defer func() { compilerIdentity, lawChecks = saved, savedChecks }()
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if s := os.Getenv("ERGO_LEDGER_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	// A seed ledger: an entry of a binary the module does not have, one of
	// a law it does not declare, and one of a declared law at constants no
	// site has.
	const gone = "law m/count.Counter.Joins for m/cmd/gone.Tally proved h1:AAAA\n"
	const old = "law m/count.Counter.Old for m/cmd/a.Tally proved h1:AAAA\n"
	const unused = "law m/count.Counter.Joins for m/cmd/a.Tally at (98, 99) proved h1:AAAA\n"
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1ed6e8))
		draw := func() [][2]int {
			var out [][2]int
			for range 1 + r.IntN(2) {
				out = append(out, [2]int{r.IntN(50), r.IntN(50)})
			}
			return out
		}
		files := map[string]string{
			"ergo.mod":         "module m\n",
			"count/count.ergo": "type Counter {\n\tAdd(n int),\n\tRead() int,\n\tlaw Joins(a int, b int) { Add(a); Add(b) } == { Add(a + b) },\n}\n\nfunc Zero() int { 0 }\n",
			"cmd/a/main.ergo":  moduleBinary(false, draw()),
			"cmd/b/main.ergo":  moduleBinary(true, draw()),
		}
		dir := writeTree(t, files)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_LEDGER_SEED=%d go test -run TestLedgerModuleOrder ./internal/sketchfront): %s\ncmd/a:\n%s\ncmd/b:\n%s", seed, seed, fmt.Sprintf(format, args...), files["cmd/a/main.ergo"], files["cmd/b/main.ergo"])
		}
		bins := map[string]Analysis{}
		for _, name := range []string{"a", "b"} {
			a, err := CheckDir(filepath.Join(dir, "cmd", name))
			if err != nil || len(a.Diagnostics) > 0 {
				t.Fatalf("cmd/%s: %v %v", name, err, a.Diagnostics)
			}
			bins[name] = a
		}
		type built struct{ program, record string }
		build1 := func(name, ledger string) built {
			program, record, printed, err := lowered(t, bins[name], ledger, false)
			if err != nil || len(printed) > 0 {
				fail("cmd/%s builds with %v and prints %q", name, err, printed)
			}
			return built{program, record}
		}
		// alone is each binary's build after its own ergo test alone.
		alone := map[string]built{}
		for name := range bins {
			path := filepath.Join(t.TempDir(), lower.LedgerFile)
			if _, err := UpdateLedger(bins[name], BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path}); err != nil {
				t.Fatal(err)
			}
			alone[name] = build1(name, path)
		}
		var first string
		for _, order := range [][]string{{"a", "b"}, {"b", "a"}, {"a", "b", "a"}, {"b", "a", "b"}, {"b", "b", "a"}} {
			path := filepath.Join(t.TempDir(), lower.LedgerFile)
			if err := os.WriteFile(path, []byte(gone+old+unused), 0o644); err != nil {
				t.Fatal(err)
			}
			tested := map[string]bool{}
			for _, name := range order {
				if _, err := UpdateLedger(bins[name], BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path}); err != nil {
					fail("ergo test of cmd/%s: %v", name, err)
				}
				tested[name] = true
				for other := range tested {
					if got := build1(other, path); got != alone[other] {
						fail("order %v: after ergo test of cmd/%s, the build of cmd/%s differs from its build after its own test alone:\n%s\n%s\n----\n%s\n%s", order, name, other, got.record, got.program, alone[other].record, alone[other].program)
					}
				}
			}
			text, _ := os.ReadFile(path)
			switch {
			case first == "":
				first = string(text)
				if strings.Contains(first, gone) || strings.Contains(first, old) || !strings.Contains(first, unused) {
					fail("the writer keeps an entry the module does not declare, or drops one it does:\n%s", first)
				}
			case string(text) != first:
				fail("order %v writes another ledger:\n%s\n----\n%s", order, text, first)
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

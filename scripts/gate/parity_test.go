package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/scripts/gate/replay"
)

// The gate reuses results at three layers: Go's test cache inside a go test
// step (only in the hermetic exception, Step.Hermetic: every other step runs
// its go tests with -count=1 in GOFLAGS), a step's receipt (Inputs) and the
// whole-gate record. Each layer must key every input the result read, or an
// incremental gate replays a pass that a fresh run fails.
// TestGateCacheParityModel runs the gate on a small checkout, with real Go
// test caching and the real mapping validator, and compares it after each
// invocation with a reference model that knows which inputs each layer keys.

// parityFixture is a checkout shaped like the parts of the repository the
// two repaired steps read:
//
//   - internal/sketch/syntax/probe.go, the syntax source, good (it declares
//     the field Value) or bad (the field is renamed). Each version has its
//     own modification time, so the size and time cmd/go rechecks tell them
//     apart as their content does;
//   - tools/provenance, a nested module whose TestControls reads that source
//     in its own process, across the module boundary, and whose
//     cmd/provenance is the analyzer the step builds and runs;
//   - internal/probe, a main-module package whose test reads the source only
//     through a child process (grep), under testinputs.Main;
//   - scripts/conformance and internal/testinputs, copied from this
//     checkout, and conformance/effect-cases.json with one evidence pointer
//     to evidence/evidence_test.go, which is tracked or not (the index);
//     the real mapping's own claims are dropped (fixtureMapping).
type parityFixture struct {
	root string
	// good and bad are the times of the two source versions.
	good, bad time.Time
}

const (
	paritySource   = "internal/sketch/syntax/probe.go"
	parityEvidence = "evidence/evidence_test.go"
	// parityEpoch is the variable both tests read, so Go's test cache keys
	// their results on it; a sweep moves to a new epoch.
	parityEpoch = "ERGO_PARITY_EPOCH"
)

func newParityFixture(t *testing.T) *parityFixture {
	t.Helper()
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	copyGoFiles := func(directory string) {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(repository, directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(repository, directory, name))
			if err != nil {
				t.Fatal(err)
			}
			write(t, root, filepath.Join(directory, name), string(content))
		}
	}
	gomod, err := os.ReadFile(filepath.Join(repository, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "go.mod", string(gomod))
	for _, directory := range []string{"internal/testinputs", "internal/testinputs/testenv", "scripts/conformance"} {
		copyGoFiles(directory)
	}
	// The real mapping, with its first case covered by the fixture's
	// evidence test. The fixture holds none of the real evidence tests, so
	// every real claim is dropped: its case is pending with no evidence.
	content, err := os.ReadFile(filepath.Join(repository, "conformance", "effect-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	mapping := fixtureMapping(t, content)
	encoded, err := json.MarshalIndent(mapping, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	firstID := mapping["cases"].([]any)[0].(map[string]any)["id"].(string)
	write(t, root, "conformance/effect-cases.json", string(encoded)+"\n")
	write(t, root, parityEvidence, "package evidence\n\nimport \"testing\"\n\nfunc TestEvidence(t *testing.T) {\n\tt.Run("+strconv.Quote(firstID)+", func(t *testing.T) {})\n}\n")
	write(t, root, provenanceManifest, "")
	write(t, root, "internal/probe/probe_test.go", `package probe

import (
	"os"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestChildRead reads the syntax source only through a child process.
func TestChildRead(t *testing.T) {
	_ = os.Getenv("`+parityEpoch+`")
	if output, err := exec.Command("grep", "-c", "Value int", "../sketch/syntax/probe.go").CombinedOutput(); err != nil {
		t.Fatalf("the child found no field Value: %v %s", err, output)
	}
}
`)
	write(t, root, "tools/provenance/go.mod", "module ergo.local/prototype/tools/provenance\n\ngo 1.27\n")
	write(t, root, "tools/provenance/controls_test.go", `package provenance

import (
	"os"
	"strings"
	"testing"
)

// TestControls reads a main-module source in its own process, outside its
// module, as the real controls copy the checker's sources.
func TestControls(t *testing.T) {
	_ = os.Getenv("`+parityEpoch+`")
	source, err := os.ReadFile("../../internal/sketch/syntax/probe.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "Value int") {
		t.Fatal("x.Value undefined")
	}
}
`)
	write(t, root, "tools/provenance/cmd/provenance/main.go", "package main\n\nfunc main() {}\n")
	f := &parityFixture{root: root, good: time.Now().Add(-time.Hour)}
	f.bad = f.good.Add(-10 * time.Minute)
	f.setSource(t, true)
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "parity fixture")
	// Every file is old, so cmd/go may cache a test that read it.
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if name == filepath.Join(root, paritySource) {
			return nil
		}
		return os.Chtimes(name, f.good, f.good)
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// fixtureMapping is the mapping content with every case that claims
// evidence made pending with none, and its first case a difference proved
// by the fixture's evidence test.
func fixtureMapping(t *testing.T, content []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var mapping map[string]any
	if err := decoder.Decode(&mapping); err != nil {
		t.Fatal(err)
	}
	for _, item := range mapping["cases"].([]any) {
		if c := item.(map[string]any); len(c["evidence"].([]any)) > 0 {
			c["status"], c["evidence"] = "pending", []any{}
		}
	}
	first := mapping["cases"].([]any)[0].(map[string]any)
	first["status"] = "difference"
	first["evidence"] = []any{map[string]any{"file": parityEvidence, "test": "TestEvidence", "targets": []any{"go"}}}
	return mapping
}

func (f *parityFixture) setSource(t *testing.T, good bool) {
	t.Helper()
	field, at := "Value", f.good
	if !good {
		field, at = "Other", f.bad
	}
	write(t, f.root, paritySource, "package syntax\n\ntype Probe struct{ "+field+" int }\n")
	if err := os.Chtimes(filepath.Join(f.root, paritySource), at, at); err != nil {
		t.Fatal(err)
	}
}

func (f *parityFixture) setTracked(t *testing.T, tracked bool) {
	t.Helper()
	if tracked {
		gitIn(t, f.root, "add", "--", parityEvidence)
		return
	}
	gitIn(t, f.root, "update-index", "--force-remove", "--", parityEvidence)
}

// The fixture's step names.
const (
	parityProvenance = "provenance analyzer"
	parityObserved   = "go test (other packages)"
	parityMapping    = "effect conformance mapping"
)

// parityConfig selects the steps: the gate's own, or a negative control
// that restores one of the two reuses this unit removed.
type parityConfig struct {
	name string
	// cachedControls puts the provenance step in the hermetic exception
	// (Step.Hermetic), which is not hermetic, and drops its own -count=1, so
	// Go's test cache may replay its controls.
	cachedControls bool
	// mappingReceipt gives the mapping step its old Inputs.
	mappingReceipt bool
}

// design says whether c is the gate's own declaration.
func (c parityConfig) design() bool { return !c.cachedControls && !c.mappingReceipt }

func (c parityConfig) steps(t *testing.T, root string, epoch int) []*Step {
	t.Helper()
	env := []string{fmt.Sprintf("%s=%d", parityEpoch, epoch)}
	provenance := &Step{Name: parityProvenance, Argv: provenanceAnalyzer(root), Env: env}
	if c.cachedControls {
		script := provenance.Argv[2]
		if !strings.Contains(script, "go test -C tools/provenance -count=1 ") {
			t.Fatalf("the provenance script has no uncached controls to restore the cache of: %q", script)
		}
		provenance.Argv = slices.Clone(provenance.Argv)
		provenance.Argv[2] = strings.Replace(script, "go test -C tools/provenance -count=1 ", "go test -C tools/provenance ", 1)
		provenance.Hermetic = true
	}
	mapping := effectMappingStep()
	if mapping.Name != parityMapping {
		t.Fatalf("the mapping step is %q", mapping.Name)
	}
	if c.mappingReceipt {
		mapping.Inputs = &Inputs{Paths: []string{"scripts/conformance/", "conformance/effect-cases.json", "go.mod", "evidence/"}}
	}
	observed := &Step{Name: parityObserved, Argv: []string{"go", "test", testTimeout, "ergo.local/prototype/internal/probe"}, Env: env}
	return []*Step{provenance, observed, mapping}
}

// parityModel is the reference: the inputs, and what each layer holds. It
// never runs a command.
type parityModel struct {
	config parityConfig
	// source is the syntax source's version, tracked the evidence file's
	// index entry.
	source, tracked bool
	// controls is a cached pass of the provenance controls, which only the
	// control that puts them in the hermetic exception makes. cmd/go keys it
	// on nothing the source changes: the source is outside their module.
	// internal/probe, which reads the source through a child, runs with the
	// uncached default, so Go's test cache holds no pass of it.
	controls bool
	// receipt is a mapping receipt; its key holds file contents, not the
	// index.
	receipt bool
	// records are the whole-gate records by input state.
	records map[[2]bool]bool
}

// parityRun is what the model predicts for one gate invocation.
type parityRun struct {
	replayed bool
	verdicts map[string]replay.Verdict
	// hits are the steps that pass from an inner cache: Go's test cache or
	// a step receipt.
	hits map[string]bool
}

func (r parityRun) passed() bool {
	for _, verdict := range r.verdicts {
		if verdict != replay.Pass {
			return false
		}
	}
	return true
}

func verdictOf(pass bool) replay.Verdict {
	if pass {
		return replay.Pass
	}
	return replay.Failed
}

// fresh is what an uncached run gives at the current inputs.
func (m *parityModel) fresh() map[string]replay.Verdict {
	return map[string]replay.Verdict{parityProvenance: verdictOf(m.source), parityObserved: verdictOf(m.source), parityMapping: verdictOf(m.tracked)}
}

func (m *parityModel) gate() parityRun {
	key := [2]bool{m.source, m.tracked}
	if m.records[key] {
		return parityRun{replayed: true, verdicts: map[string]replay.Verdict{parityProvenance: replay.Pass, parityObserved: replay.Pass, parityMapping: replay.Pass}}
	}
	run := parityRun{verdicts: map[string]replay.Verdict{}, hits: map[string]bool{}}
	switch {
	case m.config.cachedControls && m.controls:
		run.verdicts[parityProvenance], run.hits[parityProvenance] = replay.Pass, true
	default:
		run.verdicts[parityProvenance] = verdictOf(m.source)
		m.controls = m.controls || m.config.cachedControls && m.source
	}
	run.verdicts[parityObserved] = verdictOf(m.source)
	switch {
	case m.config.mappingReceipt && m.receipt:
		run.verdicts[parityMapping], run.hits[parityMapping] = replay.Pass, true
	default:
		run.verdicts[parityMapping] = verdictOf(m.tracked)
		m.receipt = m.receipt || m.config.mappingReceipt && m.tracked
	}
	// A failed run withdraws the record at its key; a passed one records.
	m.records[key] = run.passed()
	return run
}

// merge is the merge tier: the inner tier when no record exists, then every
// step uncached, compared with the record. It predicts the inner run (or
// nil), the steps whose verdicts differ (nil when the inner tier failed) and
// the exit code.
func (m *parityModel) merge() (*parityRun, []string, int) {
	key := [2]bool{m.source, m.tracked}
	var inner *parityRun
	if !m.records[key] {
		run := m.gate()
		inner = &run
		if !run.passed() {
			return inner, nil, 1
		}
	}
	// --no-cache still records a receipt for a pass.
	m.receipt = m.receipt || m.config.mappingReceipt && m.tracked
	differ := []string{}
	for name, verdict := range m.fresh() {
		if verdict != replay.Pass {
			differ = append(differ, name)
		}
	}
	slices.Sort(differ)
	if len(differ) > 0 {
		m.records[key] = false
		return inner, differ, 1
	}
	return inner, differ, 0
}

func (m *parityModel) sweep() {
	m.controls, m.receipt = false, false
	m.records = map[[2]bool]bool{}
}

// parityHarness runs the real gate on the fixture.
type parityHarness struct {
	t       *testing.T
	fixture *parityFixture
	config  parityConfig
	epoch   int
	g       *gate
	model   *parityModel
	// stale counts the invocations where a step passed while its fresh run
	// fails; withdrawn the merge runs that withdrew a record.
	stale, withdrawn int
	trace            []string
}

func newParityHarness(t *testing.T, config parityConfig) *parityHarness {
	f := newParityFixture(t)
	h := &parityHarness{t: t, fixture: f, config: config, model: &parityModel{config: config, source: true, tracked: true, records: map[[2]bool]bool{}}}
	h.newGate()
	return h
}

// newGate is a gate with empty receipts and records, over the steps of the
// current epoch.
func (h *parityHarness) newGate() {
	state, err := checkoutStateOf(h.fixture.root)
	if err != nil {
		h.t.Fatal(err)
	}
	h.g = &gate{root: h.fixture.root, cacheDir: h.t.TempDir(), steps: h.config.steps(h.t, h.fixture.root, h.epoch), programs: []string{"sh"}, limit: passThroughLimiter{},
		whole: &wholeGate{mechanism: "rss: parity", identity: state}, openSandbox: unsandboxed}
}

func (h *parityHarness) fail(format string, args ...any) {
	h.t.Helper()
	h.t.Fatalf("%s: after %s: %s", h.config.name, strings.Join(h.trace, " -> "), fmt.Sprintf(format, args...))
}

// hitMarker is what a step prints when an inner cache answers for it.
var hitMarker = map[string]string{
	parityProvenance: "ergo.local/prototype/tools/provenance\t(cached)",
	parityObserved:   "ergo.local/prototype/internal/probe\t(cached)",
	parityMapping:    "== " + parityMapping + ": ok, cached",
}

// check compares a real invocation with the model's prediction, and, for
// the gate's own steps, with the fresh verdicts.
func (h *parityHarness) check(want parityRun, code int, verdicts map[string]replay.Verdict, out string) {
	h.t.Helper()
	replayed := strings.Contains(out, "gate: replayed ")
	if replayed != want.replayed {
		h.fail("whole-gate replay %t, model %t:\n%s", replayed, want.replayed, out)
	}
	if (code == 0) != want.passed() {
		h.fail("exit %d, model verdicts %v:\n%s", code, want.verdicts, out)
	}
	fresh := h.model.fresh()
	for name, verdict := range want.verdicts {
		if verdicts[name] != verdict {
			h.fail("step %q verdict %v, model %v:\n%s", name, verdicts[name], verdict, out)
		}
		if verdict == replay.Pass && fresh[name] != replay.Pass {
			h.stale++
			if h.config.design() {
				h.fail("step %q passed incrementally, and its fresh run fails:\n%s", name, out)
			}
		}
	}
	if want.replayed {
		return
	}
	for name, marker := range hitMarker {
		if strings.Contains(out, marker) != want.hits[name] {
			h.fail("step %q answered from an inner cache: %t, model %t:\n%s", name, strings.Contains(out, marker), want.hits[name], out)
		}
	}
}

func (h *parityHarness) do(action string) {
	h.t.Helper()
	h.trace = append(h.trace, action)
	switch action {
	case "gate":
		want := h.model.gate()
		var out bytes.Buffer
		run := h.g.derive(h.g.steps, false, h.g.whole)
		code := run.run(&out)
		h.check(want, code, run.verdicts, out.String())
	case "fresh":
		inner, differ, code := h.model.merge()
		var out bytes.Buffer
		got := h.g.mergeTier(&out)
		text := out.String()
		if got != code {
			h.fail("merge tier exit %d, model %d:\n%s", got, code, text)
		}
		if inner != nil {
			// The inner tier's output ends where the uncached run starts.
			before, _, _ := strings.Cut(text, "merge: every step uncached\n")
			if !strings.Contains(before, "merge: the inner tier\n") {
				h.fail("the merge tier ran no inner tier:\n%s", text)
			}
			verdicts := map[string]replay.Verdict{}
			innerCode := 0
			for _, name := range []string{parityProvenance, parityObserved, parityMapping} {
				switch {
				case strings.Contains(before, "== "+name+": ok"):
					verdicts[name] = replay.Pass
				case strings.Contains(before, "== "+name+": FAILED"):
					verdicts[name], innerCode = replay.Failed, 1
				}
			}
			h.check(*inner, innerCode, verdicts, before)
		} else if strings.Contains(text, "merge: the inner tier\n") {
			h.fail("the merge tier ran the inner tier over a record the model holds:\n%s", text)
		}
		switch {
		case differ == nil:
			if !strings.Contains(text, "merge: FAILED: the inner tier failed") {
				h.fail("the merge tier did not stop at the failed inner tier:\n%s", text)
			}
		case len(differ) == 0:
			if !strings.Contains(text, "merge: verdicts equal (3 steps)") {
				h.fail("the merge tier found a difference the model does not:\n%s", text)
			}
		default:
			h.withdrawn++
			if !strings.Contains(text, "merge: FAILED: verdicts differ: "+strings.Join(differ, ", ")) {
				h.fail("the merge tier did not find exactly the differences %v:\n%s", differ, text)
			}
		}
	case "editSource":
		h.model.source = !h.model.source
		h.fixture.setSource(h.t, h.model.source)
	case "editIndex":
		h.model.tracked = !h.model.tracked
		h.fixture.setTracked(h.t, h.model.tracked)
	case "restore":
		if !h.model.source {
			h.model.source = true
			h.fixture.setSource(h.t, true)
		}
		if !h.model.tracked {
			h.model.tracked = true
			h.fixture.setTracked(h.t, true)
		}
	case "sweep":
		// Go's results are kept under the old epoch, which no step reads
		// again; the receipts and records go with the old gate cache.
		h.epoch++
		h.model.sweep()
		h.newGate()
	default:
		h.t.Fatalf("unknown action %q", action)
	}
}

// parityActions are the actions a generated sequence draws from; a plain
// gate, the commonest, is drawn twice as often.
var parityActions = []string{"gate", "gate", "editSource", "editIndex", "restore", "fresh", "sweep"}

// paritySeed fixes the generated tail, so a failure reproduces.
const paritySeed = 10

// TestGateCacheParityModel runs sequences of gates, merge-tier (fresh) runs,
// source edits, index edits, restores and cache sweeps on the fixture, and
// after each invocation compares the real gate with the reference model:
// whether the whole record replayed, the exit code, each step's verdict and
// whether an inner cache answered for it.
//
//   - The gate's own steps (design) must also give, after every invocation,
//     the verdict a fresh run gives: no step passes from any layer while its
//     fresh run fails. A fixed prefix takes every action; a seeded tail
//     follows.
//   - Two negative controls restore what this unit removed: cached provenance
//     controls (the step in the hermetic exception, without its own
//     -count=1), and the mapping step's old receipt. The
//     model predicts their stale passes, and each must occur from a real
//     cache hit (cmd/go's "(cached)", the gate's "ok, cached"), so the
//     fixture can see the failure the design refuses.
//   - In the mapping control a stale whole record is recorded, the merge
//     tier withdraws it, and the next gate must run its steps rather than
//     replay that record.
func TestGateCacheParityModel(t *testing.T) {
	if _, err := os.Stat(realTools["go"]); realTools["go"] == "" || err != nil {
		t.Fatal("go is required")
	}
	// Inherited flags must not decide what Go caches here, and the analyzer
	// binary goes into a temporary gate cache.
	t.Setenv("GOFLAGS", "")
	t.Setenv("ERGO_GATE_CACHE", t.TempDir())

	design := []string{"gate", "gate", "fresh", "editIndex", "gate", "fresh", "restore", "gate", "editSource", "gate", "fresh", "restore", "gate", "sweep", "gate"}
	random := rand.New(rand.NewPCG(paritySeed, 0))
	for range 14 {
		design = append(design, parityActions[random.IntN(len(parityActions))])
	}
	design = append(design, "gate")

	for _, run := range []struct {
		config parityConfig
		trace  []string
		// stale and withdrawn are the least stale passes and withdrawals
		// the trace must show.
		stale, withdrawn int
	}{
		{parityConfig{name: "design"}, design, 0, 0},
		{parityConfig{name: "cached provenance controls", cachedControls: true}, []string{"gate", "editSource", "gate", "fresh", "restore", "gate", "editIndex", "gate"}, 2, 0},
		{parityConfig{name: "mapping receipt", mappingReceipt: true}, []string{"gate", "editIndex", "gate", "fresh", "gate", "fresh", "sweep", "gate", "restore", "gate"}, 2, 2},
	} {
		h := newParityHarness(t, run.config)
		for _, action := range run.trace {
			h.do(action)
		}
		if h.stale < run.stale || h.withdrawn < run.withdrawn {
			t.Fatalf("%s: %d stale passes and %d withdrawals, want at least %d and %d; the control is vacuous (trace %s)", run.config.name, h.stale, h.withdrawn, run.stale, run.withdrawn, strings.Join(h.trace, " -> "))
		}
		t.Logf("%s: %s: %d stale passes, %d withdrawals", run.config.name, strings.Join(h.trace, " -> "), h.stale, h.withdrawn)
	}
}

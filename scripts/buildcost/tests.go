package main

// tests: the TPROBE meter (capstone §4a; decisions.md 2026-11-12, "If the
// tests become faster, then port tests too"). For each kind of test it
// counts one Go family against its Ergo forms, a hand Go control and a
// floor, whole commands, cold, warm and after an edit, and gives one
// verdict per kind by the rule below (probeVerdict). The forms, the edit,
// the seeds and the kill matrix are scripts/buildcost/testdata/tprobe/
// families.json; the Ergo forms are an overlay laid over a scratch copy
// of the tracked tree, never over the checkout.
//
//	go run ./scripts/buildcost tests [-work DIR] [-kinds K,...] [-rounds 5] [-kills] [-o FILE] [-data FILE] [-sets DIR]
//
// -kills judges every mutant of each family with every form and writes the
// matrix into families.json (the only file of the checkout it writes);
// without it the meter counts and prints the receipt table.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The kinds, forms and scenarios, in the receipt's order.
var (
	probeKinds     = []string{"property", "diagnostics", "built"}
	probeForms     = []string{"go", "control", "ergo-test", "ergo-check"}
	probeErgoForms = []string{"ergo-test", "ergo-check"}
	probeScenarios = []string{"cold", "warm", "edit"}
)

// probeFamiliesPath is families.json, from the repository root.
const probeFamiliesPath = "scripts/buildcost/testdata/tprobe/families.json"

// probeLinked is the linked Go the Go forms read; `go run ./scripts/linked`
// writes it after an edit of compiler/.
const probeLinked = "internal/linked/compiler/compiler.go"

// probeDiffers is the least relative gap between two medians that counts
// as a difference, and the spread over which a row is run again.
const probeDiffers = 0.02

type probeFile struct {
	About    string                  `json:"about"`
	Families map[string]*probeFamily `json:"families"`
}

// probeFamily is one kind's family and its forms.
type probeFamily struct {
	GoTest  string               `json:"goTest"`
	About   string               `json:"about,omitempty"`
	Package string               `json:"package"`
	Subject string               `json:"subject,omitempty"`
	Edit    probeEdit            `json:"edit"`
	Seeds   []int                `json:"seeds"`
	Inputs  []string             `json:"inputs"`
	Copies  []probeCopy          `json:"copies,omitempty"`
	Forms   map[string]probeForm `json:"forms"`
	Mutants []probeMutant        `json:"mutants"`
}

// probeEdit is the named edit of the edit scenario: one line of the code
// under test whose tokens change and whose behaviour does not.
type probeEdit struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	Why  string `json:"why,omitempty"`
}

// probeCopy puts inputs of the tree where an Ergo form embeds them (embed
// reaches no file outside its package): with no Join, the folder From[0]
// copied to To; with Join, every file under From whose name ends in
// Suffix, outside Skip, in path order, each followed by Join, into the one
// file To.
type probeCopy struct {
	From   []string `json:"from"`
	To     string   `json:"to"`
	Suffix string   `json:"suffix,omitempty"`
	Join   string   `json:"join,omitempty"`
	Skip   string   `json:"skip,omitempty"`
}

func (c probeCopy) apply(tree string) error {
	if c.Join == "" {
		return os.CopyFS(filepath.Join(tree, c.To), os.DirFS(filepath.Join(tree, c.From[0])))
	}
	var b bytes.Buffer
	for _, from := range c.From {
		err := filepath.WalkDir(filepath.Join(tree, from), func(path string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(path, c.Suffix) {
				return err
			}
			rel, _ := filepath.Rel(tree, path)
			if c.Skip != "" && strings.HasPrefix(filepath.ToSlash(rel), c.Skip) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b.Write(data)
			b.WriteString(c.Join)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(tree, c.To), b.Bytes(), 0o644)
}

// probeForm is the commands of one form. Run is the command list of cold
// and warm; the edit runs `go run ./scripts/linked` first when Relink. In
// an argument, {ergo} is the ergo built for the round, {bin} the form's
// own folder for binaries, {seed} the seed. Seed says how a seed reaches
// a form that draws cases: "arg" ({seed} in Run) or "env NAME"; empty for
// a form with fixed cases.
type probeForm struct {
	Tree   string     `json:"tree"`
	Run    [][]string `json:"run"`
	Env    []string   `json:"env,omitempty"`
	Relink bool       `json:"relink,omitempty"`
	Seed   string     `json:"seed,omitempty"`
}

// probeMutant is a row of a tracked ledger and its kill matrix: per form
// true, false or "skipped: <reason>".
type probeMutant struct {
	Name   string         `json:"name"`
	Ledger string         `json:"ledger"`
	Kills  map[string]any `json:"kills"`
}

// ---------------------------------------------------------------- the rule

// probeStats are one row's counts: the median, the least and the most,
// and the spread ((most - least) / median). WithinSpread marks a row whose
// spread stayed over probeDiffers after its reruns: it can only tie.
type probeStats struct {
	Median, Least, Most float64
	Spread              float64
	N                   int
	WithinSpread        bool
}

func probeStatsOf(counts []uint64) probeStats {
	if len(counts) == 0 {
		return probeStats{}
	}
	xs := make([]float64, len(counts))
	for i, c := range counts {
		xs[i] = float64(c)
	}
	slices.Sort(xs)
	n := len(xs)
	median := xs[n/2]
	if n%2 == 0 {
		median = (xs[n/2-1] + xs[n/2]) / 2
	}
	s := probeStats{Median: median, Least: xs[0], Most: xs[n-1], N: n}
	if median > 0 {
		s.Spread = (s.Most - s.Least) / median
	}
	return s
}

// probeCompare compares one scenario's rows of two forms: -1 when a is
// faster than b, 1 when slower, 0 when they do not differ. They differ
// when their least-to-most ranges are disjoint and their medians are more
// than probeDiffers apart, relative to the lower one; a row within spread
// differs from none.
func probeCompare(a, b probeStats) int {
	if a.N == 0 || b.N == 0 || a.WithinSpread || b.WithinSpread {
		return 0
	}
	if !(a.Most < b.Least || b.Most < a.Least) {
		return 0
	}
	lo := math.Min(a.Median, b.Median)
	if math.Abs(a.Median-b.Median) <= probeDiffers*lo {
		return 0
	}
	if a.Median < b.Median {
		return -1
	}
	return 1
}

// probeResult is one Ergo form's verdict against the Go form, with the
// scenarios where it is faster and those where it is slower.
type probeResult struct {
	Verdict        string
	Faster, Slower []string
}

// probeVerdict is the verdict of an Ergo form against the Go form (owner,
// 2026-11-18: "Any gain ports"): win when faster in at least one scenario,
// whatever the others show; tie when no scenario differs; loss when faster
// in none and slower in one. Every slower scenario is listed, a win's too.
func probeVerdict(goRows, ergoRows map[string]probeStats) probeResult {
	var r probeResult
	for _, s := range probeScenarios {
		switch probeCompare(ergoRows[s], goRows[s]) {
		case -1:
			r.Faster = append(r.Faster, s)
		case 1:
			r.Slower = append(r.Slower, s)
		}
	}
	switch {
	case len(r.Faster) > 0:
		r.Verdict = "win"
	case len(r.Slower) > 0:
		r.Verdict = "loss"
	default:
		r.Verdict = "tie"
	}
	return r
}

// probeRank orders the verdicts from the best for the Ergo side.
var probeRank = map[string]int{"win": 3, "tie": 2, "loss": 1}

// probeBetter reports that a is a better result for the Ergo side than b:
// a better verdict, or among wins more scenarios faster, then fewer slower.
func probeBetter(a, b probeResult) bool {
	if probeRank[a.Verdict] != probeRank[b.Verdict] {
		return probeRank[a.Verdict] > probeRank[b.Verdict]
	}
	if len(a.Faster) != len(b.Faster) {
		return len(a.Faster) > len(b.Faster)
	}
	return len(a.Slower) < len(b.Slower)
}

// probeSide is one form's result: its kills of the family's mutants and
// its rows. Counted is false when a guard refused a row of it.
type probeSide struct {
	Kills, Mutants int
	Counted        bool
	Rows           map[string]probeStats
}

// covered reports equal coverage: every mutant killed and every row counted.
func (s probeSide) covered() bool { return s.Mutants > 0 && s.Kills == s.Mutants && s.Counted }

// probeKindVerdict is the kind's verdict: that of its best Ergo form with
// equal coverage against the Go form (a win when any form wins), or "no
// result" when no Ergo form reached it. It names the form the verdict is
// from.
func probeKindVerdict(goSide probeSide, ergo map[string]probeSide) (probeResult, string) {
	best, from := probeResult{Verdict: "no result"}, ""
	for _, f := range probeErgoForms {
		e, ok := ergo[f]
		if !ok || !e.covered() || !goSide.Counted {
			continue
		}
		v := probeVerdict(goSide.Rows, e.Rows)
		if from == "" || probeBetter(v, best) {
			best, from = v, f
		}
	}
	return best, from
}

// probeVerdictLine is a kind's verdict as the receipt prints it: a win
// names the form, the scenarios it wins and every slower one, each with
// its ratio to go (`win (ergo-test: edit 0.61; slower: cold 1.30)`).
func probeVerdictLine(v probeResult, from string, goSide, e probeSide) string {
	if from == "" {
		return v.Verdict
	}
	ratios := func(scenarios []string) string {
		var parts []string
		for _, s := range scenarios {
			parts = append(parts, s+" "+strings.TrimSuffix(probeRatio(e.Rows[s], goSide.Rows[s]), "x"))
		}
		return strings.Join(parts, ", ")
	}
	line := v.Verdict + " (" + from
	if len(v.Faster) > 0 {
		line += ": " + ratios(v.Faster)
	}
	if len(v.Slower) > 0 {
		line += "; slower: " + ratios(v.Slower)
	}
	return line + ")"
}

// ---------------------------------------------------------------- evidence

// probeOutcome classifies one run of a form's command list: "pass", "fail"
// (a test verdict), or "broken" with the reason (a build or setup error,
// no tests run, a skip, an incomplete or an empty evaluation, a hang).
type probeOutcome struct {
	Class  string
	Reason string
}

// probeRun is one run of a command list as the classifier reads it: the
// form, the exit status of the command that ended it, its index and the
// number of commands, and the output of the whole list.
type probeRun struct {
	Form         string
	Exit         int
	Failed, Of   int
	Output       string
	Hung         bool
	CommandError string
}

// probeNoRun are the outputs that show a family did not run.
var probeNoRun = []string{"no tests to run", "[no test files]", "no test files", "PASS (0 tests"}

func probeClassify(r probeRun) probeOutcome {
	if r.Hung {
		return probeOutcome{"broken", "hung: stopped by the meter's deadline"}
	}
	if r.CommandError != "" {
		return probeOutcome{"broken", r.CommandError}
	}
	// The go and control forms' leading commands are setup (relink, go
	// build); every command of an Ergo form is a test command, and its
	// verdict is read from its output.
	if r.Exit != 0 && r.Failed < r.Of-1 && (r.Form == "go" || r.Form == "control") {
		return probeOutcome{"broken", fmt.Sprintf("command %d of %d failed (a build or setup step)", r.Failed+1, r.Of)}
	}
	for _, m := range probeNoRun {
		if strings.Contains(r.Output, m) {
			return probeOutcome{"broken", "the family did not run: " + m}
		}
	}
	switch r.Form {
	case "go":
		if strings.Contains(r.Output, "[build failed]") || strings.Contains(r.Output, "[setup failed]") {
			return probeOutcome{"broken", "go test: the test did not build"}
		}
		if strings.Contains(r.Output, "panic: test timed out") {
			return probeOutcome{"broken", "go test: timed out (a timeout is no verdict)"}
		}
		if r.Exit == 0 {
			return probeOutcome{"pass", ""}
		}
		if strings.Contains(r.Output, "--- FAIL") {
			return probeOutcome{"fail", ""}
		}
		return probeOutcome{"broken", "go test failed with no failed test"}
	case "control":
		if r.Exit == 0 {
			return probeOutcome{"pass", ""}
		}
		if r.Exit == 1 && strings.Contains(r.Output, "FAIL") {
			return probeOutcome{"fail", ""}
		}
		return probeOutcome{"broken", fmt.Sprintf("control exited %d with no verdict", r.Exit)}
	case "ergo-test":
		return probeClassifyErgoTest(r)
	case "ergo-check":
		return probeClassifyErgoCheck(r)
	}
	return probeOutcome{"broken", "unknown form " + r.Form}
}

func probeClassifyErgoTest(r probeRun) probeOutcome {
	verdict, skips := false, 0
	for _, line := range strings.Split(r.Output, "\n") {
		switch {
		case strings.HasPrefix(line, "skip "):
			skips++
		case strings.HasPrefix(line, "FAIL ") && !strings.HasPrefix(line, "FAIL ("):
			verdict = true
		}
	}
	if skips > 0 {
		return probeOutcome{"broken", fmt.Sprintf("ergo test skipped %d obligations (a skip is not a pass)", skips)}
	}
	if r.Exit == 0 {
		if !strings.Contains(r.Output, "PASS (") {
			return probeOutcome{"broken", "ergo test printed no summary"}
		}
		return probeOutcome{"pass", ""}
	}
	if verdict {
		return probeOutcome{"fail", ""}
	}
	return probeOutcome{"broken", fmt.Sprintf("ergo test exited %d with no failed test (the source did not check or build)", r.Exit)}
}

// probeEnvelope is the part of `ergo check`'s envelope the classifier reads.
type probeEnvelope struct {
	Evaluation *struct {
		Status      string `json:"status"`
		Obligations []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Cached bool   `json:"cached"`
			Reason string `json:"reason"`
		} `json:"obligations"`
	} `json:"evaluation"`
}

func probeReadEnvelope(output string) (probeEnvelope, bool) {
	var env probeEnvelope
	at := strings.Index(output, "{")
	if at < 0 {
		return env, false
	}
	err := json.NewDecoder(strings.NewReader(output[at:])).Decode(&env)
	return env, err == nil
}

func probeClassifyErgoCheck(r probeRun) probeOutcome {
	env, ok := probeReadEnvelope(r.Output)
	if !ok {
		return probeOutcome{"broken", "ergo check printed no envelope"}
	}
	if env.Evaluation == nil {
		return probeOutcome{"broken", "the source did not check (no evaluation)"}
	}
	switch env.Evaluation.Status {
	case "refuted", "defect":
		return probeOutcome{"fail", ""}
	case "passed":
		if len(env.Evaluation.Obligations) == 0 {
			return probeOutcome{"broken", "no obligation ran"}
		}
		return probeOutcome{"pass", ""}
	}
	why := ""
	for _, o := range env.Evaluation.Obligations {
		if o.Status != "passed" {
			why = ": " + o.Name + " " + o.Status + " " + o.Reason
			break
		}
	}
	return probeOutcome{"broken", "evaluation " + env.Evaluation.Status + " (not a pass)" + why}
}

// probeKilled is the kill rule: the form passes on the unmutated copy at
// every seed, and fails with a test verdict on the mutated copy at every
// seed (one entry per seed; a form with fixed cases has one).
func probeKilled(clean, mutated []probeOutcome) bool {
	if len(clean) == 0 || len(clean) != len(mutated) {
		return false
	}
	for i := range clean {
		if clean[i].Class != "pass" || mutated[i].Class != "fail" {
			return false
		}
	}
	return true
}

// probeRow is one counted run as the guards read it.
type probeRow struct {
	Scenario             string
	Run                  probeRun
	XDGNew, GoCacheNew   bool
	EditBefore, EditNow  string
	Relink               bool
	LinkedBefore, Linked string
}

// probeCached reports a line that shows an answer from a cache.
func probeCached(output string) bool {
	if strings.Contains(output, "(cached)") || strings.Contains(output, "cached pass replayed") {
		return true
	}
	env, ok := probeReadEnvelope(output)
	if ok && env.Evaluation != nil {
		for _, o := range env.Evaluation.Obligations {
			if o.Cached {
				return true
			}
		}
	}
	return false
}

// probeRefuse is "" when the row counts, else why a guard refuses it.
func probeRefuse(r probeRow) string {
	if o := probeClassify(r.Run); o.Class != "pass" {
		return "the family did not pass: " + o.Class + " " + o.Reason
	}
	switch r.Scenario {
	case "cold":
		if !r.XDGNew || !r.GoCacheNew {
			return "cold: a cache folder was not new"
		}
		if probeCached(r.Run.Output) {
			return "cold: the output shows a cached answer"
		}
	case "edit":
		if r.EditBefore == "" || r.EditBefore == r.EditNow {
			return "edit: the edited file's digest did not change"
		}
		if r.Relink && (r.LinkedBefore == "" || r.LinkedBefore == r.Linked) {
			return "edit: the linked Go did not change"
		}
	}
	return ""
}

// ---------------------------------------------------------------- the runner

type prober struct {
	root, work, ergo, floor string
	sets                    string
	stdCache, xdgTemplate   string
	files                   []string
	fams                    probeFile
	log                     *os.File
}

func probeMain(args []string) int {
	fset := flag.NewFlagSet("buildcost tests", flag.ContinueOnError)
	work := fset.String("work", "", "scratch directory (default: a fresh one under /tmp/opencode, removed at the end)")
	kinds := fset.String("kinds", strings.Join(probeKinds, ","), "the kinds to run")
	rounds := fset.Int("rounds", 5, "rounds per form")
	kills := fset.Bool("kills", false, "judge every mutant with every form and write the kill matrix into families.json")
	out := fset.String("o", "", "write the receipt table to FILE (default: standard output)")
	data := fset.String("data", "", "write every count as JSON to FILE")
	sets := fset.String("sets", "", "the pin archive sets (go run ./scripts/pinsets -o DIR); default: made into -work")
	keep := fset.Bool("keep", false, "keep -work")
	all := fset.Bool("all", false, "count every form, an Ergo form that misses a kill too")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	root, err := os.Getwd()
	if err == nil {
		_, err = os.Stat(filepath.Join(root, "go.mod"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildcost tests: run it from the repository root:", err)
		return 2
	}
	p := &prober{root: root, work: *work, sets: *sets, log: os.Stderr}
	if p.work == "" {
		if err := os.MkdirAll("/tmp/opencode", 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		dir, err := os.MkdirTemp("/tmp/opencode", "tprobe-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		p.work = dir
	}
	if !*keep {
		defer os.RemoveAll(p.work)
	}
	if err := p.load(); err != nil {
		fmt.Fprintln(os.Stderr, "buildcost tests:", err)
		return 1
	}
	var picked []string
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			if p.fams.Families[k] == nil {
				fmt.Fprintf(os.Stderr, "buildcost tests: no family %q in %s\n", k, probeFamiliesPath)
				return 2
			}
			picked = append(picked, k)
		}
	}
	if err := p.prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "buildcost tests:", err)
		return 1
	}
	if *kills {
		for _, k := range picked {
			// Saved after each kind, so a run that is cut keeps the
			// kinds it finished.
			err := p.killMatrix(k)
			if err == nil {
				err = p.save()
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "buildcost tests:", err)
				return 1
			}
		}
		return 0
	}
	report := &probeReport{Load: probeLoad(), Rounds: *rounds, Kinds: map[string]*probeKindReport{}}
	report.Prime = p.primeLines()
	for _, k := range picked {
		report.Kinds[k] = p.countKind(k, *rounds, *all)
	}
	text := report.text(p.fams, picked)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	} else {
		fmt.Print(text)
	}
	if *data != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(*data, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func (p *prober) load() error {
	data, err := os.ReadFile(filepath.Join(p.root, probeFamiliesPath))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &p.fams); err != nil {
		return fmt.Errorf("%s: %w", probeFamiliesPath, err)
	}
	out, err := exec.Command("git", "--no-optional-locks", "-C", p.root, "ls-files", "-z").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			p.files = append(p.files, f)
		}
	}
	return nil
}

func (p *prober) save() error {
	data, err := json.MarshalIndent(p.fams, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.root, probeFamiliesPath), append(data, '\n'), 0o644)
}

func (p *prober) logf(format string, a ...any) { fmt.Fprintf(p.log, "tprobe: "+format+"\n", a...) }

// prepare builds what no row counts: ergo from a scratch copy, the floor,
// Go's std archives in a template GOCACHE, and the template of a new Ergo
// cache (the toolchain and pin sets on disk, as a release has them).
func (p *prober) prepare() error {
	tree := filepath.Join(p.work, "prepare", "tree")
	if err := p.copyTree(tree, ""); err != nil {
		return err
	}
	p.stdCache = filepath.Join(p.work, "template", "gocache")
	if out, err := p.plain(tree, []string{"GOCACHE=" + p.stdCache}, "go", "build", "std"); err != nil {
		return fmt.Errorf("go build std: %v\n%s", err, out)
	}
	// The sets are made with the go the forms use (ERGO_GO=system), so the
	// lock of the copy is rewritten to name them (-lock) before ergo is
	// built from it: ergo embeds the lock, as a release's ergo names its
	// own sets. Sets given with -sets must come from this tree and go.
	if p.sets == "" {
		p.sets = filepath.Join(p.work, "sets")
		gc := filepath.Join(p.work, "template", "gocache-sets")
		if out, err := p.plain(tree, []string{"GOCACHE=" + gc, "XDG_CACHE_HOME=" + filepath.Join(p.work, "template", "xdg-sets")}, "go", "run", "./scripts/pinsets", "-o", p.sets, "-target", runtime.GOOS+"/"+runtime.GOARCH, "-lock"); err != nil {
			p.logf("pinsets failed, cold Ergo runs compile what the sets hold: %v\n%s", err, out)
		}
		os.RemoveAll(gc)
	}
	bin := filepath.Join(p.work, "bin")
	p.ergo, p.floor = filepath.Join(bin, "ergo"), filepath.Join(bin, "floor")
	for _, b := range [][]string{{"go", "build", "-o", p.ergo, "./cmd/ergo"}, {"go", "build", "-o", p.floor, "./scripts/buildcost/testdata/tprobe/floor"}} {
		if out, err := p.plain(tree, nil, b...); err != nil {
			return fmt.Errorf("%s: %v\n%s", strings.Join(b, " "), err, out)
		}
	}
	// One ergo test of a one-test file installs the toolchain and the pin
	// sets into the template; everything else it wrote goes.
	p.xdgTemplate = filepath.Join(p.work, "template", "xdg")
	primeDir := filepath.Join(p.work, "template", "prime")
	os.MkdirAll(primeDir, 0o755)
	os.WriteFile(filepath.Join(primeDir, "prime.ergo"), []byte("test Primes {\n\tassert true\n}\n"), 0o644)
	gc := filepath.Join(p.work, "template", "gocache-prime")
	out, err := p.plain(primeDir, []string{"GOCACHE=" + gc, "XDG_CACHE_HOME=" + p.xdgTemplate, "ERGO_PIN_URL=file://" + filepath.ToSlash(p.sets)}, p.ergo, "test", "prime.ergo")
	if err != nil {
		return fmt.Errorf("ergo test of the prime: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "refused") || strings.Contains(line, "building") {
			p.logf("the prime: %s", line)
		}
	}
	os.RemoveAll(gc)
	ergoRoot := filepath.Join(p.xdgTemplate, "ergo")
	entries, _ := os.ReadDir(ergoRoot)
	for _, e := range entries {
		if e.Name() != "toolchain" {
			os.RemoveAll(filepath.Join(ergoRoot, e.Name()))
		}
	}
	return os.RemoveAll(filepath.Join(p.work, "prepare"))
}

// plain runs argv in dir, uncounted, with the meter's environment and env.
func (p *prober) plain(dir string, env []string, argv ...string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = probeEnv(os.Environ(), append([]string{"GOMAXPROCS=4", "GOTOOLCHAIN=local", "ERGO_GO=system"}, env...))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// probeEnv is base with every variable of set replaced or added, and the
// variables that would carry state between runs dropped.
func probeEnv(base, set []string) []string {
	drop := map[string]bool{"GOCACHE": true, "XDG_CACHE_HOME": true, "ERGO_CACHE": true, "GOFLAGS": true, "ERGO_STAGE_REPORT": true, "ERGO_LAWCACHE": true, "ERGO_SELFHOST_SEED": true, "JSONPARSE_SEED": true, "ERGO_TEST_CLI": true, "ERGO_PIN_URL": true}
	for _, kv := range set {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, set...)
}

// copyTree copies the tracked files into dir and, for an overlay tree,
// lays kind's Ergo form and its copies over them.
func (p *prober) copyTree(dir, overlay string) error {
	for _, f := range p.files {
		src, dst := filepath.Join(p.root, f), filepath.Join(dir, f)
		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the checkout and not yet committed
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	if overlay == "" {
		return nil
	}
	from := filepath.Join(p.root, "scripts/buildcost/testdata/tprobe", overlay, "ergo")
	err := filepath.WalkDir(from, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return err
	}
	for _, c := range p.fams.Families[overlay].Copies {
		if err := c.apply(dir); err != nil {
			return err
		}
	}
	return nil
}

// probeDigest is the SHA-256 of a file, "" when it cannot be read.
func probeDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// formDir is one form's round: its tree and caches, made new.
type formDir struct {
	dir, tree, gocache, xdg, bin string
	xdgNew, gocacheNew           bool
}

func (p *prober) newFormDir(name, kind, form string) (formDir, error) {
	d := formDir{dir: filepath.Join(p.work, name, kind, form)}
	if err := os.RemoveAll(d.dir); err != nil {
		return d, err
	}
	d.tree, d.gocache, d.xdg, d.bin = filepath.Join(d.dir, "tree"), filepath.Join(d.dir, "gocache"), filepath.Join(d.dir, "xdg"), filepath.Join(d.dir, "bin")
	overlay := ""
	if p.fams.Families[kind].Forms[form].Tree == "overlay" {
		overlay = kind
	}
	if err := p.copyTree(d.tree, overlay); err != nil {
		return d, err
	}
	_, errG := os.Stat(d.gocache)
	_, errX := os.Stat(d.xdg)
	d.gocacheNew, d.xdgNew = errors.Is(errG, fs.ErrNotExist), errors.Is(errX, fs.ErrNotExist)
	if err := os.CopyFS(d.gocache, os.DirFS(p.stdCache)); err != nil {
		return d, err
	}
	if err := probeCopyKeepLinks(p.xdgTemplate, d.xdg); err != nil {
		return d, err
	}
	for _, sub := range []string{d.bin, filepath.Join(d.dir, "tmp")} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return d, err
		}
	}
	return d, nil
}

// probeCopyKeepLinks copies a folder, symbolic links as links (the
// toolchain's patched goroot links the release's entries).
func probeCopyKeepLinks(from, to string) error {
	return filepath.WalkDir(from, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case info.IsDir():
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	})
}

func (p *prober) env(d formDir, name string, form probeForm, seed int, kills bool) []string {
	set := []string{"GOCACHE=" + d.gocache, "XDG_CACHE_HOME=" + d.xdg, "TMPDIR=" + filepath.Join(d.dir, "tmp"), "GOMAXPROCS=4", "GOTOOLCHAIN=local", "ERGO_GO=system", "GOPROXY=off", "ERGO_STAGE_REPORT=" + filepath.Join(d.dir, "stage.json")}
	if name == "go" || name == "control" {
		// The direct path of ergo refuses a non-empty GOFLAGS (docs/
		// tooling.md); the Go forms take -p=4 from it, the Ergo forms
		// (in an overlay or in the tree) do not.
		set = append(set, "GOFLAGS=-p=4")
	}
	if name, ok := strings.CutPrefix(form.Seed, "env "); ok {
		set = append(set, name+"="+strconv.Itoa(seed))
	}
	if kills {
		set = append(set, "ERGO_LAWCACHE=off")
	}
	for _, kv := range form.Env {
		set = append(set, d.expand(kv, p.ergo, seed))
	}
	return probeEnv(os.Environ(), set)
}

func (d formDir) expand(arg, ergo string, seed int) string {
	return strings.NewReplacer("{ergo}", ergo, "{bin}", d.bin, "{seed}", strconv.Itoa(seed), "{tree}", d.tree).Replace(arg)
}

// commands is the form's command list for a scenario.
func (p *prober) commands(d formDir, form probeForm, scenario string, seed int) [][]string {
	var list [][]string
	if scenario == "edit" && form.Relink {
		list = append(list, []string{"go", "run", "./scripts/linked"})
	}
	for _, argv := range form.Run {
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = strings.ReplaceAll(d.expand(a, p.ergo, seed), "{ergo}", p.ergo)
		}
		list = append(list, out)
	}
	return list
}

// counted is one counted command list: its instructions, summed over the
// commands, the peak resident set of the largest process, and the run.
type counted struct {
	Instructions uint64
	PeakRSS      int64
	Run          probeRun
	Stages       map[string]int64
}

// countList runs a command list, counted, stopping at the first failure.
func (p *prober) countList(d formDir, env []string, form string, list [][]string) counted {
	var c counted
	var out bytes.Buffer
	c.Run = probeRun{Form: form, Of: len(list)}
	for i, argv := range list {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = d.tree, env
		cmd.Stdout, cmd.Stderr = &out, &out
		res := measure(cmd)
		c.Instructions += res.instructions
		c.PeakRSS = max(c.PeakRSS, probeMaxRSS(cmd.ProcessState))
		c.Run.Exit, c.Run.Failed = res.exit, i
		if res.exit != 0 {
			break
		}
	}
	c.Run.Output = out.String()
	if data, err := os.ReadFile(filepath.Join(d.dir, "stage.json")); err == nil {
		json.Unmarshal(data, &c.Stages)
		os.Remove(filepath.Join(d.dir, "stage.json"))
	}
	return c
}

// probeMaxRSS is the peak resident set, in bytes, of a waited process and
// the descendants it waited for (wait4's rusage), 0 when unknown.
func probeMaxRSS(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	v := reflect.ValueOf(ps.SysUsage())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		if f := v.Elem().FieldByName("Maxrss"); f.IsValid() && f.CanInt() {
			if runtime.GOOS == "darwin" {
				return f.Int()
			}
			return f.Int() * 1024
		}
	}
	return 0
}

// runList runs a command list uncounted, in its own process group, and
// stops the group at the deadline (a hung mutant is no verdict).
func runList(d formDir, env []string, form string, list [][]string, deadline time.Duration) probeRun {
	var out bytes.Buffer
	r := probeRun{Form: form, Of: len(list)}
	for i, argv := range list {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = d.tree, env
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			r.CommandError = err.Error()
			break
		}
		pgid := cmd.Process.Pid
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var err error
		select {
		case err = <-done:
		case <-time.After(deadline):
			syscall.Kill(-pgid, syscall.SIGKILL)
			err = <-done
			r.Hung = true
		}
		// Whatever the command left in its group goes with it.
		syscall.Kill(-pgid, syscall.SIGKILL)
		r.Exit, r.Failed = 0, i
		if err != nil {
			r.Exit = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				r.Exit = ee.ExitCode()
			}
		}
		if r.Exit != 0 || r.Hung {
			break
		}
	}
	r.Output = out.String()
	return r
}

// probeDeadline stops a kill run that hangs; it decides no verdict.
const probeDeadline = 45 * time.Minute

// ---------------------------------------------------------------- kills

// killMatrix judges each mutant of kind's family with each form by the
// kill rule and writes the cells into the family.
func (p *prober) killMatrix(kind string) error {
	fam := p.fams.Families[kind]
	for _, form := range probeForms {
		f := fam.Forms[form]
		d, err := p.newFormDir("kills", kind, form)
		if err != nil {
			return err
		}
		seeds := []int{1}
		if f.Seed != "" {
			seeds = fam.Seeds
		}
		run := func() []probeOutcome {
			var outs []probeOutcome
			for _, seed := range seeds {
				r := runList(d, p.env(d, form, f, seed, true), form, p.commands(d, f, "edit", seed), probeDeadline)
				o := probeClassify(r)
				if o.Class == "broken" {
					p.logf("%s %s seed %d: %s: %s", kind, form, seed, o.Reason, clipTail(r.Output))
				}
				outs = append(outs, o)
			}
			return outs
		}
		clean := run()
		p.logf("%s %s clean: %v", kind, form, clean)
		for i := range fam.Mutants {
			m := &fam.Mutants[i]
			if m.Kills == nil {
				m.Kills = map[string]any{}
			}
			row, err := probeLedgerRow(p.root, m.Ledger, m.Name)
			if err != nil {
				return err
			}
			if form != "go" && form != "control" && !strings.HasSuffix(row.File, ".ergo") {
				m.Kills[form] = "skipped: the mutant edits Go (" + row.File + "), which no Ergo form runs"
				continue
			}
			path := filepath.Join(d.tree, filepath.FromSlash(row.File))
			orig, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Count(orig, []byte(row.From)) != 1 {
				m.Kills[form] = fmt.Sprintf("skipped: the ledger's from text occurs %d times in %s", bytes.Count(orig, []byte(row.From)), row.File)
				continue
			}
			linkedPath := filepath.Join(d.tree, probeLinked)
			linked, _ := os.ReadFile(linkedPath)
			os.WriteFile(path, bytes.Replace(orig, []byte(row.From), []byte(row.To), 1), 0o644)
			mutated := run()
			os.WriteFile(path, orig, 0o644)
			if linked != nil {
				os.WriteFile(linkedPath, linked, 0o644)
			}
			k := probeKilled(clean, mutated)
			m.Kills[form] = k
			p.logf("%s %s %s: killed %v %v", kind, form, m.Name, k, mutated)
		}
		os.RemoveAll(d.dir)
	}
	return nil
}

// probeLedgerRow is a ledger's row by name.
type probeLedgerEntry struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Killers []string `json:"killers"`
}

func probeLedgerRow(root, ledger, name string) (probeLedgerEntry, error) {
	data, err := os.ReadFile(filepath.Join(root, ledger))
	if err != nil {
		return probeLedgerEntry{}, err
	}
	var l struct {
		Mutants []probeLedgerEntry `json:"mutants"`
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return probeLedgerEntry{}, fmt.Errorf("%s: %w", ledger, err)
	}
	for _, m := range l.Mutants {
		if m.Name == name {
			return m, nil
		}
	}
	return probeLedgerEntry{}, fmt.Errorf("%s has no mutant %s", ledger, name)
}

func clipTail(s string) string {
	if len(s) > 600 {
		return "…" + s[len(s)-600:]
	}
	return s
}

// ---------------------------------------------------------------- counts

// probeReport is a receipt run: the load, the context lines and each kind.
type probeReport struct {
	Load   string                      `json:"load"`
	Rounds int                         `json:"rounds"`
	Prime  []string                    `json:"prime"`
	Kinds  map[string]*probeKindReport `json:"kinds"`
}

type probeKindReport struct {
	// Counts[form][scenario] are the counted runs, one per round; the
	// floors are under the forms "floor-run" and "floor-skip".
	Counts  map[string]map[string][]uint64 `json:"counts"`
	RSS     map[string]map[string][]int64  `json:"rss"`
	Within  map[string]map[string]bool     `json:"withinSpread"`
	Refused map[string][]string            `json:"refused"`
	Warm    map[string]string              `json:"warm"`
	Stages  map[string]map[string]int64    `json:"stages"`
	Context []string                       `json:"context"`
}

func probeLoad() string {
	data, _ := os.ReadFile("/proc/loadavg")
	return strings.TrimSpace(firstField(string(data)))
}

// primeLines counts what every Go-building form pays on an empty cache.
func (p *prober) primeLines() []string {
	dir := filepath.Join(p.work, "prime-std")
	defer os.RemoveAll(dir)
	cmd := exec.Command("go", "build", "std")
	cmd.Dir = p.root
	cmd.Env = probeEnv(os.Environ(), []string{"GOCACHE=" + dir, "GOMAXPROCS=4", "GOFLAGS=-p=4", "GOTOOLCHAIN=local"})
	res := measure(cmd)
	return []string{fmt.Sprintf("go build std into an empty GOCACHE: %.1f G instructions, exit %d (counted once; every form that starts a Go build pays it on a machine with an empty cache; ergo check does not)", float64(res.instructions)/1e9, res.exit)}
}

// countKind counts every form of kind for rounds rounds, reruns a row
// whose spread is over probeDiffers, and counts the floors.
func (p *prober) countKind(kind string, rounds int, all bool) *probeKindReport {
	fam := p.fams.Families[kind]
	rep := &probeKindReport{Counts: map[string]map[string][]uint64{}, RSS: map[string]map[string][]int64{}, Within: map[string]map[string]bool{}, Refused: map[string][]string{}, Warm: map[string]string{}, Stages: map[string]map[string]int64{}}
	// An Ergo form that misses a kill has no speed result, so it is not
	// counted (unless -all); with none left the kind is not counted.
	forms := []string{"go", "control"}
	for _, f := range probeErgoForms {
		if all || p.covers(fam, f) {
			forms = append(forms, f)
		}
	}
	if len(forms) == 2 && !all {
		return rep
	}
	p.countRounds(kind, fam, forms, rounds, rep)
	var again []string
	for _, form := range forms {
		for _, s := range probeScenarios {
			if probeStatsOf(rep.Counts[form][s]).Spread > probeDiffers {
				again = append(again, form)
				break
			}
		}
	}
	for attempt := 0; attempt < 2 && len(again) > 0; attempt++ {
		if attempt == 1 {
			p.waitLoad(48)
		}
		for _, f := range again {
			delete(rep.Counts, f)
			delete(rep.RSS, f)
		}
		p.countRounds(kind, fam, again, rounds, rep)
		var still []string
		for _, form := range again {
			for _, s := range probeScenarios {
				if probeStatsOf(rep.Counts[form][s]).Spread > probeDiffers {
					still = append(still, form)
					break
				}
			}
		}
		again = still
	}
	for _, form := range again {
		for _, s := range probeScenarios {
			if probeStatsOf(rep.Counts[form][s]).Spread > probeDiffers {
				if rep.Within[form] == nil {
					rep.Within[form] = map[string]bool{}
				}
				rep.Within[form][s] = true
			}
		}
	}
	p.countFloors(kind, fam, rounds, rep)
	p.contextLines(kind, fam, rep)
	return rep
}

// covers reports that form kills every mutant of fam's matrix.
func (p *prober) covers(fam *probeFamily, form string) bool {
	for _, m := range fam.Mutants {
		if m.Kills[form] != true {
			return false
		}
	}
	return len(fam.Mutants) > 0
}

// waitLoad waits until the 1-minute load is under limit, at most two hours.
func (p *prober) waitLoad(limit float64) {
	for start := time.Now(); time.Since(start) < 2*time.Hour; time.Sleep(time.Minute) {
		if l, err := strconv.ParseFloat(probeLoad(), 64); err == nil && l < limit {
			return
		}
	}
	p.logf("the load stayed at or over %.0f for two hours", limit)
}

// countRounds runs forms for rounds rounds, the order of the forms rotated
// each round, each round with fresh caches.
func (p *prober) countRounds(kind string, fam *probeFamily, forms []string, rounds int, rep *probeKindReport) {
	for round := 0; round < rounds; round++ {
		order := append(slices.Clone(forms[round%len(forms):]), forms[:round%len(forms)]...)
		for _, form := range order {
			p.countForm(kind, fam, form, round, rep)
		}
	}
}

func (p *prober) countForm(kind string, fam *probeFamily, form string, round int, rep *probeKindReport) {
	f := fam.Forms[form]
	d, err := p.newFormDir(fmt.Sprintf("r%d", round), kind, form)
	defer os.RemoveAll(d.dir)
	if err != nil {
		rep.Refused[form] = append(rep.Refused[form], fmt.Sprintf("round %d: %v", round, err))
		return
	}
	env := p.env(d, form, f, 1, false)
	add := func(s string, c counted, row probeRow) {
		if why := probeRefuse(row); why != "" {
			rep.Refused[form] = append(rep.Refused[form], fmt.Sprintf("round %d %s: %s: %s", round, s, why, clipTail(c.Run.Output)))
			return
		}
		if rep.Counts[form] == nil {
			rep.Counts[form], rep.RSS[form] = map[string][]uint64{}, map[string][]int64{}
		}
		rep.Counts[form][s] = append(rep.Counts[form][s], c.Instructions)
		rep.RSS[form][s] = append(rep.RSS[form][s], c.PeakRSS)
		if s == "warm" {
			rep.Warm[form] = "ran"
			if probeCached(c.Run.Output) {
				rep.Warm[form] = "cached"
			}
			rep.Warm[form] += probeObligations(form, c.Run.Output)
		}
		if len(c.Stages) > 0 {
			rep.Stages[form+" "+s] = c.Stages
		}
	}
	cold := p.countList(d, env, form, p.commands(d, f, "cold", 1))
	add("cold", cold, probeRow{Scenario: "cold", Run: cold.Run, XDGNew: d.xdgNew, GoCacheNew: d.gocacheNew})
	warm := p.countList(d, env, form, p.commands(d, f, "warm", 1))
	add("warm", warm, probeRow{Scenario: "warm", Run: warm.Run})
	edited := filepath.Join(d.tree, filepath.FromSlash(fam.Edit.File))
	before, linkedBefore := probeDigest(edited), probeDigest(filepath.Join(d.tree, probeLinked))
	data, _ := os.ReadFile(edited)
	if bytes.Count(data, []byte(fam.Edit.From)) == 1 {
		os.WriteFile(edited, bytes.Replace(data, []byte(fam.Edit.From), []byte(fam.Edit.To), 1), 0o644)
	}
	edit := p.countList(d, env, form, p.commands(d, f, "edit", 1))
	add("edit", edit, probeRow{Scenario: "edit", Run: edit.Run, EditBefore: before, EditNow: probeDigest(edited), Relink: f.Relink, LinkedBefore: linkedBefore, Linked: probeDigest(filepath.Join(d.tree, probeLinked))})
	if form == "control" {
		// floor-run: the control's binary, built, run once.
		run := p.countList(d, env, "control", [][]string{{filepath.Join(d.bin, "control")}})
		if probeClassify(run.Run).Class == "pass" {
			if rep.Counts["floor-run"] == nil {
				rep.Counts["floor-run"], rep.RSS["floor-run"] = map[string][]uint64{}, map[string][]int64{}
			}
			rep.Counts["floor-run"]["run"] = append(rep.Counts["floor-run"]["run"], run.Instructions)
			rep.RSS["floor-run"]["run"] = append(rep.RSS["floor-run"]["run"], run.PeakRSS)
		}
	}
}

// probeObligations is the obligations an Ergo form ran of its total.
func probeObligations(form, output string) string {
	switch form {
	case "ergo-check":
		env, ok := probeReadEnvelope(output)
		if !ok || env.Evaluation == nil {
			return ""
		}
		ran := 0
		for _, o := range env.Evaluation.Obligations {
			if !o.Cached {
				ran++
			}
		}
		return fmt.Sprintf(", obligations run %d of %d", ran, len(env.Evaluation.Obligations))
	case "ergo-test":
		if strings.Contains(output, "cached pass replayed") {
			return ", obligations run 0 (the pass replayed)"
		}
		ran, all := 0, 0
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "ok ") {
				all++
				if !strings.Contains(line, "(cached)") {
					ran++
				}
			}
		}
		return fmt.Sprintf(", obligations run %d of %d", ran, all)
	}
	return ""
}

// countFloors counts floor-skip: the floor binary hashes the family's
// inputs once and reads one verdict file.
func (p *prober) countFloors(kind string, fam *probeFamily, rounds int, rep *probeKindReport) {
	verdict := filepath.Join(p.work, "floor-"+kind+".verdict")
	args := append([]string{"-verdict", verdict}, fam.Inputs...)
	if out, err := p.plain(p.root, nil, append([]string{p.floor, "-record"}, args...)...); err != nil {
		rep.Refused["floor-skip"] = append(rep.Refused["floor-skip"], fmt.Sprintf("floor -record: %v %s", err, out))
		return
	}
	for range rounds {
		cmd := exec.Command(p.floor, args...)
		cmd.Dir = p.root
		res := measure(cmd)
		if res.exit != 0 {
			rep.Refused["floor-skip"] = append(rep.Refused["floor-skip"], fmt.Sprintf("floor exited %d", res.exit))
			continue
		}
		if rep.Counts["floor-skip"] == nil {
			rep.Counts["floor-skip"], rep.RSS["floor-skip"] = map[string][]uint64{}, map[string][]int64{}
		}
		rep.Counts["floor-skip"]["run"] = append(rep.Counts["floor-skip"]["run"], res.instructions)
		rep.RSS["floor-skip"]["run"] = append(rep.RSS["floor-skip"]["run"], probeMaxRSS(cmd.ProcessState))
	}
}

// contextLines counts, once: the subject package's own laws under ergo
// check (where ST would put the tests), and the rebuild of ergo a compiler
// test in Ergo pays after an edit of compiler/.
func (p *prober) contextLines(kind string, fam *probeFamily, rep *probeKindReport) {
	d, err := p.newFormDir("context", kind, "ergo-check")
	if err != nil {
		return
	}
	defer os.RemoveAll(d.dir)
	env := p.env(d, "ergo-check", fam.Forms["ergo-check"], 1, false)
	if fam.Subject != "" {
		c := p.countList(d, env, "ergo-check", [][]string{{p.ergo, "check", fam.Subject}})
		rep.Context = append(rep.Context, fmt.Sprintf("ergo check %s (the subject's own laws, cold, one run): %.1f G instructions, %s %s", fam.Subject, float64(c.Instructions)/1e9, probeClassify(c.Run).Class, probeClassify(c.Run).Reason))
	}
	if strings.HasPrefix(fam.Edit.File, "compiler/") {
		c := p.countList(d, probeEnv(env, []string{"GOFLAGS=-p=4"}), "go", [][]string{{"go", "run", "./scripts/linked"}, {"go", "build", "-o", filepath.Join(d.bin, "ergo"), "./cmd/ergo"}})
		rep.Context = append(rep.Context, fmt.Sprintf("the rebuild of ergo after an edit of compiler/ (go run ./scripts/linked && go build ./cmd/ergo, cold GOCACHE with std, one run): %.1f G instructions, exit %d", float64(c.Instructions)/1e9, c.Run.Exit))
	}
}

// ---------------------------------------------------------------- receipt

func (r *probeReport) text(fams probeFile, kinds []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Counted: user-mode instructions of the whole process tree (perf_event_open, inherited), %d rounds, the forms' order rotated each round, fresh caches each round; load %s at the start; GOMAXPROCS=4 on both sides, GOFLAGS=-p=4 on the Go forms.\n\n", r.Rounds, r.Load)
	for _, l := range r.Prime {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	for _, kind := range kinds {
		k := r.Kinds[kind]
		fam := fams.Families[kind]
		fmt.Fprintf(&b, "\n### %s: `%s`\n\n", kind, fam.GoTest)
		sides := map[string]probeSide{}
		for _, form := range probeForms {
			s := probeSide{Mutants: len(fam.Mutants), Counted: len(k.Refused[form]) == 0, Rows: map[string]probeStats{}}
			for _, m := range fam.Mutants {
				if m.Kills[form] == true {
					s.Kills++
				}
			}
			for _, sc := range probeScenarios {
				st := probeStatsOf(k.Counts[form][sc])
				st.WithinSpread = k.Within[form][sc]
				s.Rows[sc] = st
				if st.N == 0 {
					s.Counted = false
				}
			}
			sides[form] = s
		}
		fmt.Fprintf(&b, "Kills (equal coverage): ")
		for i, form := range probeForms {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %d of %d", form, sides[form].Kills, sides[form].Mutants)
		}
		b.WriteString(".\n\n")
		floorRun, floorSkip := probeStatsOf(k.Counts["floor-run"]["run"]), probeStatsOf(k.Counts["floor-skip"]["run"])
		b.WriteString("| form | scenario | median G instr | least | most | spread | x go | x control | x floor | peak RSS MiB |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, form := range probeForms {
			for _, sc := range probeScenarios {
				st := sides[form].Rows[sc]
				if st.N == 0 {
					fmt.Fprintf(&b, "| %s | %s | no count | | | | | | | |\n", form, sc)
					continue
				}
				floor := floorRun
				if sc == "warm" {
					floor = floorSkip
				}
				spread := fmt.Sprintf("%.1f %%", 100*st.Spread)
				if st.WithinSpread {
					spread += " (within spread)"
				}
				fmt.Fprintf(&b, "| %s | %s | %.3f | %.3f | %.3f | %s | %s | %s | %s | %.0f |\n", form, sc, st.Median/1e9, st.Least/1e9, st.Most/1e9, spread,
					probeRatio(st, sides["go"].Rows[sc]), probeRatio(st, sides["control"].Rows[sc]), probeRatio(st, floor), float64(probeMedianInt(k.RSS[form][sc]))/(1<<20))
			}
		}
		fmt.Fprintf(&b, "| floor-run | (cold, edit) | %.4f | %.4f | %.4f | %.1f %% | | | | %.0f |\n", floorRun.Median/1e9, floorRun.Least/1e9, floorRun.Most/1e9, 100*floorRun.Spread, float64(probeMedianInt(k.RSS["floor-run"]["run"]))/(1<<20))
		fmt.Fprintf(&b, "| floor-skip | (warm) | %.4f | %.4f | %.4f | %.1f %% | | | | %.0f |\n\n", floorSkip.Median/1e9, floorSkip.Least/1e9, floorSkip.Most/1e9, 100*floorSkip.Spread, float64(probeMedianInt(k.RSS["floor-skip"]["run"]))/(1<<20))
		for _, form := range probeForms {
			if w := k.Warm[form]; w != "" {
				fmt.Fprintf(&b, "- warm %s: %s", form, w)
				if st := k.Stages[form+" warm"]; st != nil {
					fmt.Fprintf(&b, "; stages files_parsed=%d packages_checked=%d go_compiles=%d processes=%d", st["files_parsed"], st["packages_checked"], st["go_compiles"], st["processes"])
				}
				b.WriteString("\n")
			}
		}
		for _, form := range append(slices.Clone(probeForms), "floor-skip") {
			for _, why := range k.Refused[form] {
				fmt.Fprintf(&b, "- refused %s: %s\n", form, firstLine(why))
			}
		}
		for _, l := range k.Context {
			fmt.Fprintf(&b, "- context: %s\n", l)
		}
		goSide := sides["go"]
		v, from := probeKindVerdict(goSide, map[string]probeSide{"ergo-test": sides["ergo-test"], "ergo-check": sides["ergo-check"]})
		fmt.Fprintf(&b, "\n**%s:** %s", kind, probeVerdictLine(v, from, goSide, sides[from]))
		if from != "" {
			e := sides[from]
			fmt.Fprintf(&b, "; all three to go: cold %s, warm %s, edit %s", probeRatio(e.Rows["cold"], goSide.Rows["cold"]), probeRatio(e.Rows["warm"], goSide.Rows["warm"]), probeRatio(e.Rows["edit"], goSide.Rows["edit"]))
		} else {
			fmt.Fprintf(&b, " (ergo-test %d of %d, ergo-check %d of %d kills)", sides["ergo-test"].Kills, len(fam.Mutants), sides["ergo-check"].Kills, len(fam.Mutants))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func probeRatio(a, b probeStats) string {
	if a.N == 0 || b.N == 0 || b.Median == 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", a.Median/b.Median)
}

func probeMedianInt(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

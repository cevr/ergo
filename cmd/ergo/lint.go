package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	sourcefile "ergo.local/prototype/internal/source"
)

const lintHelp = "usage: ergo lint [PATH...] [--preset P]... [--lint-config FILE] [--rules MANIFEST]... [--gate P] [--diff[=REV] [--purpose TEXT]] [--strict] [--json]\n" +
	"Runs the lint rules over checked source: the recommended preset, then the module's ergo.lint.json\n" +
	"(beside ergo.mod: version, extends, rules), then each --preset in order. Presets: recommended (the rules\n" +
	"with a published precision of at least 0.80), justifiers, strict, layout.\n" +
	"Each finding prints its rule's published precision (p0.NN), when the rule has one; ergo lint rules lists them.\n" +
	"--gate P fails the run (exit 1) when a finding comes from a rule whose precision is at least P (0 to 1);\n" +
	"the severity policy still applies, and either can fail the run.\n" +
	"--diff reports only the findings whose span meets a line added since HEAD (git diff -U0; an untracked\n" +
	"file is all added); --diff=REV compares with REV. --diff also builds the change's record (files and their\n" +
	"added and deleted lines, packages, commit subjects since REV, files that change together over the last\n" +
	"200 commits, skipping commits of more than 50 files, and the purpose: --purpose TEXT, else the branch\n" +
	"and the subjects), which --json prints as changeSet and the change preset's rules read.\n" +
	"--lint-config FILE reads that document instead of the module's; only it may select rule packs (packs).\n" +
	"--rules MANIFEST selects one more rule pack. A pack is a separate program; nothing else starts one.\n" +
	"Suppress one finding with // " + lint.DirectiveWord + " RULE -- REASON on the line above; the reason is required.\n" +
	"Codes: L000 lint-runner failure; L001 invalid or unused suppression; L1xx rules; namespace/rule a pack's rule.\n" +
	"Exit codes: 0 policy passed; 1 failed policy (an error finding, a warning with --strict, an incomplete run or unchecked source) or operational failure; 2 invalid invocation or configuration.\n"

const lintRulesHelp = "usage: ergo lint rules [--preset P]... [--lint-config FILE] [--rules MANIFEST]...\n" +
	"Prints every rule as JSON: the built-in rules, then each selected pack's, with its pack identity,\n" +
	"default and effective severity, requirements and options. It reads metadata only: no pack starts.\n" +
	"Without --lint-config it reads the ergo.lint.json of the module around the working directory.\n" +
	"Exit codes: 0 listed; 2 invalid invocation or configuration.\n"

const lintTestHelp = "usage: ergo lint test PATH... [--update] (--lint-config FILE | --rules MANIFEST...)\n" +
	"Runs the selected rule packs over each fixture, as ergo lint runs them: a PATH is an .ergo program, or a\n" +
	"directory whose .ergo files (sorted, not recursive) are each one. NAME.ergo expects NAME.lint.json: complete,\n" +
	"every pack rule's status, the pack findings and lint-runner errors, and the namespace/rule suppressions.\n" +
	"--update writes the expectations; it never changes a fixture.\n" +
	"Exit codes: 0 every fixture as expected; 1 a fixture that does not check, a missing or invalid expectation or a\n" +
	"mismatch; 2 invalid invocation: no fixture, no selected pack, or an invalid configuration.\n"

// lintReport is what `ergo lint --json` prints: the diagnostic report with
// lint merged, and every rule's status.
type lintReport struct {
	report.DiagnosticReport
	Rules []lint.RuleStatus `json:"rules"`
	Gate  *lintGate         `json:"gate,omitempty"`
	// ChangeSet is the change --diff linted (anti-slop AS6).
	ChangeSet *lint.ChangeSet `json:"changeSet,omitempty"`
}

// packSelection is the explicit pack selection of a lint command:
// --lint-config FILE, at most once, and each --rules MANIFEST.
type packSelection struct {
	config    string
	configSet bool
	rules     []string
}

// lintFlags takes --preset, --lint-config and --rules out of args.
func lintFlags(args []string) (presets []string, sel packSelection, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		if name != "--preset" && name != "--lint-config" && name != "--rules" {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 == len(args) {
				return nil, sel, nil, fmt.Errorf("%s requires a value", name)
			}
			value = args[i+1]
			i++
		}
		switch name {
		case "--preset":
			presets = append(presets, value)
		case "--lint-config":
			if sel.configSet {
				return nil, sel, nil, fmt.Errorf("--lint-config is given twice")
			}
			if len(value) == 0 {
				return nil, sel, nil, fmt.Errorf("--lint-config needs a file")
			}
			sel.config, sel.configSet = value, true
		case "--rules":
			if len(value) == 0 {
				return nil, sel, nil, fmt.Errorf("--rules needs a manifest path")
			}
			sel.rules = append(sel.rules, value)
		}
	}
	return presets, sel, rest, nil
}

// lintGate is the gate part of `ergo lint --json`: the precision asked
// for and whether the findings in scope passed it.
type lintGate struct {
	Precision float64 `json:"precision"`
	Passed    bool    `json:"passed"`
}

// lintScope is --gate P and --diff[=REV] of ergo lint.
type lintScope struct {
	gate    float64
	gateSet bool
	diff    string
	diffSet bool
	// purpose is --purpose TEXT, the change's stated purpose (--diff only).
	purpose    string
	purposeSet bool
}

// lintScopeFlags takes --gate and --diff out of args. --gate needs a
// precision from 0 to 1; --diff takes its revision only as --diff=REV
// (git's form for an optional value), so a FILE after it stays the FILE.
func lintScopeFlags(args []string) (scope lintScope, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		switch name {
		case "--gate":
			if scope.gateSet {
				return scope, nil, fmt.Errorf("--gate is given twice")
			}
			if !inline {
				if i+1 == len(args) {
					return scope, nil, fmt.Errorf("--gate requires a precision")
				}
				value = args[i+1]
				i++
			}
			p, perr := strconv.ParseFloat(value, 64)
			if perr != nil || p < 0 || p > 1 {
				return scope, nil, fmt.Errorf("--gate needs a precision from 0 to 1, not %q", value)
			}
			scope.gate, scope.gateSet = p, true
		case "--diff":
			if scope.diffSet {
				return scope, nil, fmt.Errorf("--diff is given twice")
			}
			if inline && value == "" {
				return scope, nil, fmt.Errorf("--diff= needs a revision")
			}
			scope.diff, scope.diffSet = "HEAD", true
			if inline {
				scope.diff = value
			}
		case "--purpose":
			if scope.purposeSet {
				return scope, nil, fmt.Errorf("--purpose is given twice")
			}
			if !inline {
				if i+1 == len(args) {
					return scope, nil, fmt.Errorf("--purpose requires a text")
				}
				value = args[i+1]
				i++
			}
			scope.purpose, scope.purposeSet = value, true
		default:
			rest = append(rest, args[i])
		}
	}
	if scope.purposeSet && !scope.diffSet {
		return scope, nil, fmt.Errorf("--purpose states a change's purpose, so it needs --diff")
	}
	return scope, rest, nil
}

// lintCommand lints one source or package folder, or with DIR/... every
// package DiscoverPackages selects there, each as alone; at a workspace
// root it first reports the workspace's rows (lintWorkspace).
func lintCommand(args []string) error {
	scope, args, err := lintScopeFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	presets, sel, rest, err := lintFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	opts, err := parseFileOptions("lint", rest)
	if err != nil {
		return invalidInvocation(err)
	}
	if recursivePattern(opts.file) {
		failed, err := lintWorkspace(opts)
		if err != nil {
			return err
		}
		err = recursiveCommand(opts, nil, func(one fileOptions) error { return lintPackage(one, scope, presets, sel) })
		if err == nil && failed {
			err = exitStatusError{code: 1, handled: true}
		}
		return err
	}
	return lintPackage(opts, scope, presets, sel)
}

// workspaceDivergentRequire is the rule of a member that requires another
// member's Go module by version with no replace line (lintWorkspace).
const workspaceDivergentRequire = "workspace-divergent-require"

// lintWorkspace reports the workspace rows of `ergo lint DIR/...` when DIR
// is a workspace root (docs/spec/build.md, Workspace), one line each on
// stdout before the packages' reports, or a JSON object each with --json:
// workspace-divergent-require, a member that requires another member's Go
// module by version with no replace, so it builds against a copy from the
// module cache and not the one beside it. failed is true when it reported
// one; elsewhere it reports nothing.
func lintWorkspace(opts fileOptions) (failed bool, err error) {
	dir := filepath.Dir(filepath.Clean(opts.file))
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return false, err
	}
	if root, err := sketchfront.IsWorkspaceRoot(nil, abs); err != nil || !root {
		return false, err
	}
	ws, err := sketchfront.DiscoverWorkspace(nil, abs)
	if err != nil {
		return false, invalidInvocation(err)
	}
	for _, d := range ws.Divergences {
		from, to := ws.Members[d.From], ws.Members[d.To]
		file := path.Join(from.Rel, sketchfront.GoModFile)
		target, err := filepath.Rel(from.Dir, to.Dir)
		if err != nil {
			return false, err
		}
		target = filepath.ToSlash(target)
		if !strings.HasPrefix(target, "../") {
			target = "./" + target
		}
		message := fmt.Sprintf("%s requires %s %s by version, but %s is the member %s beside it: it builds against the module cache's copy, not that one", from.Name(), d.Path, d.Version, d.Path, to.Name())
		fix := fmt.Sprintf("replace %s => %s", d.Path, target)
		if opts.json {
			if err := printJSON(struct {
				Rule    string `json:"rule"`
				File    string `json:"file"`
				Line    int    `json:"line"`
				Message string `json:"message"`
				Fix     string `json:"fix"`
			}{workspaceDivergentRequire, file, d.Line, message, fix}); err != nil {
				return false, err
			}
		} else if _, err := fmt.Fprintf(os.Stdout, "%s:%d: %s: %s; add %s\n", file, d.Line, workspaceDivergentRequire, message, fix); err != nil {
			return false, err
		}
	}
	return len(ws.Divergences) > 0, nil
}

// lintPackage lints the one source or package folder opts names.
func lintPackage(opts fileOptions, scope lintScope, presets []string, sel packSelection) error {
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	conf, err := lintConfiguration(l.source.Admitted(), presets, sel)
	if err != nil {
		return invalidInvocation(err)
	}
	in := lintInput(l)
	if scope.diffSet {
		if in.Change, err = changeSetOf(l.source.Admitted(), scope.diff, scope.purpose); err != nil {
			return err
		}
	}
	run := conf.Run(in)
	if in.Change != nil {
		run = run.InDiff(in, in.Change.Added)
	}
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	findings := l.result.ReportWithLint(l.snapshot, opts.strict, run.Advice())
	var gate *lintGate
	if scope.gateSet {
		gate = &lintGate{Precision: scope.gate, Passed: !lint.GateFails(run.Findings, scope.gate)}
		findings.PolicyPassed = findings.PolicyPassed && gate.Passed
	}
	if opts.json {
		err = printJSON(lintReport{DiagnosticReport: findings, Rules: run.Rules, Gate: gate, ChangeSet: in.Change})
	} else {
		err = findings.WriteText(os.Stdout, l.source.String())
	}
	if err != nil {
		return err
	}
	if gate != nil && !gate.Passed {
		return fmt.Errorf("lint failed the gate: a finding's rule has a precision of at least %g", scope.gate)
	}
	if !findings.PolicyPassed {
		return fmt.Errorf("lint failed policy")
	}
	return nil
}

// lintLoaded configures lint for the loaded program and runs it.
func lintLoaded(l loaded, presets []string, sel packSelection) (lint.Result, error) {
	conf, err := lintConfiguration(l.source.Admitted(), presets, sel)
	if err != nil {
		return lint.Result{}, err
	}
	return conf.Run(lintInput(l)), nil
}

// addedLines is what the change from rev to the working tree added under
// the program at path, keyed as lint names files: relative to the module
// root (the directory of ergo.mod above path, else path's directory), or
// "" for a program of one source file. An untracked file is all added.
func addedLines(path, rev string) (lint.Added, error) {
	root, single, err := diffRoot(path)
	if err != nil {
		return nil, err
	}
	git := gitAt(root)
	diff := ""
	// A repository with no commit yet has no HEAD: every file is untracked.
	if _, headErr := git("rev-parse", "--verify", "--quiet", "HEAD^{commit}"); headErr == nil || rev != "HEAD" {
		if diff, err = git("diff", "-U0", "--no-color", "--no-ext-diff", "--no-prefix", "--relative", rev, "--", "."); err != nil {
			return nil, err
		}
	}
	added := lint.ParseDiff(diff)
	untracked, err := git("ls-files", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	for _, f := range strings.Split(untracked, "\n") {
		if f != "" {
			added[f] = []lint.LineRange{{From: 1, To: math.MaxInt}}
		}
	}
	if single {
		return lint.Added{"": added[filepath.Base(path)]}, nil
	}
	return added, nil
}

// diffRoot is the directory a change of the program at path is read
// from: the module root (the directory of ergo.mod above path, else
// path's directory), or the file's directory for one source file (single).
func diffRoot(path string) (root string, single bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	single = !info.IsDir()
	dir := path
	if single {
		dir = filepath.Dir(path)
	}
	if single {
		return dir, true, nil
	}
	root = dir
	for probe := dir; ; probe = filepath.Dir(probe) {
		if _, err := os.Stat(filepath.Join(probe, "ergo.mod")); err == nil {
			return probe, false, nil
		}
		if filepath.Dir(probe) == probe {
			return root, false, nil
		}
	}
}

// gitAt runs git in root and returns its standard output.
func gitAt(root string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"--no-optional-locks", "-c", "core.quotePath=false"}, args...)...)
		cmd.Dir = root
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
}

// lintConfiguration reads the configuration (the --lint-config document,
// else the module's ergo.lint.json around path), loads the manifests of
// the packs it and --rules select, and configures the built-in rules and
// the packs. A module document that selects packs is refused: packs run
// only from an explicit selection (custom-lint.md). Nothing starts.
func lintConfiguration(path string, presets []string, sel packSelection) (*lint.Configuration, error) {
	var doc lint.Config
	var err error
	if sel.configSet {
		doc, err = readLintConfig(sel.config)
	} else {
		doc, err = lintConfig(path)
		if err == nil && len(doc.Packs) > 0 {
			err = fmt.Errorf("%s selects rule packs, which run only from an explicit selection; name it with --lint-config", filepath.Join(doc.Dir, lint.ConfigFile))
		}
	}
	if err != nil {
		return nil, err
	}
	packs, err := lint.LoadPacks(&doc, sel.rules)
	if err != nil {
		return nil, err
	}
	conf, err := lint.Builtin().ConfigureWith(doc, presets, packs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lint.ConfigFile, err)
	}
	return conf, nil
}

// readLintConfig reads the configuration document at path; its directory
// resolves the manifests it selects.
func readLintConfig(path string) (lint.Config, error) {
	data, err := sourcefile.ReadRegularFile(path, lint.MaxConfigBytes)
	if err != nil {
		return lint.Config{}, err
	}
	doc, err := lint.ParseConfig(data)
	if err != nil {
		return lint.Config{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return lint.Config{}, err
	}
	doc.Dir = filepath.Dir(absolute)
	return doc, nil
}

// lintInput is the program as lint reads it: each package's checked facts
// and parsed files, dependencies first.
func lintInput(l loaded) lint.Input {
	in := lint.InputOf(l.analysis)
	in.Checked = l.result.Checked && l.analysis.Checked() != nil
	return in
}

// lintConfig reads the ergo.lint.json beside the module's ergo.mod, found
// upward from path; with no module or no document it is the default.
func lintConfig(path string) (lint.Config, error) {
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "ergo.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return lint.DefaultConfig(), nil
		}
		dir = parent
	}
	data, err := sourcefile.ReadRegularFile(filepath.Join(dir, lint.ConfigFile), lint.MaxConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return lint.DefaultConfig(), nil
	}
	if err != nil {
		return lint.Config{}, err
	}
	doc, err := lint.ParseConfig(data)
	doc.Dir = dir
	return doc, err
}

// lintRulesCommand prints the rule inspection of the configuration.
func lintRulesCommand(args []string) error {
	presets, sel, rest, err := lintFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	if len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		return unknownOption("", rest[0])
	}
	if len(rest) > 0 {
		return invalidInvocation(fmt.Errorf("unexpected argument %q", rest[0]))
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	conf, err := lintConfiguration(dir, presets, sel)
	if err != nil {
		return invalidInvocation(err)
	}
	return printJSON(conf.Inspect())
}

// lintTestCommand runs the selected packs over source fixtures and compares
// each with its expectation.
func lintTestCommand(args []string) error {
	presets, sel, rest, err := lintFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	update := false
	var paths []string
	for _, a := range rest {
		switch {
		case a == "--update":
			update = true
		case strings.HasPrefix(a, "-"):
			return unknownOption("", a)
		default:
			paths = append(paths, a)
		}
	}
	fixtures, err := lintFixtures(paths)
	if err != nil {
		return invalidInvocation(err)
	}
	if !sel.configSet && len(sel.rules) == 0 {
		return invalidInvocation(fmt.Errorf("lint test needs --lint-config or --rules"))
	}
	var confs []*lint.Configuration
	for _, fixture := range fixtures {
		conf, err := lintConfiguration(fixture, presets, sel)
		if err != nil {
			return invalidInvocation(err)
		}
		if !conf.PacksSelected() {
			return invalidInvocation(fmt.Errorf("lint test needs a selected rule pack"))
		}
		if !update {
			if err := admitExpectation(expectationPath(fixture)); err != nil {
				return invalidInvocation(err)
			}
		}
		confs = append(confs, conf)
	}
	failed := 0
	for i, fixture := range fixtures {
		problem := runLintFixture(fixture, confs[i], update)
		if problem != "" {
			failed++
			fmt.Fprintf(os.Stdout, "FAIL %s: %s\n", fixture, problem)
			continue
		}
		fmt.Fprintf(os.Stdout, "ok   %s\n", fixture)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d lint fixtures failed", failed, len(fixtures))
	}
	return nil
}

// runLintFixture runs one fixture: "" when it matches its expectation (or
// --update wrote it), else what went wrong.
func runLintFixture(fixture string, conf *lint.Configuration, update bool) string {
	l, err := load(fixture, report.Target)
	if err != nil {
		return err.Error()
	}
	if !l.result.Checked {
		return "the fixture does not check"
	}
	got := lint.ExpectationOf(conf.Run(lintInput(l)))
	path := expectationPath(fixture)
	if update {
		if err := writeAtomically(path, got.Encode()); err != nil {
			return err.Error()
		}
		return ""
	}
	data, err := sourcefile.ReadRegularFile(path, lint.MaxConfigBytes)
	if err != nil {
		return "expectation: " + err.Error()
	}
	want, err := lint.ParseExpectation(data)
	if err != nil {
		return "invalid expectation " + path + ": " + err.Error()
	}
	return want.Diff(got)
}

// lintFixtures expands the paths: a file is one fixture, a directory its
// .ergo files, sorted, not recursive.
func lintFixtures(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() || filepath.Ext(p) != ".ergo" {
				return nil, fmt.Errorf("%s is not an .ergo file", p)
			}
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".ergo" {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no lint fixture")
	}
	return out, nil
}

// expectationPath is NAME.lint.json beside NAME.ergo.
func expectationPath(fixture string) string {
	return strings.TrimSuffix(fixture, ".ergo") + ".lint.json"
}

// admitExpectation refuses an existing expectation that is not a regular
// file, without opening it; a missing one fails its fixture later.
func admitExpectation(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expectation %s is not a regular file", path)
	}
	return nil
}

// writeAtomically replaces path with data through a temporary file in its
// directory.
func writeAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lint-expectation-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

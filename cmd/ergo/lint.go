package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/lint"
	sourcefile "ergo.local/prototype/internal/source"
)

const lintHelp = "usage: ergo lint FILE|DIR [--preset P]... [--lint-config FILE] [--rules MANIFEST]... [--strict] [--json] [--target go]\n" +
	"Runs the lint rules over checked source: the recommended preset, then the module's ergo.lint.json\n" +
	"(beside ergo.mod: version, extends, rules), then each --preset in order.\n" +
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

// lintCommand lints one source or package folder.
func lintCommand(args []string) error {
	presets, sel, rest, err := lintFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	opts, err := parseFileOptions("lint", rest)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	run, err := lintLoaded(l, presets, sel)
	if err != nil {
		return invalidInvocation(err)
	}
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	findings := l.result.ReportWithLint(l.snapshot, opts.strict, run.Advice())
	if opts.json {
		err = printJSON(lintReport{DiagnosticReport: findings, Rules: run.Rules})
	} else {
		err = findings.WriteText(os.Stdout, l.source.String())
	}
	if err != nil {
		return err
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
			return invalidInvocation(fmt.Errorf("unknown option %s", a))
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

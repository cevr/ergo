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

const lintHelp = "usage: ergo lint FILE|DIR [--preset P]... [--strict] [--json] [--target go]\n" +
	"Runs the lint rules over checked source: the recommended preset, then the module's ergo.lint.json\n" +
	"(beside ergo.mod: version, extends, rules), then each --preset in order.\n" +
	"Suppress one finding with // " + lint.DirectiveWord + " RULE -- REASON on the line above; the reason is required.\n" +
	"Codes: L000 lint-runner failure; L001 invalid or unused suppression; L1xx rules.\n" +
	"Exit codes: 0 policy passed; 1 failed policy (an error finding, a warning with --strict, an incomplete run or unchecked source) or operational failure; 2 invalid invocation or configuration.\n"

// lintReport is what `ergo lint --json` prints: the diagnostic report with
// lint merged, and every rule's status.
type lintReport struct {
	report.DiagnosticReport
	Rules []lint.RuleStatus `json:"rules"`
}

// lintCommand lints one source or package folder.
func lintCommand(args []string) error {
	var presets, rest []string
	for i := 0; i < len(args); i++ {
		if value, ok := strings.CutPrefix(args[i], "--preset="); ok {
			presets = append(presets, value)
			continue
		}
		if args[i] == "--preset" {
			if i+1 == len(args) {
				return invalidInvocation(fmt.Errorf("--preset requires a value"))
			}
			presets = append(presets, args[i+1])
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	opts, err := parseFileOptions("lint", rest)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	run, err := lintLoaded(l, presets)
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

// lintLoaded configures the built-in registry from the module's
// configuration and presets, and runs it over the loaded program.
func lintLoaded(l loaded, presets []string) (lint.Result, error) {
	doc, err := lintConfig(l.source.Admitted())
	if err != nil {
		return lint.Result{}, err
	}
	conf, err := lint.Builtin().Configure(doc, presets)
	if err != nil {
		return lint.Result{}, fmt.Errorf("%s: %w", lint.ConfigFile, err)
	}
	return conf.Run(lintInput(l)), nil
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
	return lint.ParseConfig(data)
}

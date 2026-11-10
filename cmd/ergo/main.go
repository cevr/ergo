// Command ergo checks, formats, builds and runs Ergo source on its one front
// end (internal/sketchfront). The command table (commands.go) classifies
// every command word; this file holds the options and loading of the
// commands that read one source, and check and diagnostics; build.go holds
// build and run, and test.go holds test.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	sourcefile "ergo.local/prototype/internal/source"
)

func main() {
	// The compiler's identity recalls its image digest from the cache root
	// it keys, so a command does not hash the whole binary each run.
	if root, err := build.DefaultRoot(); err == nil {
		producer.Remember(root)
	}
	if err := command(os.Args[1:]); err != nil {
		if exit, ok := err.(exitStatusError); ok {
			if !exit.handled && exit.message != "" {
				fmt.Fprintln(os.Stderr, exit.message)
			}
			os.Exit(exit.code)
		}
		fmt.Fprintln(os.Stderr, err)
		if _, usage := err.(usageError); usage {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// usageError is an invalid invocation: exit 2.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// exitStatusError exits with code, printing message unless the command
// already reported the failure (handled).
type exitStatusError struct {
	code    int
	message string
	handled bool
}

func (e exitStatusError) Error() string { return e.message }

func invalidInvocation(err error) error {
	if err == nil {
		return usageError{message: "invalid invocation"}
	}
	return usageError{message: err.Error()}
}

// printJSON prints v as indented JSON.
func printJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// printProjectionJSON prints v as compact JSON and a line feed.
func printProjectionJSON(v any) error {
	return json.NewEncoder(os.Stdout).Encode(v)
}

// sourceExtension is the extension of every source file (#221). Every
// other extension, the retired .ef included, gets the same refusal (#217:
// no rename hint and no special case for an old spelling).
const sourceExtension = ".ergo"

// requireSourceExtension is the one source-extension rule of every command.
func requireSourceExtension(path string) error {
	if filepath.Ext(path) != sourceExtension {
		return fmt.Errorf("source file must have %s extension", sourceExtension)
	}
	return nil
}

const diagnosticsHelp = "usage: ergo diagnostics FILE|DIR [--strict] [--json] [--target go]\n" +
	"Locations: one-based UTF-16 lines/columns in text; zero-based UTF-16 ranges and original UTF-8 byte spans in JSON.\n" +
	"A DIR is a package: each finding names its file relative to the module root (file in JSON).\n" +
	"Exit codes: 0 policy passed; 1 failed policy or source/operational failure; 2 invalid invocation.\n" +
	"With --json, source findings produce a report on stdout even when policy fails; operational failures produce no report and explain the error on stderr.\n"

// fileOptions are the options of a command that reads one source.
type fileOptions struct {
	// command is the command word the options belong to.
	command            string
	target, output     string
	strict, json, work bool
	// seed is test's --seed, a decimal uint64; empty when not given.
	seed string
	// states is test's --states, the budget of a reachability law's
	// search, a positive decimal; empty when not given.
	states string
	// receipt is test's --receipt FILE: the law receipt to write (L10).
	receipt string
	// law is test's --law, the one law to run, by its report's title
	// (Design.Safe); skipLaw is test's --skip-law, a law not to run. Each
	// is empty when not given.
	law, skipLaw string
	file         string
	// args are run's program arguments: every word after the source, as
	// go run passes them, less one "--" right after the source.
	args []string
}

// parseFileOptions reads the options of command. --strict and --json belong
// to diagnostics, -o to build, -work to build and test, and --seed N (or
// --seed=N), --states N, --law NAME and --skip-law NAME to test; any other option is unknown. A target other than Go is
// the EF110 refusal. For run, the options stand before the source, and the
// words after it are the program's arguments (`ergo run FILE|DIR [--]
// ARGS...`, as `go run`).
func parseFileOptions(command string, args []string) (fileOptions, error) {
	opts := fileOptions{target: report.Target, command: command}
	var positional []string
	seeded := false
	budgeted := false
	selected := false
	for i := 0; i < len(args); i++ {
		if command == "run" && len(positional) == 1 {
			opts.args = args[i:]
			if opts.args[0] == "--" {
				opts.args = opts.args[1:]
			}
			break
		}
		if value, ok := strings.CutPrefix(args[i], "--seed="); ok {
			opts.seed, seeded = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--states="); ok {
			opts.states, budgeted = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--law="); ok {
			opts.law, selected = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--skip-law="); ok {
			opts.skipLaw, selected = value, true
			continue
		}
		switch args[i] {
		case "--target", "-o", "--seed", "--states", "--receipt", "--law", "--skip-law":
			if i+1 == len(args) {
				return opts, fmt.Errorf("%s requires a value", args[i])
			}
			switch args[i] {
			case "--target":
				opts.target = args[i+1]
			case "-o":
				opts.output = args[i+1]
			case "--states":
				opts.states, budgeted = args[i+1], true
			case "--receipt":
				opts.receipt = args[i+1]
			case "--law":
				opts.law, selected = args[i+1], true
			case "--skip-law":
				opts.skipLaw, selected = args[i+1], true
			default:
				opts.seed, seeded = args[i+1], true
			}
			i++
		case "--strict":
			opts.strict = true
		case "--json":
			opts.json = true
		case "-work":
			opts.work = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, fmt.Errorf("unknown option %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if diagnostic, unsupported := report.UnsupportedTarget(opts.target); unsupported {
		return opts, fmt.Errorf("%s: %s", diagnostic.Code, diagnostic.Message)
	}
	switch {
	case len(positional) == 0:
		return opts, fmt.Errorf("source file required")
	case opts.strict && command != "diagnostics":
		return opts, fmt.Errorf("--strict is only supported by diagnostics")
	case opts.json && command != "diagnostics" && command != "run" && command != "test":
		return opts, fmt.Errorf("--json is only supported by diagnostics, run and test")
	case opts.output != "" && command != "build":
		return opts, fmt.Errorf("-o is only supported by build")
	case opts.work && command != "build" && command != "test":
		return opts, fmt.Errorf("-work is only supported by build and test")
	case opts.receipt != "" && command != "test":
		return opts, fmt.Errorf("--receipt is only supported by test")
	case seeded && command != "test":
		return opts, fmt.Errorf("--seed is only supported by test")
	case seeded && !validSeed(opts.seed):
		return opts, fmt.Errorf("--seed takes a decimal number from 0 to 18446744073709551615, not %q", opts.seed)
	case budgeted && command != "test":
		return opts, fmt.Errorf("--states is only supported by test")
	case budgeted && !validStates(opts.states):
		return opts, fmt.Errorf("--states takes a positive decimal number of states, not %q", opts.states)
	case selected && command != "test":
		return opts, fmt.Errorf("--law and --skip-law are only supported by test")
	case selected && (!validLaw(opts.law) || !validLaw(opts.skipLaw) || opts.law+opts.skipLaw == ""):
		return opts, fmt.Errorf("--law and --skip-law take a law's name, as a report shows it (Design.Safe)")
	case len(positional) != 1:
		return opts, fmt.Errorf("incorrect arguments for %s", command)
	}
	opts.file = positional[0]
	if isPackageFolder(opts.file) {
		return opts, nil
	}
	return opts, requireSourceExtension(opts.file)
}

// isPackageFolder reports whether path names a folder: a package of every
// .ergo file in it, which may import the packages of its module (196b).
// Any other path is one source file, with the one-file meaning it has
// always had; so is a path with the .ergo extension, even when it names a
// folder, which is then refused as a source that is not a regular file.
func isPackageFolder(path string) bool {
	if filepath.Ext(path) == sourceExtension {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// loaded is one source admitted once and checked: its location, the bytes
// read from the open file, the analysis and its diagnostics-only result.
// name names the executable a build writes.
type loaded struct {
	source   receipt.Location
	name     string
	snapshot report.SourceSnapshot
	analysis sketchfront.Analysis
	result   *report.Result
}

// load admits a source path once and checks it. The spelling is located
// once (receipt.LocateSource): diagnostics name the admitted path and
// //line directives name it too, and the bytes come from one non-blocking
// open of the located file, which must be a regular file. A folder is a
// package: the loader reads it and the local packages it imports
// (sketchfront.CheckDir), and each diagnostic names its file.
func load(path, target string) (loaded, error) {
	if isPackageFolder(path) {
		return loadFolder(path, target)
	}
	if err := requireSourceExtension(path); err != nil {
		return loaded{}, err
	}
	source, err := receipt.LocateSource(path)
	if err != nil {
		return loaded{}, err
	}
	uri, err := report.FileURI(source.Admitted())
	if err != nil {
		return loaded{}, err
	}
	file, err := sourcefile.Open(source.Path())
	if err != nil {
		return loaded{}, err
	}
	text, err := file.Read(0)
	file.Close()
	if err != nil {
		return loaded{}, err
	}
	analysis := sketchfront.Check(string(text))
	return loaded{
		source:   source,
		name:     strings.TrimSuffix(filepath.Base(source.String()), filepath.Ext(source.String())),
		snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Text: string(text)},
		analysis: analysis,
		result:   analysis.Result(string(text), target),
	}, nil
}

// loadFolder admits a package folder once and checks its program. The
// executable is named after the folder, as go build names it.
func loadFolder(path, target string) (loaded, error) {
	source, err := receipt.LocateSource(path)
	if err != nil {
		return loaded{}, err
	}
	uri, err := report.FileURI(source.Admitted())
	if err != nil {
		return loaded{}, err
	}
	analysis, err := sketchfront.CheckDir(source.Admitted())
	if err != nil {
		return loaded{}, err
	}
	return loaded{
		source:   source,
		name:     filepath.Base(source.Admitted()),
		snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Files: analysis.Files()},
		analysis: analysis,
		result:   analysis.Result(analysis.Text(), target),
	}, nil
}

// refuseUnchecked reports a source that does not check and returns the
// "check failed" error; that refusal acquires the producer identity to
// print its facts. build, and run and test with --json, print the check
// envelope on stdout; run and test print one `file:line:col: error CODE:
// message` line per finding on stderr, as diagnostics prints them.
func refuseUnchecked(l loaded, opts fileOptions) error {
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	if opts.json || opts.command == "build" {
		_ = printJSON(l.result.CheckEnvelope())
	} else {
		_ = l.result.Report(l.snapshot, false).WriteText(os.Stderr, l.source.String())
	}
	return fmt.Errorf("check failed")
}

// checkCommand prints the check envelope; exit 1 when the source does not
// check.
func checkCommand(args []string) error {
	opts, err := parseFileOptions("check", args)
	if err != nil {
		return err
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	if err := printProjectionJSON(l.result.CheckEnvelope()); err != nil {
		return err
	}
	if !l.result.Checked {
		return fmt.Errorf("check failed")
	}
	return nil
}

const inspectHelp = "usage: ergo inspect FILE|DIR [--target go]\n\n" +
	"Prints, as JSON, what the program reaches of Go (docs/tooling.md, inspect):\n" +
	"each Go import with its binding's provenance and the declarations the\n" +
	"binding refuses, with their reasons; for each function, the Go symbols its\n" +
	"code reaches and the uninterruptible host waits among them; for each Go\n" +
	"symbol, its Go declaration, Ergo signature, tier (pure or foreign),\n" +
	"provenance and wait (none, interruptible, uninterruptible). It runs no Go\n" +
	"tool.\n\n" +
	"Exit codes: 0 printed; 1 the source does not check.\n\n" +
	inspectMachineHelp

// inspectGoCommand prints, as JSON, what the program reaches of Go
// (sketchfront.InspectGo; docs/tooling.md, inspect); exit 1 when the
// source does not check.
func inspectGoCommand(args []string) error {
	opts, err := parseFileOptions("inspect", args)
	if err != nil {
		return err
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	pkgs := l.analysis.Checked()
	if !l.result.Checked || pkgs == nil {
		return refuseUnchecked(l, opts)
	}
	return printJSON(sketchfront.InspectGo(pkgs))
}

// diagnosticsCommand prints the diagnostic report as text or JSON; exit 1
// when the policy fails, 2 on an invalid invocation.
func diagnosticsCommand(args []string) error {
	opts, err := parseFileOptions("diagnostics", args)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	findings := l.result.Report(l.snapshot, opts.strict)
	if opts.json {
		err = printJSON(findings)
	} else {
		err = findings.WriteText(os.Stdout, l.source.String())
	}
	if err != nil {
		return err
	}
	if !findings.PolicyPassed {
		return fmt.Errorf("diagnostics failed policy")
	}
	return nil
}

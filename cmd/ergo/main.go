// Command ergo checks, formats, builds and runs Ergo source on its one front
// end (internal/sketchfront). The command table (commands.go) classifies
// every command word; this file holds the options and loading of the
// commands that read one source, and check and diagnostics; build.go holds
// build and run, and test.go holds test.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/internal/sketchfront"
	sourcefile "ergo.local/prototype/internal/source"
	"ergo.local/prototype/internal/stage"
)

func main() {
	tuneGC()
	// The checked std is in the binary (compile-speed CS1).
	sketchfront.UseEmbeddedStd(true)
	// The compiler's identity recalls its image digest from the cache root
	// it keys, so a command does not hash the whole binary each run.
	if root, err := build.DefaultRoot(); err == nil {
		producer.Remember(root)
		// The checked std snapshot lives beside the emission cache (CHK1b).
		sketchfront.UseStdCache(root)
	}
	args, profCfg, err := extractProfileConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
	prof, err := startProfileSession(profCfg, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ergo: profile:", err)
		os.Exit(1)
	}
	err = command(args)
	if perr := prof.finish(); perr != nil {
		fmt.Fprintln(os.Stderr, "ergo: profile:", perr)
		if err == nil {
			err = perr
		}
	}
	if werr := sketchfront.WriteStageReport(); werr != nil {
		fmt.Fprintln(os.Stderr, "ergo: stage report:", werr)
	}
	if err != nil {
		if exit, ok := err.(exitStatusError); !ok || !exit.handled && exit.message != "" {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(exitCode(err))
	}
}

// exitCode is the process exit code of a command's error: an
// exitStatusError's code, 2 for an invalid invocation (usageError), and 1
// for every other failure.
func exitCode(err error) int {
	switch e := err.(type) {
	case nil:
		return 0
	case exitStatusError:
		return e.code
	case usageError:
		return 2
	}
	return 1
}

// usageError is an invalid invocation: exit 2.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// unknownOption is the one refusal of an option a command does not take:
// an invalid invocation, so every command exits 2 on it.
func unknownOption(command, option string) error {
	if command == "" {
		return usageError{message: "unknown option " + option}
	}
	return usageError{message: command + ": unknown option " + option}
}

// exitStatusError exits with code, printing message unless the command
// already reported the failure (handled).
type exitStatusError struct {
	code    int
	message string
	handled bool
}

func (e exitStatusError) Error() string { return e.message }

// flagError is the refusal of a flag set's parse error: an option the set
// does not define is the one unknown-option refusal, and any other parse
// error (an option without its value) is usage.
func flagError(err error, usage string) error {
	if option, ok := strings.CutPrefix(err.Error(), "flag provided but not defined: "); ok {
		return unknownOption("", option)
	}
	return usageError{message: usage}
}

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

// sourceExtension is the extension of every source file. Every
// other extension, the retired .ef included, gets the same refusal (the needs and provides rename:
// no rename hint and no special case for an old spelling).
const sourceExtension = ".ergo"

// requireSourceExtension is the one source-extension rule of every command.
func requireSourceExtension(path string) error {
	if filepath.Ext(path) != sourceExtension {
		return fmt.Errorf("source file must have %s extension", sourceExtension)
	}
	return nil
}

const diagnosticsHelp = "usage: ergo diagnostics FILE|DIR [--lint-config FILE] [--rules MANIFEST]... [--strict] [--json] [--target go]\n" +
	"Locations: one-based UTF-16 lines/columns in text; zero-based UTF-16 ranges and original UTF-8 byte spans in JSON.\n" +
	"A DIR is a package: each finding names its file relative to the module root (file in JSON).\n" +
	"Lint advice (ergo lint) follows the compiler's findings, under the module's ergo.lint.json; --strict fails policy on a warning too.\n" +
	"Exit codes: 0 policy passed; 1 failed policy or source/operational failure; 2 invalid invocation.\n" +
	"With --json, source findings produce a report on stdout even when policy fails; operational failures produce no report and explain the error on stderr.\n"

// fileOptions are the options of a command that reads one source.
type fileOptions struct {
	// command is the command word the options belong to.
	command            string
	target, output     string
	strict, json, work bool
	// locked is build's and run's --locked: a stale or missing law
	// ledger entry refuses the build (owner q122).
	locked bool
	// emitted is build's and test's --emitted: print the digest of the Go
	// module the command would build (sketchfront.EmittedDigest) and build
	// nothing (gate-speed GS8).
	emitted bool
	// debug is build's, run's and test's --debug: the link keeps the symbol
	// table and DWARF, which it leaves out by default (compile-speed CS7).
	debug bool
	// release is build's --release: the build compiles with the
	// program's CPU profile, default.pgo beside its source (go build
	// -pgo; U4 PGO-RELEASE). profile is that file's absolute path, set by
	// buildCommand (releaseProfile).
	release bool
	profile string
	// goTrace is build's, run's and test's --go-trace: the program links
	// runtime/obs (the ergogotrace tag), and ERGO_GO_TRACE then shows its
	// task tree to Go's tracer and pprof (RT-OBS).
	goTrace bool
	// explore is test's --explore: the tests run under the controlled
	// scheduler (runtime.md, Explored schedules). search is its mode
	// (dfs, pct, random, or replay with --schedule) and bounds; with no
	// mode the tests run once, under the cooperative schedule.
	explore bool
	search  exploreOptions
	// seed is test's --seed, a decimal uint64; empty when not given.
	seed string
	// states is test's --states, the budget of a reachability law's
	// search, a positive decimal; empty when not given.
	states string
	// evalSteps and evalBytes are check's --eval-steps N and --eval-bytes
	// N, the budgets of check-time evaluation, positive decimals; empty
	// when not given.
	evalSteps, evalBytes string
	// receipt is test's --receipt FILE: the law receipt to write (L10).
	receipt string
	// law is test's --law, the one law to run, by its report's title
	// (Design.Safe); skipLaw is test's --skip-law, a law not to run. Each
	// is empty when not given.
	law, skipLaw string
	// run is test's --run NAME: the one test or law the test binary runs,
	// plain or explored; empty runs every one (X8).
	run string
	// uncached is test's --count 1: the run neither replays nor keeps a
	// cached result (testcache.go), as go test -count=1.
	uncached bool
	// affected is test's --affected [BASE], --list and --why (affected.go).
	affected affectedOptions
	// trace is test's --trace: after the run, the steps of every scene
	// script played (printSteps). A traced run is never cached.
	trace bool
	// native is test's --native: every obligation runs in the native test binary
	// (tier 2), never on the check-time evaluator (TESTSPEED FT1). The gate
	// and CI use it.
	native bool
	// mutants is test's --mutants: run .ergo mutant ledger rows in memory
	// using graph read sets and reach-digest verdict caching (MN1).
	mutants bool
	// fresh is check's and test's --fresh: the run reads no stored
	// answer, result or verdict (warm answers, the test result cache, the
	// law-verdict store, and with --mutants the stored mutant verdicts),
	// runs every obligation again and keeps each pass as a run without it
	// does (useFresh).
	fresh bool
	// derived is test's --derived with --mutants: derive typed mutants from
	// the checked .ergo AST in addition to any hand ledger rows (MN2).
	derived bool
	// pruneCases is test's --prune-cases: replay stored property tapes in
	// ergo.cases and remove those that now pass (PROPERTY-TESTING P8).
	pruneCases bool
	// sharding holds test's --parallel, -j/--jobs, --shard, --timings and
	// --update-timings options (TEST-SHARD-1).
	sharding testShardOptions
	file     string
	// more are test's targets after the first (`ergo test A B C/...`),
	// each a file, a folder or a pattern; none for every other command.
	more []string
	// args are run's program arguments: every word after the source, as
	// go run passes them, less one "--" right after the source.
	args []string
	// platform is the GOOS/GOARCH the command checks for (platformOf).
	platform gobind.Platform
	// level is build's CPU level, the third part of --platform
	// OS/ARCH/LEVEL (linux/amd64/v3, darwin/arm64/v8.2): GOAMD64 or
	// GOARM64 of the build (build.Toolchain.Level); empty is the
	// environment's, else Go's default.
	level string
	// phone is the phone platform a build is for (gobind.Phones), whose
	// bindings are platform's; zero for any other platform. A phone build
	// writes the module and stops before the link (buildPhone).
	phone gobind.Platform
	// goText is build's -go-text DIR: build through IR and write the Go
	// text of what was compiled into DIR (buildGoText).
	goText string
	// report is build's -report KIND: "copies" prints the copy report on
	// stderr (OWN1, REUSE RU0; the sites of ergo inspect copies); empty
	// prints none.
	report string
	// noViews is build's -views=off: no Go call takes a view of a
	// string's bytes (LAWFFI LF2b; lower.Unit.NoViews).
	noViews bool
	// fallback is -fallback=off|on (default off, or ERGO_FALLBACK=on):
	// when off, a body the IR builder cannot lower is an error naming it
	// and its reason, not a text body (FALLBACK-OFF; lower.Unit.NoFallback).
	fallback string
	// rewrites is build's -rewrites=on|proved|off|canary[=N] (lower.Unit.Rewrites):
	// which rewrites over functions the run build applies; empty is on.
	rewrites string
}

// parseFileOptions reads the options of command. --strict and --json belong
// to diagnostics, -o, --platform, -go-text DIR and -report copies to build, -work and --emitted to build and test,
// --debug to build, run and test, --seed N (or --seed=N) to check and test, --eval-steps N and
// --eval-bytes N to check, and --states N, --law NAME, --skip-law NAME,
// --run NAME (or --run=NAME), --receipt FILE and --explore to test; any
// other option is unknown. A target other than Go is the EF110 refusal,
// and so is a platform outside the target list. For run, the options stand
// before the source, and the
// words after it are the program's arguments (`ergo run FILE|DIR [--]
// ARGS...`, as `go run`).
//
// Every refusal here is an invalid invocation (exit 2), whatever the
// command: an unknown option, an option given to a command that does not
// take it, a missing or extra source, a source without the extension, and
// an unsupported target or platform. A source that names no file is found
// later, by load: an operational failure (exit 1).
func parseFileOptions(command string, args []string) (fileOptions, error) {
	opts, err := fileOptionsOf(command, args)
	if err != nil {
		return opts, invalidInvocation(err)
	}
	return opts, nil
}

// fileOptionsOf is parseFileOptions before its refusal is classified.
func fileOptionsOf(command string, args []string) (fileOptions, error) {
	opts := fileOptions{target: report.Target, command: command, search: exploreDefaults("")}
	var positional []string
	seeded, searched := false, false
	budgeted := false
	selected, ran := false, false
	laws, skips := 0, 0
	platform, platformed := "", false
	stepsGiven, bytesGiven := false, false
	count, counted := "", false
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
		if value, ok := strings.CutPrefix(args[i], "--run="); ok {
			opts.run, ran = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--affected="); ok {
			opts.affected.on, opts.affected.base = true, value
			if value == "" || strings.HasPrefix(value, "-") {
				return opts, fmt.Errorf("--affected= takes a commit, not %q", value)
			}
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--count="); ok {
			count, counted = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--states="); ok {
			opts.states, budgeted = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--eval-steps="); ok {
			opts.evalSteps, stepsGiven = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--eval-bytes="); ok {
			opts.evalBytes, bytesGiven = value, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "-fallback="); ok {
			if value != "on" && value != "off" {
				return opts, fmt.Errorf("-fallback takes on or off, not %q", value)
			}
			opts.fallback = value
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "-rewrites="); ok {
			if _, _, err := lower.ParseRewrites(value); err != nil {
				return opts, err
			}
			opts.rewrites = value
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--law="); ok {
			opts.law, selected, laws = value, true, laws+1
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--skip-law="); ok {
			opts.skipLaw, selected, skips = value, true, skips+1
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--shard="); ok {
			sp, err := parseShardSpec(value)
			if err != nil {
				return opts, err
			}
			opts.sharding.shard = sp
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--jobs="); ok {
			n, err := parseJobsCount(value)
			if err != nil {
				return opts, err
			}
			opts.sharding.jobs, opts.sharding.jobsSet = n, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "-j="); ok {
			n, err := parseJobsCount(value)
			if err != nil {
				return opts, err
			}
			opts.sharding.jobs, opts.sharding.jobsSet = n, true
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--timings="); ok {
			if value == "" {
				return opts, fmt.Errorf("--timings requires a file path")
			}
			opts.sharding.timingsPath = value
			continue
		}
		if value, ok := strings.CutPrefix(args[i], "--update-timings="); ok {
			if value == "" {
				return opts, fmt.Errorf("--update-timings= requires a file path")
			}
			opts.sharding.updateTimings = true
			opts.sharding.updatePath = value
			continue
		}
		took, err := parseExploreFlag(&opts.search, args, i)
		if err != nil {
			return opts, err
		}
		if took > 0 {
			searched = true
			i += took - 1
			continue
		}
		switch args[i] {
		case "--target", "-o", "--seed", "--states", "--receipt", "--law", "--skip-law", "--platform", "--eval-steps", "--eval-bytes", "--run", "--count", "-go-text", "-report", "-j", "--jobs", "--shard", "--timings":
			if i+1 == len(args) {
				return opts, fmt.Errorf("%s requires a value", args[i])
			}
			switch args[i] {
			case "-j", "--jobs":
				n, err := parseJobsCount(args[i+1])
				if err != nil {
					return opts, err
				}
				opts.sharding.jobs, opts.sharding.jobsSet = n, true
			case "--shard":
				sp, err := parseShardSpec(args[i+1])
				if err != nil {
					return opts, err
				}
				opts.sharding.shard = sp
			case "--timings":
				if args[i+1] == "" {
					return opts, fmt.Errorf("--timings requires a file path")
				}
				opts.sharding.timingsPath = args[i+1]
			case "--count":
				count, counted = args[i+1], true
			case "-report":
				opts.report = args[i+1]
			case "--eval-steps":
				opts.evalSteps, stepsGiven = args[i+1], true
			case "--eval-bytes":
				opts.evalBytes, bytesGiven = args[i+1], true
			case "--target":
				opts.target = args[i+1]
			case "-o":
				opts.output = args[i+1]
			case "--platform":
				platform, platformed = args[i+1], true
			case "--states":
				opts.states, budgeted = args[i+1], true
			case "--receipt":
				opts.receipt = args[i+1]
			case "--law":
				opts.law, selected, laws = args[i+1], true, laws+1
			case "--skip-law":
				opts.skipLaw, selected, skips = args[i+1], true, skips+1
			case "--run":
				opts.run, ran = args[i+1], true
			case "-go-text":
				opts.goText = args[i+1]
			default:
				opts.seed, seeded = args[i+1], true
			}
			i++
		case "--affected":
			// BASE is the next word once the source is given and the word
			// is no option: `ergo test ./... --affected main`.
			opts.affected.on = true
			if len(positional) > 0 && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				opts.affected.base = args[i+1]
				i++
			}
		case "--list":
			opts.affected.list = true
		case "--why":
			opts.affected.why = true
		case "--strict":
			opts.strict = true
		case "--json":
			opts.json = true
		case "-work":
			opts.work = true
		case "--locked":
			opts.locked = true
		case "-views=off":
			opts.noViews = true
		case "--emitted":
			opts.emitted = true
		case "--debug":
			opts.debug = true
		case "--release":
			opts.release = true
		case "--native":
			opts.native = true
		case "--mutants":
			opts.mutants = true
		case "--fresh":
			opts.fresh = true
		case "--derived":
			opts.derived = true
		case "--prune-cases":
			opts.pruneCases = true
		case "--parallel":
			opts.sharding.parallel = true
		case "--update-timings":
			opts.sharding.updateTimings = true
		case "--go-trace":
			opts.goTrace = true
		case "--explore":
			opts.explore = true
			if i+1 < len(args) && slices.Contains(exploreModes, args[i+1]) {
				opts.search.mode = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, unknownOption("", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if diagnostic, unsupported := report.UnsupportedTarget(opts.target); unsupported {
		return opts, fmt.Errorf("%s: %s", diagnostic.Code, diagnostic.Message)
	}
	switch {
	case len(positional) == 0 && !opts.mutants:
		return opts, fmt.Errorf("source file required")
	case opts.strict && command != "diagnostics" && command != "lint" && command != "check" && !(command == "test" && opts.mutants && opts.derived):
		return opts, fmt.Errorf("--strict is only supported by diagnostics, lint, check and test --mutants --derived")
	case opts.json && command != "diagnostics" && command != "lint" && command != "run" && command != "test" && command != "inspect machine":
		return opts, fmt.Errorf("--json is only supported by diagnostics, lint, run, test and inspect machine")
	case opts.output != "" && command != "build":
		return opts, fmt.Errorf("-o is only supported by build")
	case platformed && command != "build":
		return opts, fmt.Errorf("--platform is only supported by build")
	case opts.goText != "" && command != "build":
		return opts, fmt.Errorf("-go-text is only supported by build")
	case opts.noViews && command != "build":
		return opts, fmt.Errorf("-views=off is only supported by build")
	case opts.fallback != "" && command != "build" && command != "run" && command != "test":
		return opts, fmt.Errorf("-fallback is only supported by build, run and test")
	case opts.report != "" && command != "build":
		return opts, fmt.Errorf("-report is only supported by build")
	case opts.rewrites != "" && command != "build":
		return opts, fmt.Errorf("-rewrites is only supported by build")
	case opts.report != "" && opts.report != "copies":
		return opts, fmt.Errorf("-report takes copies, not %q", opts.report)
	case opts.work && command != "build" && command != "test":
		return opts, fmt.Errorf("-work is only supported by build and test")
	case opts.locked && command != "build" && command != "run":
		return opts, fmt.Errorf("--locked is only supported by build and run")
	case opts.emitted && command != "build" && command != "test":
		return opts, fmt.Errorf("--emitted is only supported by build and test")
	case opts.debug && command != "build" && command != "run" && command != "test":
		return opts, fmt.Errorf("--debug is only supported by build, run and test")
	case opts.release && command != "build":
		return opts, fmt.Errorf("--release is only supported by build")
	case opts.native && command != "test":
		return opts, fmt.Errorf("--native is only supported by test")
	case opts.goTrace && command != "build" && command != "run" && command != "test":
		return opts, fmt.Errorf("--go-trace is only supported by build, run and test")
	case opts.receipt != "" && command != "test":
		return opts, fmt.Errorf("--receipt is only supported by test")
	case (opts.explore || searched) && command != "test":
		return opts, fmt.Errorf("--explore is only supported by test")
	case searched && opts.search.mode == "":
		return opts, fmt.Errorf("--preempt, --schedules and --depth need --explore dfs, pct or random")
	case seeded && command != "test" && command != "check":
		return opts, fmt.Errorf("--seed is only supported by check and test")
	case ran && command != "test":
		return opts, fmt.Errorf("--run is only supported by test")
	case counted && command != "test":
		return opts, fmt.Errorf("--count is only supported by test")
	case counted && count != "1":
		return opts, fmt.Errorf("--count takes 1, a run that ignores the cached result, not %q", count)
	case ran && opts.run == "":
		return opts, fmt.Errorf("--run takes the name of a test or a law")
	case seeded && !validSeed(opts.seed):
		return opts, fmt.Errorf("--seed takes a decimal number from 0 to 18446744073709551615, not %q", opts.seed)
	case budgeted && command != "test":
		return opts, fmt.Errorf("--states is only supported by test")
	case budgeted && !validStates(opts.states):
		return opts, fmt.Errorf("--states takes a positive decimal number of states, not %q", opts.states)
	case (stepsGiven || bytesGiven) && command != "check":
		return opts, fmt.Errorf("--eval-steps and --eval-bytes are only supported by check")
	case stepsGiven && !validCount(opts.evalSteps):
		return opts, fmt.Errorf("--eval-steps takes a positive decimal number of steps, not %q", opts.evalSteps)
	case bytesGiven && !validCount(opts.evalBytes):
		return opts, fmt.Errorf("--eval-bytes takes a positive decimal number of bytes, not %q", opts.evalBytes)
	case selected && command != "test":
		return opts, fmt.Errorf("--law and --skip-law are only supported by test")
	case opts.affected.on && command != "test":
		return opts, fmt.Errorf("--affected is only supported by test")
	case (opts.affected.list || opts.affected.why) && !opts.affected.on:
		return opts, fmt.Errorf("--list and --why need --affected")
	case opts.affected.on && (selected || ran || opts.receipt != "" || opts.emitted || opts.explore || searched || opts.work):
		return opts, fmt.Errorf("--affected selects the obligations itself: it takes no --law, --skip-law, --run, --receipt, --emitted, --explore or -work")
	case opts.mutants && command != "test":
		return opts, fmt.Errorf("--mutants is only supported by test")
	case opts.fresh && command != "test" && command != "check":
		return opts, fmt.Errorf("--fresh is only supported by check and test")
	case opts.derived && !opts.mutants:
		return opts, fmt.Errorf("--derived needs --mutants")
	case opts.mutants && (opts.affected.on || selected || opts.receipt != "" || opts.emitted || opts.explore || searched || opts.work || opts.pruneCases):
		return opts, fmt.Errorf("--mutants selects tests itself: it takes no --affected, --law, --skip-law, --receipt, --emitted, --explore, --prune-cases or -work")
	case opts.pruneCases && command != "test":
		return opts, fmt.Errorf("--prune-cases is only supported by test")
	case opts.pruneCases && (opts.affected.on || opts.explore || searched || opts.receipt != "" || opts.emitted):
		return opts, fmt.Errorf("--prune-cases takes no --affected, --explore, --receipt or --emitted")
	case opts.sharding.active() && command != "test":
		return opts, fmt.Errorf("--parallel, -j/--jobs, --shard, --timings and --update-timings are only supported by test")
	case opts.sharding.updateTimings && opts.sharding.updatePath == "" && opts.sharding.timingsPath == "":
		return opts, fmt.Errorf("--update-timings requires --timings=FILE or --update-timings=FILE")
	case opts.sharding.active() && (opts.mutants || opts.affected.on || opts.emitted):
		return opts, fmt.Errorf("--parallel/--shard/--timings take no --mutants, --affected or --emitted")
	case laws > 1 || skips > 1:
		return opts, fmt.Errorf("--law and --skip-law each select one law: given twice, the first would be dropped")
	case selected && (!validLaw(opts.law) || !validLaw(opts.skipLaw) || opts.law+opts.skipLaw == ""):
		return opts, fmt.Errorf("--law and --skip-law take a law's name, as a report shows it (Design.Safe)")
	case len(positional) > 1 && command != "test" && command != "check", len(positional) == 0 && command != "test":
		return opts, fmt.Errorf("incorrect arguments for %s", command)
	}
	if len(positional) == 0 && opts.mutants {
		positional = []string{"."}
	}
	if len(positional) > 1 {
		if err := severalTargets(command, opts, positional); err != nil {
			return opts, err
		}
		opts.more = positional[1:]
	}
	var err error
	if platformed {
		platform, opts.level = cutLevel(platform)
	}
	if opts.platform, opts.phone, err = targetPlatforms(platform, platformed, os.Getenv("GOOS"), os.Getenv("GOARCH")); err != nil {
		return opts, err
	}
	if opts.phone != (gobind.Platform{}) && (command == "run" || command == "test") {
		// A phone program is a library its packager links; nothing here
		// can run it.
		return opts, fmt.Errorf("%s does not run a program for %s, a phone platform; ergo build writes its module", command, opts.phone)
	}
	if opts.phone != (gobind.Platform{}) && opts.level != "" {
		// The packager links a phone module for the phone's own levels.
		return opts, fmt.Errorf("--platform: %s is a phone platform and takes no CPU level; drop /%s", opts.phone, opts.level)
	}
	if err := checkLevel(opts.platform.GOARCH, opts.level); err != nil {
		return opts, err
	}
	opts.file = positional[0]
	opts.uncached = counted
	if opts.search.mode != "" {
		// --schedule alone replays: it needs the explored build too.
		opts.explore = true
	}
	if recursivePattern(opts.file) && (command == "check" || command == "test" || command == "lint") {
		return opts, nil
	}
	if isPackageFolder(opts.file) || isStdTarget(opts.file) && command == "test" {
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
	stdTest  testplan.NativeRoot // zero for a module's own test run
}

// load admits a source path once and checks it. The spelling is located
// once (receipt.LocateSource): diagnostics name the admitted path and
// //line directives name it too, and the bytes come from one non-blocking
// open of the located file, which must be a regular file. A folder is a
// package: the loader reads it and the local packages it imports
// (sketchfront.CheckDir), and each diagnostic names its file.
func load(path, target string) (loaded, error) {
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return loaded{}, err
	}
	return loadForPlatform(path, target, platform)
}

func loadForPlatform(path, target string, platform gobind.Platform) (loaded, error) {
	return loadThrough(nil, path, target, platform)
}

// loadThrough is loadForPlatform reading a package folder through fsys (a
// warm request's Recorder); nil reads the host.
func loadThrough(fsys sketchfront.FS, path, target string, platform gobind.Platform) (loaded, error) {
	if isPackageFolder(path) {
		return loadFolder(fsys, path, target, platform)
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
	stage.Add(stage.FilesRead, 1)
	stage.Add(stage.BytesRead, int64(len(text)))
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
func loadFolder(fsys sketchfront.FS, path, target string, platform gobind.Platform) (loaded, error) {
	source, err := receipt.LocateSource(path)
	if err != nil {
		return loaded{}, err
	}
	uri, err := report.FileURI(source.Admitted())
	if err != nil {
		return loaded{}, err
	}
	var analysis sketchfront.Analysis
	if fsys == nil {
		analysis, err = sketchfront.CheckDirForPlatform(source.Admitted(), platform)
	} else {
		analysis, err = sketchfront.CheckDirFS(fsys, source.Admitted(), platform)
	}
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
	return refuseUncheckedIO(l, opts, os.Stdout, os.Stderr)
}

func refuseUncheckedIO(l loaded, opts fileOptions, stdout, stderr io.Writer) error {
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	if opts.json || opts.command == "build" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(l.result.CheckEnvelope())
	} else {
		_ = l.result.Report(l.snapshot, false).WriteText(stderr, l.source.String())
	}
	return fmt.Errorf("check failed")
}

// checkCommand prints the check envelope; exit 1 when the source does not
// check. A source that checks has its pure obligations evaluated
// (sketchfront.Analysis.Evaluation) under the budget of --eval-steps,
// --eval-bytes and --seed, and the envelope carries the evaluation; the
// exit status is then evaluationExit's.
func checkCommand(args []string) error {
	if len(args) > 0 && args[0] == "--want" {
		return checkWant(os.Stdout, args[1:])
	}
	opts, err := parseFileOptions("check", args)
	if err != nil {
		return err
	}
	useFresh(opts)
	if len(opts.more) > 0 {
		return manyCheckCommand(opts)
	}
	if recursivePattern(opts.file) {
		return recursiveCheckCommand(opts)
	}
	// A daemon of this release serving the working directory answers a
	// warm check with no work here (ergo dev).
	if wd, err := os.Getwd(); err == nil && isPackageFolder(opts.file) {
		if stdout, ok := askDev(wd, opts.file, append([]string{"check"}, args...)); ok {
			_, err := os.Stdout.Write(stdout)
			return err
		}
	}
	return checkPackage(opts)
}

// recursiveCheckCommand is `ergo check DIR/...`: one shared load of the
// module when it can, else each package alone.
func recursiveCheckCommand(opts fileOptions) error {
	if shared, err := sharedCheckCommand(opts); shared {
		return err
	}
	return recursiveCommand(opts, nil, checkPackage)
}

// manyCheckCommand is `ergo check A B ...` (PKG G9): each target in the
// order given, a file or folder as `ergo check A` prints it, a DIR/...
// pattern as `ergo check DIR/...` does. A target that does not check does
// not stop the later ones; a defect, a signal or a usage error does. The
// run exits 1 when a target failed. No ergo dev daemon answers it.
func manyCheckCommand(opts fileOptions) error {
	failed := false
	for _, target := range append([]string{opts.file}, opts.more...) {
		one := opts
		one.file, one.more = target, nil
		var err error
		if recursivePattern(target) {
			err = recursiveCheckCommand(one)
		} else {
			err = checkPackage(one)
		}
		stop, fail := packageOutcome(target, err)
		if stop != nil {
			return stop
		}
		failed = failed || fail
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

func checkPackage(opts fileOptions) error {
	w := startWarm(opts, nil)
	if w != nil && w.hit {
		_, err := os.Stdout.Write(w.answer.Stdout)
		return err
	}
	l, err := loadThrough(w.fsys(), opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	return checkLoaded(opts, l, w)
}

// checkLoaded prints the check envelope of a loaded package, evaluated
// when it checks, and returns check's exit status.
func checkLoaded(opts fileOptions, l loaded, w *warm) error {
	out, exit, err := checkEnvelope(opts, l, w.fsys())
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return err
	}
	if exit == nil && l.result.Checked {
		w.record(sketchfront.WarmAnswer{Stdout: out})
	}
	return exit
}

// checkEnvelope is the check envelope check prints for a loaded package,
// one JSON line, evaluated when the package checks, and check's exit
// status (exit); err is a failure to produce it. test --emitted --json
// prints the same envelope (emittedjson.go).
func checkEnvelope(opts fileOptions, l loaded, fsys sketchfront.FS) (out []byte, exit, err error) {
	id := producer.Current()
	if err := l.result.Qualify(id); err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if !l.result.Checked {
		if err := json.NewEncoder(&buf).Encode(featuredEnvelope(l.result)); err != nil {
			return nil, nil, err
		}
		return buf.Bytes(), fmt.Errorf("check failed"), nil
	}
	return evaluatedEnvelope(opts, l, id, fsys)
}

// evaluatedEnvelope is the check envelope of a loaded package that
// checks, with its evaluation, and check's exit status; id is the
// producer identity the envelope was qualified with, and fsys the file
// system the ledger is read through (a warm request's Recorder; nil reads
// the host).
func evaluatedEnvelope(opts fileOptions, l loaded, id producer.Identity, fsys sketchfront.FS) (out []byte, exit, err error) {
	// Only a source that checks is evaluated (L7): in process, with no Go
	// build and no child.
	budget := evalBudget(opts)
	// A law run whose verdict the store holds is reported cached, and a
	// run that held is stored (lawcache), so ergo test does not run it
	// again. The store is keyed by the identity check acquired for its
	// envelope: check acquires none for the cache.
	var store *lawcache.Store
	if compiler, err := build.IdentityOf(id); err == nil {
		store = lawStoreOf(compiler)
	}
	budget.Verdicts = l.analysis.EvalVerdicts(store, budget.Seed, budget.States, pinnedToolchain(opts.platform))
	evaluation, err := l.analysis.Evaluation(budget, opts.file)
	boundLaws(store)
	if err != nil {
		return nil, nil, err
	}
	// A rewrite law in scope that its ledger does not license yet gets
	// advice (LF6c). The ledger is read through the warm recorder, so a
	// changed ergo.laws changes the warm key.
	if err := l.analysis.AdviseLicences(&evaluation, fsys, ledgerPath(opts.file)); err != nil {
		return nil, nil, err
	}
	l.result.SetEvaluation(evaluation)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(featuredEnvelope(l.result)); err != nil {
		return nil, nil, err
	}
	exit = evaluationExit(evaluation)
	if exit == nil && opts.strict {
		exit = undecidedExit(evaluation.Obligations)
	}
	return buf.Bytes(), exit, nil
}

// featuredEnvelope is what check prints: the result's envelope with this
// ergo's features (versionFeatures).
func featuredEnvelope(r *report.Result) report.CheckEnvelope {
	env := r.CheckEnvelope()
	env.Features = versionFeatures
	return env
}

// evalBudget is check's evaluation budget: the editor default
// (eval.DefaultBudget) with --seed, --eval-steps and --eval-bytes over it.
// parseFileOptions has validated each.
func evalBudget(opts fileOptions) eval.Budget {
	b := eval.DefaultBudget()
	if opts.seed != "" {
		b.Seed, _ = strconv.ParseUint(opts.seed, 10, 64)
	}
	if opts.evalSteps != "" {
		b.Steps, _ = strconv.ParseInt(opts.evalSteps, 10, 64)
	}
	if opts.evalBytes != "" {
		b.Bytes, _ = strconv.ParseInt(opts.evalBytes, 10, 64)
	}
	return b
}

// evaluationExit is check's exit status after a checked source's
// evaluation, as ergo test's runner gives it: 2 when a defect stopped the
// run, 1 when an obligation was refuted, else 0. A skipped or incomplete
// obligation is listed in the envelope with its reason and leaves the
// status 0; the envelope's status never reads "passed" for it.
func evaluationExit(e report.Evaluation) error {
	switch e.Status {
	case "defect":
		return exitStatusError{code: 2, message: "check: a defect stopped the evaluation"}
	case "refuted":
		return exitStatusError{code: 1, message: "check: an obligation was refuted"}
	}
	return nil
}

// undecidedExit is check --strict's exit after an evaluation that
// evaluationExit passed: 1 when an obligation was skipped, stopped or not
// run, naming each with its reason, since a law that was not decided is
// not a pass (MODELCHECK T3b: an ergo whose check skipped every law exited
// 0, and orrery read that as proved). Plain check keeps exit 0 for them:
// ergo test decides what check time skips.
func undecidedExit(obligations []report.Obligation) error {
	var undecided []string
	for _, o := range obligations {
		if o.Status != "passed" {
			undecided = append(undecided, fmt.Sprintf("%s (%s: %s)", o.Name, o.Status, o.Reason))
		} else if o.Machine != nil && o.Evidence != "proved" {
			// A machine law that passed on sampled runs only (its graph
			// could not be listed): no run broke it, which proves nothing
			// (batch-mc1 T3b O1).
			undecided = append(undecided, fmt.Sprintf("%s (%s, not proved: %s)", o.Name, o.Evidence, o.Reason))
		}
	}
	if len(undecided) == 0 {
		return nil
	}
	return exitStatusError{code: 1, message: "check --strict: not decided: " + strings.Join(undecided, "; ")}
}

// validCount reports whether s is a positive decimal int64 with no sign.
func validCount(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && s[0] != '+'
}

const inspectHelp = "usage: ergo inspect FILE|DIR [--target go]\n\n" +
	"Prints, as JSON, what the program reaches of Go (docs/tooling.md, inspect):\n" +
	"each Go import with its binding's provenance and the declarations the\n" +
	"binding refuses, with their reasons; for each function, the Go symbols its\n" +
	"code reaches and the uninterruptible host waits among them; for each Go\n" +
	"symbol, its Go declaration, Ergo signature, tier (pure or foreign),\n" +
	"provenance and wait (none, interruptible, uninterruptible); for each\n" +
	"rewrite site, its position, law, provider and constants, its ergo.laws entry's\n" +
	"verdict and state (current, stale or missing) and whether the build\n" +
	"applies it (a current, proved entry only). It runs no Go tool.\n\n" +
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
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	pkgs := l.analysis.Checked()
	if !l.result.Checked || pkgs == nil {
		return refuseUnchecked(l, opts)
	}
	report := sketchfront.InspectGo(pkgs)
	sites, err := sketchfront.InspectRewrites(l.analysis, l.source.Admitted(), ledgerPath(opts.file))
	if err != nil {
		return err
	}
	report.RewriteSites = sites
	return printJSON(report)
}

// diagnosticsCommand prints the diagnostic report as text or JSON; exit 1
// when the policy fails, 2 on an invalid invocation.
func diagnosticsCommand(args []string) error {
	presets, sel, rest, err := lintFlags(args)
	if err != nil {
		return invalidInvocation(err)
	}
	if len(presets) > 0 {
		return invalidInvocation(fmt.Errorf("--preset belongs to ergo lint"))
	}
	opts, err := parseFileOptions("diagnostics", rest)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	run, err := lintLoaded(l, nil, sel)
	if err != nil {
		return invalidInvocation(err)
	}
	if err := l.result.Qualify(producer.Current()); err != nil {
		return err
	}
	findings := l.result.ReportWithLint(l.snapshot, opts.strict, run.Advice())
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

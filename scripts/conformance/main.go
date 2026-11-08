// Command conformance verifies Ergo's reference-only Effect and Foldkit
// conformance data.
//
//	go run ./scripts/conformance check [--mapping FILE] [--run]
//	go run ./scripts/conformance foldkit [SOURCE] (--capture | --check | --self-check) [--root DIR]
//
// check validates the selected Effect behavior mapping and its evidence tests
// and, with --run, executes those tests and requires the subtest of each
// case they claim to run and pass. foldkit verifies the committed
// Foldkit snapshot offline (--self-check), verifies it against a read-only
// pinned Foldkit checkout (--check), or captures it from one into an empty
// root (--capture).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const usage = `usage: conformance <command> [flags]

commands:
  check [--mapping FILE] [--run]    validate conformance/effect-cases.json; --run executes its evidence tests
  foldkit [SOURCE] (--capture | --check | --self-check) [--root DIR]
                                    capture or verify the pinned Foldkit snapshot
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "foldkit":
		return runFoldkit(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "conformance: unknown command %q\n%s", args[0], usage)
	return 2
}

// repositoryRoot is the Go module root enclosing the working directory, the
// directory go run ./scripts/conformance resolves against.
func repositoryRoot() string {
	start, err := os.Getwd()
	if err != nil {
		return "."
	}
	for directory := start; ; {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return start
		}
		directory = parent
	}
}

// parseFlags parses a subcommand's flags; done reports an exit code when
// parsing ends the command (help or a usage error).
func parseFlags(flags *flag.FlagSet, args []string, stderr io.Writer) (code int, done bool) {
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unrecognized arguments: %s\n", flags.Name(), strings.Join(flags.Args(), " "))
		flags.Usage()
		return 2, true
	}
	return 0, false
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	root := repositoryRoot()
	flags := flag.NewFlagSet("conformance check", flag.ContinueOnError)
	mappingPath := flags.String("mapping", filepath.Join(root, mappingRelative), "selected behavior mapping")
	runEvidence := flags.Bool("run", false, "execute the selected evidence tests")
	if code, done := parseFlags(flags, args, stderr); done {
		return code
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "effect_conformance: %v\n", err)
		return 1
	}
	mapping, err := readMapping(*mappingPath)
	if err != nil {
		return fail(err)
	}
	tests, claims, err := validateMapping(mapping, root)
	if err != nil {
		return fail(err)
	}
	if *runEvidence {
		for _, argv := range evidenceCommands(tests) {
			command := exec.Command(argv[0], argv[1:]...)
			var events bytes.Buffer
			command.Dir, command.Stdout, command.Stderr = root, &events, stderr
			runErr := command.Run()
			var exit *exec.ExitError
			if runErr != nil && !errors.As(runErr, &exit) {
				return fail(fmt.Errorf("cannot execute Go evidence runner: %v", runErr))
			}
			claimErr := checkClaimsRan(&events, argv[3], claims, stdout)
			if exit != nil {
				return exit.ExitCode()
			}
			if claimErr != nil {
				return fail(claimErr)
			}
		}
	}
	cases := mapping["cases"].([]any)
	fmt.Fprintf(stdout, "effect mapping: %d selected behaviors at %s; %d Go evidence tests\n", len(cases), pinnedCommit[:12], len(tests))
	return 0
}

const foldkitUsage = "usage: conformance foldkit [SOURCE] (--capture | --check | --self-check) [--root DIR]\n"

// runFoldkit mirrors the original argparse interface: one optional SOURCE,
// anywhere among the flags, and exactly one mode.
func runFoldkit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("conformance foldkit", flag.ContinueOnError)
	captureMode := flags.Bool("capture", false, "capture the pinned source into this worktree")
	checkMode := flags.Bool("check", false, "verify capture against the pinned source checkout")
	selfCheckMode := flags.Bool("self-check", false, "verify the committed snapshot offline")
	rootFlag := flags.String("root", "", "Ergo repository root (default: the enclosing Go module)")
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, foldkitUsage)
		flags.PrintDefaults()
	}
	usageError := func(format string, values ...any) int {
		fmt.Fprintf(stderr, "%s: "+format+"\n", append([]any{flags.Name()}, values...)...)
		fmt.Fprint(stderr, foldkitUsage)
		return 2
	}
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		rest := flags.Args()
		if len(rest) == 0 {
			break
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if len(positional) > 1 {
		return usageError("unrecognized arguments: %s", strings.Join(positional[1:], " "))
	}
	modes := 0
	for _, mode := range []bool{*captureMode, *checkMode, *selfCheckMode} {
		if mode {
			modes++
		}
	}
	if modes == 0 {
		return usageError("one of the arguments --capture --check --self-check is required")
	}
	if modes > 1 {
		return usageError("only one of the arguments --capture --check --self-check may be given")
	}
	root := ""
	flags.Visit(func(set *flag.Flag) {
		if set.Name == "root" {
			root = purePath(*rootFlag)
		}
	})
	if root == "" {
		root = resolvePath(repositoryRoot())
	}
	source := foldkitDefaultSource
	if len(positional) == 1 {
		source = purePath(positional[0])
	}
	err := func() error {
		snapshot := pureJoin(root, foldkitSnapshotRelative)
		switch {
		case *captureMode:
			if err := captureFoldkit(source, root, pinnedFoldkit, writeFoldkitJSON, stdout); err != nil {
				return err
			}
			if foldkitExpectedFileCount != 0 && foldkitExpectedExampleInventorySHA256 != "" && foldkitExpectedRootSHA256 != "" {
				return selfCheckFoldkit(root, stdout)
			}
			return nil
		case *checkMode:
			return checkFoldkitSource(source, snapshot, stdout)
		}
		if len(positional) == 1 {
			return errors.New("--self-check does not accept a source checkout")
		}
		return selfCheckFoldkit(root, stdout)
	}()
	if err != nil {
		fmt.Fprintf(stderr, "Foldkit corpus import failed: %v\n", err)
		return 1
	}
	return 0
}

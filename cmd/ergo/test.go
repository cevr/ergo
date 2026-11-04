package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// testCommand runs a source's tests: `ergo test FILE [--seed N] [-work]
// [--target go]`. It is a transport over sketchfront's test build
// (build.ModeTest, lower.LowerTests): it checks the source, builds the test
// binary into a private temporary directory, never the working directory,
// dist/ or the module, and runs it as a child with --seed, through the
// same child path as run (runEntryProcess), so SIGINT gives 130 and
// SIGTERM 143. Its exit status is the runner's: 0 when every test passes,
// 1 when one fails, 2 when a defect stops the run. An invalid invocation, a
// malformed --seed included, is a usage error (exit 2) that runs nothing
// and prints no report line; a check, lowering or Go build failure prints
// its diagnostics and exits 1 with no PASS line. -work prints WORK=DIR on
// stderr, DIR being the test module in the emission cache.
func testCommand(args []string) error {
	opts, err := parseFileOptions("test", args)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		if err := l.result.Qualify(producer.Current()); err != nil {
			return err
		}
		_ = printJSON(l.result.CheckEnvelope())
		return fmt.Errorf("check failed")
	}
	dir, err := os.MkdirTemp("", "ergo-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := strings.TrimSuffix(filepath.Base(l.source.String()), filepath.Ext(l.source.String()))
	binary := filepath.Join(dir, name+".test")
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    l.source.Admitted(),
		Output:    binary,
		Cache:     build.Cache{Root: root},
		Toolchain: build.HostToolchain(),
		Stderr:    os.Stderr,
	})
	if opts.work && module.Dir != "" {
		fmt.Fprintf(os.Stderr, "WORK=%s\n", module.Dir)
	}
	if err != nil {
		return err
	}
	var childArgs []string
	if opts.seed != "" {
		childArgs = append(childArgs, "--seed", opts.seed)
	}
	child := exec.Command(binary, childArgs...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	return runEntryProcess(child)
}

// validSeed reports whether s is a seed the test binary reads: a decimal
// uint64 with no sign.
func validSeed(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

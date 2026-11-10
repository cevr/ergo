package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// testCommand runs a source's tests: `ergo test FILE [--seed N]
// [--explore] [-work] [--target go]`. --explore links the same test module
// with the ergoexplore tag, so its tasks run on one baton under the
// cooperative schedule, and a deadlock is a defect that names the tasks
// (runtime.md, Explored schedules). It is a transport over sketchfront's test build
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
		return refuseUnchecked(l, opts)
	}
	dir, err := os.MkdirTemp("", "ergo-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, l.name+".test")
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	toolchain := build.HostToolchain()
	if opts.explore {
		// The tag chooses the runtime's controlled scheduler at link
		// time; the emitted module is the one a plain `ergo test` uses
		// (runtime.md, Explored schedules).
		toolchain.Tags = []string{"ergoexplore"}
	}
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    l.source.Admitted(),
		Output:    binary,
		Cache:     build.Cache{Root: root},
		Toolchain: toolchain,
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
	// The test binary runs under a memory limit, 2 GiB unless
	// ERGO_PROGRAM_MEMORY sets another, and its stderr passes through a
	// watcher, so a test that grows without bound stops early and `ergo`
	// says which limit stopped it (docs/tooling.md, "Test memory").
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return err
	}
	child := exec.Command(binary, childArgs...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	return runEntryProcess(child, limit, proclimit.Watch(child))
}

// validSeed reports whether s is a seed the test binary reads: a decimal
// uint64 with no sign.
func validSeed(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

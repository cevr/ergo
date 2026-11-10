// Command sizes is the size receipt of the gate's full tier (binsize plan,
// BS0; scripts/gate step "sizes"). It builds each fixture as `ergo build`
// does, default and stripped, and a Go hello control with the same
// toolchain and flags, prints one JSON receipt per fixture on standard
// output, and checks the receipts against internal/sizes/budgets.json:
// each problem is a line on standard error, and any problem exits 1.
//
//	go run ./internal/sizes/cmd/sizes [-root DIR]
//
// It is a command, not a test, so a checker edit reaches no extra test
// package in the fast tier: the size of a build exists only after the Go
// linker runs, and the fast tier runs no Go build.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sizes"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// fixtures are the programs the budget holds, by name, with their folder
// relative to the module root.
var fixtures = []struct{ name, dir string }{
	{"hello", "internal/sizes/testdata/hello"},
	{"pure", "internal/sizes/testdata/pure"},
	{"unused", "internal/sizes/testdata/unused"},
	{"relay", "examples/relay/cmd/relay"},
	{"kvstore", "examples/kvstore/cmd/kvstore"},
	{"deploys", "examples/deploys/cmd/deploys"},
}

// controlDir is the Go hello control.
const controlDir = "internal/sizes/testdata/gohello"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sizes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "the module root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		fmt.Fprintf(stderr, "sizes: the budget is measured on linux/amd64 and the receipt reads ELF; skipped on %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return 0
	}
	receipts, err := measure(*root)
	if err != nil {
		fmt.Fprintln(stderr, "sizes:", err)
		return 2
	}
	for _, r := range receipts {
		line, err := json.Marshal(r)
		if err != nil {
			fmt.Fprintln(stderr, "sizes:", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", line)
	}
	budget, err := sizes.Checked()
	if err != nil {
		fmt.Fprintln(stderr, "sizes:", err)
		return 2
	}
	problems := budget.Check(receipts)
	for _, p := range problems {
		fmt.Fprintln(stderr, "sizes:", p)
	}
	if len(problems) > 0 {
		return 1
	}
	fmt.Fprintf(stderr, "sizes: %d fixtures within budget\n", len(receipts))
	return 0
}

// measure builds the control and every fixture in a private directory,
// removed at the end, and measures them.
func measure(rootSpelled string) ([]sizes.Receipt, error) {
	root, err := filepath.Abs(rootSpelled)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "ergo-sizes-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	control, err := measureGo(filepath.Join(root, filepath.FromSlash(controlDir)), work)
	if err != nil {
		return nil, err
	}
	cache := build.Cache{Root: filepath.Join(work, "cache")}
	var receipts []sizes.Receipt
	for _, f := range fixtures {
		b, err := measureErgo(filepath.Join(root, filepath.FromSlash(f.dir)), work, f.name, cache)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		receipts = append(receipts, sizes.Receipt{Fixture: f.name, Binary: b, Control: control})
	}
	return receipts, nil
}

// measureErgo builds the program in dir as `ergo build` does, then again
// stripped from the same generated module, and measures the pair.
func measureErgo(dir, work, name string, cache build.Cache) (sizes.Binary, error) {
	analysis, err := sketchfront.CheckDir(dir)
	if err != nil {
		return sizes.Binary{}, err
	}
	if len(analysis.Diagnostics) > 0 {
		return sizes.Binary{}, fmt.Errorf("%s does not check: %+v", dir, analysis.Diagnostics)
	}
	paths := [2]string{filepath.Join(work, name), filepath.Join(work, name+".stripped")}
	var module build.Module
	for i, goflags := range []string{"", "-ldflags=-s"} {
		var stderr bytes.Buffer
		module, err = sketchfront.Build(analysis, sketchfront.BuildRequest{
			Mode:      build.ModeBuild,
			Source:    dir,
			Output:    paths[i],
			Cache:     cache,
			Toolchain: build.Toolchain{Go: "go", GOFLAGS: goflags},
			Stderr:    &stderr,
		})
		if err != nil {
			return sizes.Binary{}, fmt.Errorf("build %s: %w\n%s", dir, err, stderr.String())
		}
	}
	b, err := sizes.MeasureBinary(paths[0], paths[1])
	if err != nil {
		return sizes.Binary{}, err
	}
	b.GoPackages, err = goPackages(module.Dir)
	return b, err
}

// measureGo builds the Go module in dir with the flags of every native
// build, default and stripped, and measures the pair.
func measureGo(dir, work string) (sizes.Binary, error) {
	paths := [2]string{filepath.Join(work, "gohello"), filepath.Join(work, "gohello.stripped")}
	for i, extra := range [][]string{nil, {"-ldflags=-s"}} {
		cmd := receipt.GoBuildCommand(dir, paths[i], ".", extra...)
		cmd.Env = append(cmd.Env, "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			return sizes.Binary{}, fmt.Errorf("build %s: %w\n%s", dir, err, out)
		}
	}
	b, err := sizes.MeasureBinary(paths[0], paths[1])
	if err != nil {
		return sizes.Binary{}, err
	}
	b.GoPackages, err = goPackages(dir)
	return b, err
}

// goPackages counts the packages the main package in dir links.
func goPackages(dir string) (int, error) {
	list := exec.Command("go", "list", "-mod=readonly", "-deps", ".")
	list.Dir = dir
	list.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	out, err := list.Output()
	if err != nil {
		return 0, fmt.Errorf("go list -deps in %s: %w", dir, err)
	}
	return len(strings.Fields(string(out))), nil
}

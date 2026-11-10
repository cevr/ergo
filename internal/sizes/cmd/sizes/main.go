// Command sizes is the size receipt of the gate's full tier (binsize plan,
// BS0; scripts/gate step "sizes"). It builds each fixture as `ergo build
// --debug` and `ergo build` (stripped, the default) do, and a Go hello
// control with the same toolchain and flags, prints one JSON receipt per fixture on standard
// output, and checks the receipts against internal/sizes/budgets.json:
// each problem is a line on standard error, and any problem exits 1.
// It also checks that a defect report is the same in both builds.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/proclimit"
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

// defectDir is the program whose defect report must not change when the
// build strips its symbols; defectMemory bounds each of its runs.
const (
	defectDir    = "internal/sizes/testdata/defect"
	defectMemory = 256 << 20
)

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
	if err := sameDefectReport(root, work, cache); err != nil {
		return nil, err
	}
	if err := reproducible(root, work); err != nil {
		return nil, err
	}
	return receipts, nil
}

// measureErgo builds the program in dir as `ergo build --debug` does, then
// as `ergo build` does (stripped) from the same generated module, and
// measures the pair: the symbols come from the first, the stripped size
// is the second's.
func measureErgo(dir, work, name string, cache build.Cache) (sizes.Binary, error) {
	paths, module, err := buildBoth(dir, work, name, cache)
	if err != nil {
		return sizes.Binary{}, err
	}
	b, err := sizes.MeasureBinary(paths[0], paths[1])
	if err != nil {
		return sizes.Binary{}, err
	}
	b.GoPackages, err = goPackages(module.Dir)
	return b, err
}

// buildBoth builds the program in dir to work/name with the symbols
// (`--debug`) and to work/name.stripped as `ergo build` does by default.
func buildBoth(dir, work, name string, cache build.Cache) ([2]string, build.Module, error) {
	paths := [2]string{filepath.Join(work, name), filepath.Join(work, name+".stripped")}
	analysis, err := sketchfront.CheckDir(dir)
	if err != nil {
		return paths, build.Module{}, err
	}
	if len(analysis.Diagnostics) > 0 {
		return paths, build.Module{}, fmt.Errorf("%s does not check: %+v", dir, analysis.Diagnostics)
	}
	var module build.Module
	for i, strip := range []bool{false, true} {
		var stderr bytes.Buffer
		module, err = sketchfront.Build(analysis, sketchfront.BuildRequest{
			Mode:      build.ModeBuild,
			Source:    dir,
			Output:    paths[i],
			Cache:     cache,
			Toolchain: build.Toolchain{Go: "go", Strip: strip},
			Stderr:    &stderr,
		})
		if err != nil {
			return paths, module, fmt.Errorf("build %s: %w\n%s", dir, err, stderr.String())
		}
	}
	return paths, module, nil
}

// reproducible checks the binaries measure built (binsize BS6; plan 3 (f)):
// no fixture's file, stripped or not, holds the checkout's path, and
// unused, hello's main.ergo plus a second file of declarations that main
// never reaches, builds byte for byte hello's binaries. The byte equality
// covers what a symbol search cannot: an initializer, a table entry and a
// path of the unreachable declarations.
func reproducible(root, work string) error {
	data := map[string][]byte{}
	for _, f := range fixtures {
		for _, name := range []string{f.name, f.name + ".stripped"} {
			b, err := os.ReadFile(filepath.Join(work, name))
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte(root)) {
				return fmt.Errorf("%s holds the checkout's path %s", name, root)
			}
			data[name] = b
		}
	}
	for _, name := range []string{"unused", "unused.stripped"} {
		hello := strings.Replace(name, "unused", "hello", 1)
		if !bytes.Equal(data[name], data[hello]) {
			return fmt.Errorf("%s (%d bytes) differs from %s (%d bytes): a declaration main never reaches reached the binary", name, len(data[name]), hello, len(data[hello]))
		}
	}
	return nil
}

// sameDefectReport builds the defect fixture with and without the symbol
// table and runs both: the report (output, exit status) must be byte for
// byte the same, since stripping keeps the pclntab the report reads (binsize
// BS4). The report names its source relative to the fixture's folder
// (binsize BS6). It builds Go because the stripped file exists only after
// the linker runs; each run is bounded in memory by proclimit.
func sameDefectReport(root, work string, cache build.Cache) error {
	paths, _, err := buildBoth(filepath.Join(root, filepath.FromSlash(defectDir)), work, "defect", cache)
	if err != nil {
		return err
	}
	var runs [2]string
	for i, path := range paths {
		var out bytes.Buffer
		cmd := exec.Command(path)
		cmd.Stdout, cmd.Stderr = &out, &out
		err := proclimit.Run(cmd, defectMemory)
		var exit *exec.ExitError
		var limited *proclimit.LimitError
		if err != nil && (!errors.As(err, &exit) || errors.As(err, &limited)) {
			return fmt.Errorf("run %s: %w", path, err)
		}
		runs[i] = fmt.Sprintf("exit %d\n%s", cmd.ProcessState.ExitCode(), out.String())
	}
	if runs[0] != runs[1] || !strings.Contains(runs[0], "\tpick at main.ergo:2\n") {
		return fmt.Errorf("the defect report differs between the --debug and the default build:\n--debug:\n%s\ndefault:\n%s", runs[0], runs[1])
	}
	return nil
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

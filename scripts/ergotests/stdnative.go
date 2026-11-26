package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/testplan"
)

// nativeLine is a test binary's verdict line for a law: `ok law L …`,
// `skip law L…` or `FAIL law L …`, L a law or a member law T.L
// (lower/rewritecheck.go reads the same lines).
var nativeLine = regexp.MustCompile(`^(ok|skip|FAIL) law ([A-Za-z_][A-Za-z0-9_.]*)`)

// nativeFail is the class of a law whose native run failed.
const nativeFail = "native fail"

// nativePackages selects where to rerun undecided laws. A member law
// runs where a provider binds it, which need not be its own package.
// A law with no known binding falls back to its declaration package.
func nativePackages(nativeOnly []testplan.NativeRoot, bare []string, run *stdRun) []testplan.NativeRoot {
	// Native-only roots have no in-place leg, even when every ledger
	// entry has a Go runner.
	pkgs := slices.Clone(nativeOnly)
	for _, key := range bare {
		if ranInPlace(run.verdicts[key]) || run.failed[key] != "" {
			continue
		}
		bindings := run.bindings[key]
		if len(bindings) == 0 {
			pkg, _, _ := strings.Cut(key, " ")
			bindings = []string{pkg}
		}
		for _, pkg := range bindings {
			if run.failed[pkg+" *"] == "" && !slices.ContainsFunc(pkgs, func(root testplan.NativeRoot) bool { return root.Path == pkg }) {
				pkgs = append(pkgs, testplan.NativeRoot{Path: pkg})
			}
		}
	}
	return pkgs
}

// runStdNative runs each selected package on its own source with
// `ergo test std:PKG` and credits its verdicts to the declaring laws.
// ergo is the ergo binary; "" builds ./cmd/ergo.
func runStdNative(w io.Writer, root, ergo string, nativeOnly []testplan.NativeRoot, bare []string, run *stdRun) int {
	tmp, err := os.MkdirTemp("", "ergotests-std-")
	if err != nil {
		fmt.Fprintln(w, "FAIL", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	if ergo == "" {
		ergo = filepath.Join(tmp, "ergo")
		build := ergobuild.Command(ergo, "./cmd/ergo")
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(w, "FAIL go build ./cmd/ergo: %v\n%s", err, out)
			return 1
		}
	}
	cache := filepath.Join(tmp, "cache")
	for _, target := range nativePackages(nativeOnly, bare, run) {
		cmd := exec.Command(ergo, "test", "std:"+target.Path)
		cmd.Dir = root
		cmd.Env = os.Environ()
		if os.Getenv("ERGO_CACHE") == "" {
			cmd.Env = append(cmd.Env, "ERGO_CACHE="+cache)
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		if nativeResult(w, target, err, out.Bytes(), run) != 0 {
			return 1
		}
	}
	return 0
}

// nativeResult accounts for the verdicts and the exit of one planned root.
func nativeResult(w io.Writer, root testplan.NativeRoot, err error, out []byte, run *stdRun) int {
	seen := nativeVerdicts(root, out, run)
	pkg := root.Path
	switch {
	case err != nil && root.Required:
		fmt.Fprintf(w, "FAIL ergo test std:%s: %v\n%s", pkg, err, out)
		return 1
	case err != nil && seen == 0:
		// Every law it was to decide is unrun; the output says why.
		fmt.Fprintf(w, "red %s: ergo test std:%s: %v\n%s", pkg, pkg, err, out)
	case err != nil:
		fmt.Fprintf(w, "native %s: ergo test std:%s: %v\n", pkg, pkg, err)
	default:
		fmt.Fprintf(w, "native %s: %d law verdicts\n", pkg, seen)
	}
	return 0
}

// nativeVerdicts records the law verdict lines of pkg's native run in run,
// a FAIL as a failure of class nativeFail, and returns how many it read.
func nativeVerdicts(root testplan.NativeRoot, out []byte, run *stdRun) int {
	pkg := root.Path
	n := 0
	scan := bufio.NewScanner(bytes.NewReader(out))
	scan.Buffer(nil, 1<<20)
	for scan.Scan() {
		line := scan.Text()
		if root.Required {
			if law, ok := stdlib.NativeMissingLaw(line); ok {
				run.nativeMissing[pkg+" "+law] = line
			}
		}
		m := nativeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n++
		key := run.key(pkg, m[2])
		passed := m[1] == "ok"
		run.native[key] = append(run.native[key], passed)
		if m[1] == "FAIL" && run.failed[key] == "" {
			run.failed[key] = nativeFail
		}
	}
	return n
}

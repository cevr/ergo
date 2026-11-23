package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expectedSkipsFile names, beside a module's ergo.mod, the skip lines its
// ergo test may print (runtime.md, Tests 4): one line each, exactly as
// printed; a line starting "#" gives the reason. Any other skip fails the
// run, so a law ergo test did not run is never a pass by default.
const expectedSkipsFile = "expected-skips"

// skipsPath is the expected-skips file of the module of path (a file, a
// package folder or DIR/...): beside the ergo.laws ledgerPath finds, which
// is the module root's, or the folder's when there is no ergo.mod.
func skipsPath(path string) string {
	ledger := ledgerPath(strings.TrimSuffix(path, "/..."))
	if ledger == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(ledger), expectedSkipsFile)
}

// unexpectedSkips are the lines of a test run's stdout that report a skip
// ("skip …") and that the file at skips does not list. A skip asked for
// with --skip-law is the caller's and always expected.
func unexpectedSkips(stdout []byte, skips string) []string {
	expected := map[string]bool{}
	if data, err := os.ReadFile(skips); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				expected[line] = true
			}
		}
	}
	var out []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "skip ") && !strings.HasSuffix(line, ": --skip-law") && !expected[line] {
			out = append(out, line)
		}
	}
	return out
}

// skipsExit is the exit of a test run that passed: 1, naming each, when it
// printed a skip the module's expected-skips does not list.
func skipsExit(stdout []byte, path string) error {
	skips := unexpectedSkips(stdout, skipsPath(path))
	if len(skips) == 0 {
		return nil
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "FAIL: %d unexpected skips; run each law, or list its line in %s with the reason (runtime.md, Tests 4)", len(skips), skipsPath(path))
	for _, s := range skips {
		fmt.Fprintf(&b, "\n    %s", s)
	}
	return exitStatusError{code: 1, message: b.String()}
}

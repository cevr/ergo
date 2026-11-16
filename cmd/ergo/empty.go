package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// nothingToTest reports whether a package's test build would run nothing:
// it has no test and no law run.
func nothingToTest(pkg *check.Package) bool {
	return len(pkg.Tests) == 0 && len(pkg.LawRuns) == 0
}

// emptyTestRun is what the test binary of a package with nothing to test
// does with opts (lower's testRunner and $Seed), without building or
// running it, as go test reports "[no test files]": so the package's
// initializers, such as a main package's lets, never run. Like the binary
// it refuses a --run name, since the package has none (exit 2); writes the
// receipt, with no law run; fails a --law name (exit 1); and otherwise
// passes with no test. --skip-law names nothing to skip. revision is the
// source's revision, which the binary's receipt names.
// TestEmptyRunMatchesTheRunner holds it to the binary's own run.
func emptyTestRun(opts fileOptions, revision string, stdout, stderr io.Writer) error {
	if opts.run != "" {
		fmt.Fprintln(stderr, "usage: --run NAME: no test or law is named "+strconv.Quote(opts.run)+"; the names: ")
		return exitStatusError{code: 2, handled: true}
	}
	if opts.receipt != "" {
		seed := opts.seed
		if seed == "" {
			seed = "1"
		}
		n, _ := strconv.ParseUint(seed, 10, 64)
		text := "{\n  \"schema\": 1,\n  \"source\": " + receiptJSON(opts.file) + ",\n  \"revision\": " + receiptJSON(revision) +
			",\n  \"seed\": " + strconv.FormatUint(n, 10) + ",\n  \"target\": \"go\",\n  \"laws\": []\n}\n"
		if err := os.WriteFile(opts.receipt, []byte(text), 0o644); err != nil {
			fmt.Fprintln(stderr, err.Error())
			return exitStatusError{code: 1, handled: true}
		}
	}
	if opts.law != "" {
		fmt.Fprintln(stdout, "FAIL no law "+opts.law+" in this package (--law)")
		fmt.Fprintln(stderr, "error: no law "+opts.law)
		return exitStatusError{code: 1, handled: true}
	}
	fmt.Fprintln(stdout, "PASS (0 tests)")
	return nil
}

// receiptJSON is s as the test binary's receipt writes a string ($JSON):
// quotes, backslashes and control bytes escaped, and a byte that is not
// UTF-8 written as U+FFFD.
func receiptJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c < 0x20:
			b.WriteString("\\u00")
			b.WriteByte("0123456789abcdef"[c>>4])
			b.WriteByte("0123456789abcdef"[c&15])
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

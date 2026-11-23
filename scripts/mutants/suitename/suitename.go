// Package suitename translates legacy killer spellings at the ledger boundary.
// Discovery and coverage use checkout-relative directory names instead.
package suitename

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const (
	Check = "internal/sketch/check"
	Lower = "internal/sketch/lower"
	Std   = "internal/sketch/stdlib"
	API   = "internal/sketch/api"
	Ergo  = "cmd/ergo"
)

var aliases = map[string]string{
	"check": Check,
	"lower": Lower,
	"std":   Std,
	"api":   API,
	"ergo":  Ergo,
}

// Resolver is scoped to one checkout. Only a legacy spelling needs to check
// for a competing directory; cache that answer across the ledger rows.
type Resolver struct {
	hasTests func(string) bool
	testDirs map[string]bool
}

func At(root string) *Resolver {
	return WithTestDirs(func(dir string) bool {
		goTests, _ := filepath.Glob(filepath.Join(root, dir, "*_test.go"))
		ergoFiles, _ := filepath.Glob(filepath.Join(root, dir, "*.ergo"))
		return len(goTests) != 0 || len(ergoFiles) != 0
	})
}

// WithTestDirs resolves names for a tree that may not be checked out.
func WithTestDirs(hasTests func(string) bool) *Resolver {
	return &Resolver{hasTests: hasTests, testDirs: map[string]bool{}}
}

// Resolve returns the canonical spelling to store in a ledger, preserving the
// test name and its note. An explicit ./DIR bypasses the legacy alias table.
func (r *Resolver) Resolve(id string) (string, error) {
	name, test, ok := strings.Cut(id, ":")
	if !ok {
		return "", fmt.Errorf("test %q is not SUITE:NAME", id)
	}
	base, _, _ := strings.Cut(strings.TrimPrefix(name, "./"), "[")
	if base == "." || path.Clean(base) != base || base == ".." || strings.HasPrefix(base, "../") ||
		path.IsAbs(base) || strings.Contains(base, `\`) {
		return "", fmt.Errorf("test %q needs a checkout-relative suite directory", id)
	}
	dir, aliased := aliases[name]
	if !aliased {
		return id, nil
	}
	hasTests, seen := r.testDirs[name]
	if !seen {
		hasTests = r.hasTests(name)
		r.testDirs[name] = hasTests
	}
	if hasTests {
		return "", fmt.Errorf("ambiguous test %q: use %q or %q", id, "./"+name+":"+test, dir+":"+test)
	}
	return dir + ":" + test, nil
}

// Split returns the suite's identity and the test name without its note.
// The directory qualifier distinguishes spellings on disk, not suite IDs.
func Split(id string) (suite, test string) {
	suite, test, _ = strings.Cut(id, ":")
	test, _, _ = strings.Cut(test, " ")
	return strings.TrimPrefix(suite, "./"), test
}

// ForLedger spells an already canonical ID so it cannot be read as an
// alias. It is used for results, not for unresolved ledger input.
func ForLedger(id string) string {
	name, _, _ := strings.Cut(id, ":")
	if _, aliased := aliases[name]; aliased {
		return "./" + id
	}
	return id
}

// LegacyTarget returns the old spelling's directory without consulting
// the checkout. A saved coverage file can predate canonical identities.
func LegacyTarget(name string) string { return aliases[name] }

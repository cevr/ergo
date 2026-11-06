package check_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// wrapImporter refuses every non-std path with the error its wrap function
// makes from an *ImportError.
type wrapImporter struct{ wrap func(error) error }

func (i wrapImporter) Import(path string) (*check.Package, error) {
	if _, err := check.Std.Import(path); err == nil {
		return check.Std.Import(path)
	}
	return nil, i.wrap(&check.ImportError{Path: path, Reason: "the folder lib is gone"})
}

// cycleErr unwraps to itself: a custom Unwrap with a cycle.
type cycleErr struct{}

func (e *cycleErr) Error() string { return "cycle" }
func (e *cycleErr) Unwrap() error { return e }

// TestImportErrorWrapped: an importer may wrap its *ImportError (once,
// twice, inside errors.Join, inside a join inside a wrap), and the S104
// diagnostic still carries the reason. An error with no *ImportError, or
// with an Unwrap cycle, gives the plain diagnostic and ends.
func TestImportErrorWrapped(t *testing.T) {
	const reason = `unknown package "lib": the folder lib is gone`
	other := errors.New("disk on fire")
	for _, c := range []struct {
		name string
		wrap func(error) error
		want string
	}{
		{"plain", func(e error) error { return e }, reason},
		{"once", func(e error) error { return fmt.Errorf("load: %w", e) }, reason},
		{"twice", func(e error) error { return fmt.Errorf("a: %w", fmt.Errorf("b: %w", e)) }, reason},
		{"join", func(e error) error { return errors.Join(other, e) }, reason},
		{"join in wrap", func(e error) error { return fmt.Errorf("a: %w", errors.Join(other, fmt.Errorf("b: %w", e))) }, reason},
		{"multi %w", func(e error) error { return fmt.Errorf("%w; %w", other, e) }, reason},
		{"none", func(error) error { return fmt.Errorf("load: %w", other) }, `unknown package "lib"`},
		{"cycle", func(error) error { return &cycleErr{} }, `unknown package "lib"`},
		{"cycle joined", func(e error) error { return errors.Join(&cycleErr{}, e) }, reason},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := syntax.Parse("import lib\n\nfunc main() {}\n")
			if err != nil {
				t.Fatal(err)
			}
			_, ds := check.Check("main", []*syntax.File{f}, wrapImporter{c.wrap})
			var got []string
			for _, d := range ds {
				if d.Code == "S104" {
					got = append(got, d.Message)
				}
			}
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("S104 = %q, want [%q]\nall: %v", got, c.want, ds)
			}
			if c.want != reason && strings.Contains(got[0], ":") {
				t.Fatalf("S104 %q has a reason, want none", got[0])
			}
		})
	}
}

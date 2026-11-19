package gobind_test

import (
	"go/importer"
	"go/token"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// TestPureTierActsOnlyValidated is LF2d's admission of a pure row
// (interop.md, Foreign facts 7): ergo bind go writes strings.ToUpper in
// the pure tier, with no go.Foreign requirement, exactly when the std ledger holds the
// guard line of strings' pure row current and validated; with the line
// missing it binds the function under needs go.Foreign.
//
// Mutant it alone catches: ffi-pure-unguarded-acts (a pure row acts with
// no guard line).
func TestPureTierActsOnlyValidated(t *testing.T) {
	pkg, err := importer.ForCompiler(token.NewFileSet(), "source", nil).Import("strings")
	if err != nil {
		t.Fatal(err)
	}
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	key := gofacts.PureKey("strings")
	var dropped []string
	for _, l := range strings.Split(stdlib.Ledger(), "\n") {
		if !strings.HasPrefix(l, "fact "+key+" ") {
			dropped = append(dropped, l)
		}
	}
	for _, c := range []struct {
		name, ledger string
		foreign      bool
	}{
		{"validated", stdlib.Ledger(), false},
		{"missing", strings.Join(dropped, "\n"), true},
	} {
		ledger := c.ledger
		check.FactLedger = func() string { return ledger }
		var upper string
		for _, d := range gobind.Decls(pkg, nil) {
			if d.Name == "ToUpper" {
				upper = d.Ergo
			}
		}
		if got := strings.Contains(upper, "needs go.Foreign"); got != c.foreign {
			t.Errorf("%s: strings.ToUpper binds as %q; needs go.Foreign: %v, want %v", c.name, upper, got, c.foreign)
		}
	}
}

// TestEffectTierActsOnlyValidated is GBX6's binding of a memory-only row:
// ergo bind go writes atomic.Int64.Add with no needs row and an
// `// ergo:effect none` line exactly when the std ledger holds the row's
// guard line current and validated; with the line missing it binds the
// method with the go.Foreign row and no effect line.
//
// Mutant it alone catches: ffi-effect-bind-unguarded (the binder writes an
// effect row with no guard line).
func TestEffectTierActsOnlyValidated(t *testing.T) {
	pkg, err := importer.ForCompiler(token.NewFileSet(), "source", nil).Import("sync/atomic")
	if err != nil {
		t.Fatal(err)
	}
	saved := check.FactLedger
	t.Cleanup(func() { check.FactLedger = saved })
	key := gofacts.EffectKey("sync/atomic.Int64.Add")
	var dropped []string
	for _, l := range strings.Split(stdlib.Ledger(), "\n") {
		if !strings.HasPrefix(l, "fact "+key+" ") {
			dropped = append(dropped, l)
		}
	}
	for _, c := range []struct {
		name, ledger string
		memory       bool
	}{
		{"validated", stdlib.Ledger(), true},
		{"missing", strings.Join(dropped, "\n"), false},
	} {
		ledger := c.ledger
		check.FactLedger = func() string { return ledger }
		var add gobind.Decl
		for _, d := range gobind.Decls(pkg, nil) {
			if d.Name == "Int64.Add" {
				add = d
			}
		}
		if foreign := strings.Contains(add.Ergo, "needs go.Foreign"); foreign == c.memory || (add.Effect != "") != c.memory {
			t.Errorf("%s: atomic.Int64.Add binds as %q with effect %q; want memory-only %v", c.name, add.Ergo, add.Effect, c.memory)
		}
	}
}

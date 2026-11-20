package selfhostcheck

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// preludeGroup is exprdump's arguments for the prelude as an imported
// group, "-- EXT FOREIGN FILE...": the Go checker's tables of the prelude
// package, written under tmp, and the std root's .ergo files.
func preludeGroup(t *testing.T, tmp string) []string {
	t.Helper()
	prelude := typesPackage(stdDir())
	if prelude == nil {
		t.Fatal("the Go checker gives no prelude package")
	}
	_, ext := goKinds(prelude)
	_, foreign := goTypes(prelude)
	pe, pf := filepath.Join(tmp, "prelude-ext"), filepath.Join(tmp, "prelude-foreign")
	writeLines(t, pe, ext)
	writeLines(t, pf, foreign)
	out := []string{"--", pe, pf}
	files, err := filepath.Glob(filepath.Join(stdDir(), "*.ergo"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no prelude files in %s: %v", stdDir(), err)
	}
	return append(out, files...)
}

// TestCompilerExprBareOperationIsUndeclared is the S104 of a bare name of
// the language's operations (PRELUDE-ONE-SOURCE): Append, reached only as
// xs.Append(x), is no name in scope, so Append(xs, 1) is S104 at Append in
// both checkers, and xs.Append(1) is clean. exprdump runs with the prelude
// (its prelude.ergo and builtin.ergo, the Go checker's tables) as the
// imported group. The row is the diagnostic's position, which no property
// of accepted programs reaches (mutant receiver-op-in-scope).
//
// It builds Go because exprdump is the Ergo program under test.
func TestCompilerExprBareOperationIsUndeclared(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := exprDump(t, ctx)
	tmp := t.TempDir()
	group := preludeGroup(t, tmp)
	good := "func f(xs []int) []int { xs.Append(1) }\n"
	pkg, ds := check.CheckSource(good)
	if pkg == nil || len(ds) > 0 {
		t.Fatalf("the Go checker refuses the good row: %v", ds)
	}
	_, ext := goKinds(pkg)
	_, foreign := goTypes(pkg)
	e, f, file := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign"), filepath.Join(tmp, "p.ergo")
	writeLines(t, e, ext)
	writeLines(t, f, foreign)
	run := func(src string) []string {
		writeLines(t, file, []string{src})
		_, diags, _ := splitDiags(runDump(t, ctx, bin, append([]string{e, f, file}, group...)...))
		return diags
	}
	if d := run(good); len(d) > 0 {
		t.Errorf("the Go checker accepts %q, exprdump reports %v", good, d)
	}
	bad := "func f(xs []int) []int { Append(xs, 1) }\n"
	_, bds := check.CheckSource(bad)
	if len(bds) != 1 || bds[0].Code != "S104" {
		t.Fatalf("the Go checker gives %v for %q, want one S104", bds, bad)
	}
	// The Go checker's position: the name Append.
	want := "p.ergo " + strconv.Itoa(bds[0].Offset) + " " + strconv.Itoa(bds[0].Offset+bds[0].Length)
	if bad[bds[0].Offset:bds[0].Offset+bds[0].Length] != "Append" {
		t.Fatalf("the Go checker's S104 is at %q, want Append", bad[bds[0].Offset:bds[0].Offset+bds[0].Length])
	}
	if d := run(bad); len(d) != 1 || d[0] != want {
		t.Errorf("exprdump reports %v for %q, want [%s]: a receiver operation is no name in scope (%v)", d, bad, want, bds[0])
	}
}

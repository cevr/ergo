package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestPackageChecksAlikeAsRoot is the seeded property for the builder
// rule's test-package role (check/builders.go, rule 2; FU-audit, found by
// TEST-one round 2): the loader gives a package its role, so a test
// package checks the same whether it is the root (`ergo check celltest`,
// checked as main) or an import of another root (checked under its import
// path). One check of it is then enough for both.
//
// Each seed writes a module with an operation type cell.Cell, a test
// package celltest that defines providers of it (some complete, some not,
// zero to two New<X> builders) and a test over cell.Cell, and a package
// user that imports celltest. The diagnostics in celltest's files must be
// the same from both loads. A failing seed prints celltest's source and
// both reports; replay it with -run 'TestTestPackageChecksAlikeAsRoot/seed=N'.
//
// Mutant it alone catches: the role read from the path the checker is
// given (q.Path == tn.Pkg.Path+"test"), so a root checked as main has no
// test builders and reports S108 where its import load passes.
func TestTestPackageChecksAlikeAsRoot(t *testing.T) {
	builders := map[int]bool{}
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			files, n := testRoleModule(seed)
			builders[n] = true
			dir := writeTree(t, files)
			alone, err := CheckDir(filepath.Join(dir, "celltest"))
			if err != nil {
				t.Fatal(err)
			}
			imported, err := CheckDir(filepath.Join(dir, "user"))
			if err != nil {
				t.Fatal(err)
			}
			// A diagnostic names the root's functions as main's and an
			// import's by its path: that label is the one difference a
			// shared load sets for the root it lowers (TEST-one r2, R2-2.1).
			a := celltestLines(alone)
			b := strings.ReplaceAll(celltestLines(imported), "gen/celltest.", "main.")
			if a != b {
				t.Fatalf("celltest checks differently as the root and as an import\n--- celltest/mem.ergo\n%s\n--- as root\n%s--- as an import\n%s", files["celltest/mem.ergo"], a, b)
			}
			// The model: one builder generates cell.Cell; none or two do not.
			if (n == 1) != (a == "") {
				t.Fatalf("celltest has %d complete builders (2 is two or more); report:\n%s--- celltest/mem.ergo\n%s", n, a, files["celltest/mem.ergo"])
			}
		})
	}
	// The seeds reach a test package with no builder, one, and two.
	for n := 0; n <= 2; n++ {
		if !builders[n] {
			t.Errorf("no seed writes %d complete builders", n)
		}
	}
}

// celltestLines is the diagnostics of a's report in celltest's files.
func celltestLines(a Analysis) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(diagnosticLines(a), "\n") {
		if strings.HasPrefix(line, "celltest/") {
			b.WriteString(line)
		}
	}
	return b.String()
}

// testRoleModule writes the property's module for seed and returns it with
// the number of New<X> builders whose X provides every operation of
// cell.Cell.
func testRoleModule(seed uint64) (map[string]string, int) {
	r := rand.New(rand.NewPCG(seed, 0x7e57))
	ops := 1 + r.IntN(3)
	var cell strings.Builder
	cell.WriteString("type Cell {\n")
	for i := range ops {
		fmt.Fprintf(&cell, "\tGet%d() int,\n", i)
	}
	cell.WriteString("}\n")
	var mem strings.Builder
	mem.WriteString("import gen/cell\n")
	complete := 0
	for k := range r.IntN(3) + 1 {
		all := true
		var fns []string
		for i := range ops {
			if r.IntN(4) == 0 {
				all = false
				continue
			}
			fns = append(fns, fmt.Sprintf("\nfunc Get%d(m Mem%d) int { m.v }\n", i, k))
		}
		// A type with every operation declares Cell (EXC1); one without
		// all of them is no provider and declares nothing.
		clause := ""
		if all {
			clause = " implements cell.Cell"
		}
		fmt.Fprintf(&mem, "\ntype Mem%d { v int }%s\n", k, clause)
		for _, f := range fns {
			mem.WriteString(f)
		}
		if r.IntN(3) > 0 {
			fmt.Fprintf(&mem, "\nfunc NewMem%d() Mem%d { Mem%d{v: %d} }\n", k, k, k, k)
			if all {
				complete++
			}
		}
	}
	mem.WriteString("\ntest Reads(c cell.Cell) { assert c.Get0() == c.Get0() }\n")
	return map[string]string{
		"ergo.mod":          "module gen\n",
		"cell/cell.ergo":    cell.String(),
		"celltest/mem.ergo": mem.String(),
		"user/user.ergo":    "import gen/celltest\n\ntest S { assert true }\n",
	}, min(complete, 2)
}

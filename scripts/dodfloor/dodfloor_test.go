package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticCheckerCatchesDoDViolations(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("compiler/ergo.mod", "module compiler\n")
	mustWrite("compiler/syntax/good.ergo", `
import dense

type Row {
	kind []uint8,
	tok  []uint32,
}

func Walk(t Row) int {
	t.tok.Len()
}
`)
	mustWrite("compiler/format/bad.ergo", `
import dense
import slices

type Node {}

type BadLayout {
	Offs []int32,
	Cell []int,
}

func badWalk(n int, m Map[dense.ID[Node], int]) []uint32 {
	if n > 0 {
		let _ = children(n, []uint32{})
		return badWalk(n-1, m)
	}
	slices.Repeat([]uint32{uint32(0)}, n)
}

func children(n int, out []uint32) []uint32 { out }
`)

	rep, err := CheckStatic(root)
	if err != nil {
		t.Fatal(err)
	}
	hasRule := func(rule, substr string) bool {
		for _, f := range rep.Findings {
			if f.Rule == rule && strings.Contains(f.Message, substr) {
				return true
			}
		}
		return false
	}
	if !hasRule("no-pointer-keyed-maps", "dense id") {
		t.Errorf("expected no-pointer-keyed-maps finding, got %+v", rep.Findings)
	}
	if !hasRule("dense-uint32-ids", "Offs") || !hasRule("dense-uint32-ids", "Cell") {
		t.Errorf("expected dense-uint32-ids findings for Offs and Cell, got %+v", rep.Findings)
	}
	if !hasRule("no-per-node-scratch-allocs", "[]uint32{}") || !hasRule("no-per-node-scratch-allocs", "slices.Repeat") {
		t.Errorf("expected no-per-node-scratch-allocs findings, got %+v", rep.Findings)
	}
}

func TestSpreadPct(t *testing.T) {
	if s := spreadPct([]float64{100, 102, 100, 99, 101}); s < 2.9 || s > 3.1 {
		t.Fatalf("spreadPct = %.4f, want ~3.0", s)
	}
}

package main

import (
	"slices"
	"testing"
)

// TestWrappedReferencesMove is the smallest repro of batch 18 verify M3: a
// reference wrapped across two comment lines ("Lowering" / "9") is found
// and moved, as are a one-line reference, a linked one with two numbers,
// and one in a wrapped Markdown paragraph; a reference to another section
// or a code line is not. It catches specrefs-lines-unjoined and
// specrefs-move-first-only (scripts/specrefs' ledger).
func TestWrappedReferencesMove(t *testing.T) {
	src := "package p\n\n// Go builtins (interop.md, Lowering\n// 9) and interop.md, Lowering 13;\n// [interop.md](interop.md#lowering), Lowering 9 and 2.\nvar x = \"interop.md, Lowering 9\"\n\n// interop.md, Levels 9\n"
	pattern := referencePattern("interop.md", "Lowering")
	refs := find(src, false, pattern)
	var got []string
	for _, r := range refs {
		got = append(got, r.text)
	}
	want := []string{"interop.md, Lowering 9", "interop.md, Lowering 13", "[interop.md](interop.md#lowering), Lowering 9 and 2"}
	if !slices.Equal(got, want) {
		t.Fatalf("references %q, want %q", got, want)
	}
	if refs[0].line != 3 {
		t.Fatalf("the wrapped reference is at line %d, want 3", refs[0].line)
	}
	out, changed := rewrite(src, refs, map[string]string{"9": "13", "13": "9", "2": "5"})
	wantOut := "package p\n\n// Go builtins (interop.md, Lowering\n// 13) and interop.md, Lowering 9;\n// [interop.md](interop.md#lowering), Lowering 13 and 5.\nvar x = \"interop.md, Lowering 9\"\n\n// interop.md, Levels 9\n"
	if !changed || out != wantOut {
		t.Fatalf("moved:\n%s\nwant:\n%s", out, wantOut)
	}
	md := "See interop.md, Lowering\n9 for builtins.\n\nAnd interop.md,\nLowering 2.\n"
	if refs := find(md, true, pattern); len(refs) != 2 || refs[0].line != 1 || refs[1].line != 4 {
		t.Fatalf("markdown references %+v, want two, at lines 1 and 4", refs)
	}
}

package v1

import (
	"fmt"
	"slices"
	"testing"
)

func makeInlineTable(n int) []struct {
	Key    string
	Call   int
	Parent string
} {
	table := make([]struct {
		Key    string
		Call   int
		Parent string
	}, n)
	for i := range table {
		table[i] = struct {
			Key    string
			Call   int
			Parent string
		}{
			Key:  fmt.Sprintf("k%d", i),
			Call: i,
		}
		if i > 0 {
			table[i].Parent = fmt.Sprintf("k%d", i-1)
		}
	}
	return table
}

// TestRegisterInlinesZeroAllocs guards PERF-STARTUP-HEAP-1: registering inline
// metadata at program start retains the caller's table with zero heap
// allocations and leaves the lookup map nil until a defect traceback runs.
func TestRegisterInlinesZeroAllocs(t *testing.T) {
	t.Cleanup(func() { RegisterInlines(nil) })
	table := makeInlineTable(4096)
	RegisterInlines(table)
	inlinesMu.Lock()
	built := inlines != nil
	inlinesMu.Unlock()
	if built {
		t.Fatal("RegisterInlines eagerly allocated the inline lookup map")
	}
	allocs := testing.AllocsPerRun(100, func() {
		RegisterInlines(table)
	})
	if allocs != 0 {
		t.Fatalf("RegisterInlines allocated %.2f times per call; want 0", allocs)
	}
}

func BenchmarkRegisterInlines(b *testing.B) {
	b.Cleanup(func() { RegisterInlines(nil) })
	table := makeInlineTable(4096)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		RegisterInlines(table)
	}
}

// TestErgoStackInlinedCallsLazy exercises lazy inline map construction in
// ergoStack: nested parent chains, outermost-caller disambiguation on
// ambiguous same-line calls, unknown tags, and cache reset on re-registration.
func TestErgoStackInlinedCallsLazy(t *testing.T) {
	t.Cleanup(func() {
		RegisterCalls(nil, nil)
		registered = false
		RegisterInlines(nil)
	})
	decls := []struct {
		Name      string
		File      string
		Line, End int
	}{
		{Name: "leaf", File: "app.ergo", Line: 10, End: 15},
		{Name: "mid", File: "app.ergo", Line: 20, End: 30},
		{Name: "outer", File: "app.ergo", Line: 31, End: 40},
		{Name: "altOuter", File: "app.ergo", Line: 41, End: 45},
		{Name: "entry", File: "app.ergo", Line: 46, End: 60},
	}
	names := []string{"app.ergo", "leaf", "mid", "outer", "altOuter", "entry", "decoy"}
	// Indices: 0=app.ergo, 1=leaf, 2=mid, 3=outer, 4=altOuter, 5=entry, 6=decoy
	callTable := []SourceCall{
		// 0: mid calls leaf at app.ergo:25:7
		{Func: 2, Callee: 1, File: 0, Line: 25, Column: 7},
		// 1: outer calls mid at app.ergo:35:9
		{Func: 3, Callee: 2, File: 0, Line: 35, Column: 9},
		// 2: entry calls outer at app.ergo:50:3 (shares line 50 with decoy & altOuter)
		{Func: 5, Callee: 3, File: 0, Line: 50, Column: 3},
		// 3: entry calls decoy at app.ergo:50:15
		{Func: 5, Callee: 6, File: 0, Line: 50, Column: 15},
		// 4: altOuter calls mid at app.ergo:43:5
		{Func: 4, Callee: 2, File: 0, Line: 43, Column: 5},
		// 5: entry calls altOuter at app.ergo:50:25
		{Func: 5, Callee: 4, File: 0, Line: 50, Column: 25},
	}
	RegisterCompactCalls(decls, names, callTable)

	RegisterInlines([]struct {
		Key    string
		Call   int
		Parent string
	}{
		{Key: "k_inner", Call: 0, Parent: "k_outer"},
		{Key: "k_outer", Call: 1, Parent: ""},
	})

	stack := []byte(
		"goroutine 1 [running]:\n" +
			"panic(value)\n" +
			"\t/go/runtime/panic.go:1\n" +
			"main.outer(...)\n" +
			"\tapp.ergo#ergo-inline-k_inner:12 +0x10\n" +
			"main.entry(...)\n" +
			"\tapp.ergo:50 +0x20\n",
	)
	got := ergoStack(stack)
	want := []string{
		"leaf at app.ergo:12",
		"mid calls leaf at app.ergo:25:7",
		"outer calls mid at app.ergo:35:9",
		"entry calls outer at app.ergo:50:3",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("nested inline stack:\ngot  %q\nwant %q", got, want)
	}

	// Unknown inline tag must not fabricate a frame or match a file.
	unknownStack := []byte(
		"goroutine 1 [running]:\n" +
			"panic(value)\n" +
			"\t/go/runtime/panic.go:1\n" +
			"main.outer(...)\n" +
			"\tapp.ergo#ergo-inline-unknown:12 +0x10\n" +
			"main.entry(...)\n" +
			"\tapp.ergo:50 +0x20\n",
	)
	gotUnknown := ergoStack(unknownStack)
	wantUnknown := []string{"entry at app.ergo:50"}
	if !slices.Equal(gotUnknown, wantUnknown) {
		t.Fatalf("unknown inline tag stack:\ngot  %q\nwant %q", gotUnknown, wantUnknown)
	}

	// Re-registering inline metadata must reset the lazily-built cache so the
	// next traceback reflects the new parent chain instead of stale entries.
	RegisterInlines([]struct {
		Key    string
		Call   int
		Parent string
	}{
		{Key: "k_inner", Call: 0, Parent: "k_alt"},
		{Key: "k_alt", Call: 4, Parent: ""},
	})
	gotReset := ergoStack(stack)
	wantReset := []string{
		"leaf at app.ergo:12",
		"mid calls leaf at app.ergo:25:7",
		"altOuter calls mid at app.ergo:43:5",
		"entry calls altOuter at app.ergo:50:25",
	}
	if !slices.Equal(gotReset, wantReset) {
		t.Fatalf("after RegisterInlines reset:\ngot  %q\nwant %q", gotReset, wantReset)
	}

	// Registering nil must clear previously cached inline keys.
	RegisterInlines(nil)
	gotCleared := ergoStack(stack)
	wantCleared := []string{"entry at app.ergo:50"}
	if !slices.Equal(gotCleared, wantCleared) {
		t.Fatalf("after RegisterInlines(nil):\ngot  %q\nwant %q", gotCleared, wantCleared)
	}
}

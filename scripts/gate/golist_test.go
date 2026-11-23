package main

import (
	"slices"
	"testing"
)

// The real checkout's go lists run once per test process: a second
// declaration of the module's test packages is answered by the memo
// (kills golist-memo-bypassed), counted, not timed. A fixture checkout is
// listed afresh every time, so a package added between two lists is seen
// (kills golist-memo-any-root).
func TestGoListRunsOncePerCheckout(t *testing.T) {
	root := checkoutRoot(t)
	if goListMemo == nil || goListMemo.root != root {
		t.Fatalf("TestMain set no go list memo for %s", root)
	}
	if _, _, err := testPackages(root); err != nil {
		t.Fatal(err)
	}
	goListMemo.mu.Lock()
	runs, hits := goListMemo.runs, goListMemo.hits
	goListMemo.mu.Unlock()
	if _, _, err := testPackages(root); err != nil {
		t.Fatal(err)
	}
	goListMemo.mu.Lock()
	gotRuns, gotHits := goListMemo.runs, goListMemo.hits
	goListMemo.mu.Unlock()
	if gotRuns != runs || gotHits != hits+1 {
		t.Errorf("a second list of the checkout's test packages ran go list %d more times and was answered %d times; want 0 runs and 1 answer", gotRuns-runs, gotHits-hits)
	}
	t.Logf("go list: %d runs, %d answered from the memo so far", gotRuns, gotHits)

	fixture := t.TempDir()
	write(t, fixture, "go.mod", "module example.test/list\n\ngo 1.24\n")
	write(t, fixture, "a/a.go", "package a\n")
	first, err := goPackages(fixture)
	if err != nil {
		t.Fatal(err)
	}
	write(t, fixture, "b/b.go", "package b\n")
	second, err := goPackages(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(second, "example.test/list/b") || slices.Contains(first, "example.test/list/b") {
		t.Errorf("a fixture's lists before and after adding package b: %v, %v; want b only in the second", first, second)
	}
}

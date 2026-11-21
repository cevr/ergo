package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The trim leaves the stores at most the bound, across toolchains, and
// removes the least recently used files first: here the five oldest of
// twelve 100-byte files, for a bound of 700 bytes.
func TestGoStoreTrimHoldsTheBound(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var paths []string
	for i := range 12 {
		// Alternate toolchains and kinds, so age, not place, orders them.
		path := filepath.Join(root, fmt.Sprintf("go-%d", i%2), []string{"plans", "actions", "blobs"}[i%3], fmt.Sprintf("f%02d", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		used := start.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, used, used); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	trim, err := trimGoStores(root, 700)
	if err != nil {
		t.Fatal(err)
	}
	if trim.size > 700 || trim.removed != 5 || trim.kept != 7 {
		t.Fatalf("trim %+v; want 7 files kept, 700 bytes, 5 removed", trim)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if gone := os.IsNotExist(err); gone != (i < 5) {
			t.Errorf("file %d (used at hour %d): gone %t; want only the five least recently used gone", i, i, gone)
		}
	}
	// The store directories stay: an emission cache links them.
	for _, dir := range []string{"go-0", "go-1"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Errorf("store %s: %v", dir, err)
		}
	}
}

// A GoStore step of a plain run gets a store in the gate cache keyed by the
// go fingerprint, the same on the next run; a --no-cache run's is fresh and
// removed at its end; a step without GoStore, and a run with no gate cache,
// get none.
func TestGoStoreFollowsTheRun(t *testing.T) {
	root := checkoutRoot(t)
	cache := t.TempDir()
	step := &Step{Name: "go test ./cmd/ergo (fast)", Argv: []string{"go", "test", "./cmd/ergo"}, ToolEnv: testTools, GoStore: true}
	other := &Step{Name: "go test ./x (fast)", Argv: []string{"go", "test", "./x"}, ToolEnv: testTools}
	storeOf := func(g *gate, s *Step) (string, bool) {
		return environmentVariable(g.commandEnvironment(s), testGoStoreVariable)
	}
	var plain [2]string
	for i := range plain {
		g := &gate{root: root, cacheDir: cache}
		store, ok := storeOf(g, step)
		if !ok || !strings.HasPrefix(store, filepath.Join(cache, goStoreDir, "go-")) {
			t.Fatalf("plain run %d: store %q (set %t); want one under %s", i, store, ok, filepath.Join(cache, goStoreDir))
		}
		if _, ok := storeOf(g, other); ok {
			t.Errorf("a step without GoStore got a store")
		}
		g.closeGoStores(io.Discard)
		if _, err := os.Stat(store); err != nil {
			t.Fatalf("plain run %d: the store is gone after the run: %v", i, err)
		}
		plain[i] = store
	}
	if plain[0] != plain[1] {
		t.Errorf("two plain runs got stores %q and %q; want one, persistent", plain[0], plain[1])
	}
	g := &gate{root: root, cacheDir: cache, noCache: true}
	fresh, ok := storeOf(g, step)
	if !ok || fresh == plain[0] {
		t.Fatalf("--no-cache run: store %q (set %t); want a fresh one, not %q", fresh, ok, plain[0])
	}
	g.closeGoStores(io.Discard)
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("--no-cache run: its fresh store %s is left after the run (%v)", fresh, err)
	}
	if _, ok := storeOf(&gate{root: root}, step); ok {
		t.Errorf("a run with no gate cache got a store")
	}
}

// In both tiers the steps of goStorePackages get the store, and no other
// step does.
func TestGoStorePackagesGetTheStore(t *testing.T) {
	root := checkoutRoot(t)
	module := modulePath(root)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for tier, steps := range map[string][]*Step{fullTier: full, fastTier: fast} {
		found := 0
		for _, step := range steps {
			ours := runsGoTest(step) && slices.ContainsFunc(goStorePackages, func(relative string) bool {
				return slices.Contains(step.Argv, module+"/"+relative) || slices.Contains(step.Argv, "./"+relative)
			})
			if ours {
				found++
			}
			if step.GoStore != ours {
				t.Errorf("%s tier: %s GoStore %t; want %t", tier, step.Name, step.GoStore, ours)
			}
		}
		if found == 0 {
			t.Errorf("%s tier: no step runs the tests of %v", tier, goStorePackages)
		}
	}
}

package closure

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The std store holds only what `go build std` compiles, outside the
// checkout, and every run's cache shares it read-only (stdcache_test.go).
// With a go command that logs each call and writes one cache file per
// build: each build of stdBuilds runs once, with cgo off, as `go ... std` from a
// directory outside the checkout, into the fill directory, on the
// checkout's toolchain; the store replaces every other entry, is filled
// once, and its files are read-only; seedCache places each entry file and
// nothing else; and a GOROOT inside the checkout is refused before any
// build.
func TestStdStoreHoldsOnlyStdCompiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "go.log")
	fakeGo := func(goroot string) {
		script := "#!/bin/sh\n" +
			"if [ \"$1\" = env ]; then printf '{\"GOVERSION\":\"go0.0-fake\",\"GOROOT\":\"%s\"}' '" + goroot + "'; exit 0; fi\n" +
			"printf '%s|%s|%s|%s|%s\\n' \"$PWD\" \"$GOCACHE\" \"$GOTOOLCHAIN\" \"$CGO_ENABLED\" \"$*\" >> '" + log + "'\n" +
			"mkdir -p \"$GOCACHE/ab\" && printf '%s' \"$*\" > \"$GOCACHE/ab/$(printf '%s' \"$GOOS$GOARCH $*\" | tr ' /=-' '____')-d\"\n"
		if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := func() []string {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(data) == 0 {
			return nil
		}
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	parent := filepath.Join(temp, stdStoreDir)
	if err := os.MkdirAll(filepath.Join(parent, "0123456789abcdef"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeGo(t.TempDir())
	store, err := fillStdStore(root)
	if err != nil {
		t.Fatal(err)
	}
	logged := calls()
	if len(logged) != len(stdBuilds()) {
		t.Fatalf("%d go builds, want one per std build (%d): %q", len(logged), len(stdBuilds()), logged)
	}
	for _, line := range logged {
		fields := strings.Split(line, "|")
		dir, cache, toolchain, cgo, args := fields[0], fields[1], fields[2], fields[3], strings.Fields(fields[4])
		if rel, err := filepath.Rel(root, dir); err != nil || !strings.HasPrefix(rel, "..") {
			t.Errorf("a store build ran in %s, inside the checkout %s", dir, root)
		}
		if args[len(args)-1] != "std" || slices.ContainsFunc(args[1:len(args)-1], func(a string) bool { return !strings.HasPrefix(a, "-") }) {
			t.Errorf("a store build builds %q, not std alone", args)
		}
		if filepath.Dir(cache) != parent || !strings.Contains(filepath.Base(cache), ".fill-") {
			t.Errorf("a store build wrote GOCACHE %s, not a fill directory of %s", cache, parent)
		}
		if cgo != "0" {
			t.Errorf("a store build ran with CGO_ENABLED=%q; want 0, so no C linker reads the checkout's directory", cgo)
		}
		if toolchain != "go0.0-fake" {
			t.Errorf("a store build ran GOTOOLCHAIN=%q, not the checkout's go0.0-fake", toolchain)
		}
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 2 {
		t.Errorf("the store directory holds %v (%v), want only the lock and the store", entries, err)
	}
	stored := 0
	_ = filepath.WalkDir(store, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if info, err := d.Info(); err != nil || info.Mode().Perm()&0o222 != 0 {
			t.Errorf("store file %s is writable (%v)", p, err)
		}
		if filepath.Base(p) != stdStoreComplete {
			stored++
		}
		return nil
	})
	if again, err := fillStdStore(root); err != nil || again != store || len(calls()) != len(logged) {
		t.Errorf("a second fill: store %s (%v) after %d builds; want %s and no build", again, err, len(calls())-len(logged), store)
	}

	cache := t.TempDir()
	placed, err := seedCache(store, cache)
	if err != nil {
		t.Fatal(err)
	}
	seeded := 0
	_ = filepath.WalkDir(cache, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			seeded++
		}
		return err
	})
	if placed != stored || seeded != stored || stored != len(stdBuilds()) {
		t.Errorf("seedCache placed %d files (%d in the cache) of the store's %d entries, from %d builds", placed, seeded, stored, len(stdBuilds()))
	}

	fakeGo(filepath.Join(root, "goroot"))
	if _, err := fillStdStore(root); err == nil || len(calls()) != len(logged) {
		t.Errorf("a GOROOT inside the checkout: error %v after %d builds; want a refusal and no build", err, len(calls())-len(logged))
	}
}

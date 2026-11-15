package sketchfront

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/casegen"
	"ergo.local/prototype/internal/sketch/eval/evalgen"
	"ergo.local/prototype/internal/sketch/lower"
)

// resetStdCache simulates the next process. No test that uses it is parallel.
func resetStdCache(t *testing.T) {
	t.Helper()
	check.ResetStdForTest()
	stdCache.once = sync.Once{}
	stdCache.mu.Lock()
	stdCache.compiler = ""
	stdCache.mu.Unlock()
}

// TestStdCacheWork is the disk-cache acceptance guard. The cold process
// writes a snapshot, the warm process checks zero std packages, and a corrupt
// file or a failed write falls back to source. It also guards both cache key
// inputs and excludes process-only producer identities. Source-only checks
// cannot catch any of these mutants (testdata/mutants.json).
func TestStdCacheWork(t *testing.T) {
	savedIdentity := compilerIdentity
	stdCache.mu.Lock()
	savedRoot := stdCache.root
	stdCache.mu.Unlock()
	t.Cleanup(func() {
		compilerIdentity = savedIdentity
		UseStdCache(savedRoot)
		resetStdCache(t)
	})
	compiler := "sha256:" + strings.Repeat("a", 64)
	compilerIdentity = func() (string, error) { return compiler, nil }
	deps := stdDependencies()
	key := stdSnapshotKey(compiler, deps)
	if stdSnapshotKey(compiler+"b", deps) == key {
		t.Fatal("compiler identity is not keyed")
	}
	for i := range deps {
		edited := slices.Clone(deps)
		edited[i].Implementation += "edit"
		if stdSnapshotKey(compiler, edited) == key {
			t.Fatalf("std source %s is not keyed", deps[i].Name)
		}
		edited = slices.Clone(deps)
		edited[i].Name += "edit"
		if stdSnapshotKey(compiler, edited) == key {
			t.Fatalf("std package identity %s is not keyed", deps[i].Name)
		}
	}
	root := t.TempDir()
	UseStdCache(root)
	source := "import time\nfunc Now() int64 { time.NewManual(0).Now().UnixSeconds() }\n"
	run := func(warm bool) {
		t.Helper()
		resetStdCache(t)
		a := Check(source)
		if len(a.Diagnostics) != 0 {
			t.Fatal(a.Diagnostics)
		}
		got := check.WorkDone()
		if warm && sourceWork(got) {
			t.Fatalf("warm cache did %+v; want zero std source work", got)
		}
		if !warm && got.StdPackagesChecked == 0 {
			t.Fatal("cold or invalid cache did not check std from source")
		}
	}
	run(false)
	path := filepath.Join(root, "std", key+".snap")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run(true)
	// A truncated cache file is not a partially installed universe.
	if err := os.WriteFile(path, data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	// A second check in this process must not touch the cache again:
	// std may now hold lazily created instances of the caller's types.
	if a := Check(source); len(a.Diagnostics) != 0 {
		t.Fatal(a.Diagnostics)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(data[:len(data)/2]) {
		t.Fatalf("a second check rewrote the cache: %v", err)
	}
	run(false)
	run(true) // fallback repairs the cache
	compiler = "process:" + strings.Repeat("b", 64)
	run(false)
	privatePath := filepath.Join(root, "std", stdSnapshotKey(compiler, deps)+".snap")
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("process-only compiler published a snapshot: %v", err)
	}
	compiler = "sha256:" + strings.Repeat("c", 64)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	UseStdCache(blocked)
	run(false)
	// CheckDir must prepare std too, not just the single-source entry.
	UseStdCache(root)
	compiler = "sha256:" + strings.Repeat("a", 64)
	resetStdCache(t)
	dir := writeTree(t, map[string]string{"main.ergo": source})
	a, err := CheckDir(dir)
	if err != nil || len(a.Diagnostics) != 0 || sourceWork(check.WorkDone()) {
		t.Fatalf("warm CheckDir: %v, %v, work %+v", err, a.Diagnostics, check.WorkDone())
	}
}

type stdCacheCase struct {
	name, source, dir string
}

// TestStdCacheAgreesWithSource compares the exact diagnostics, inspect
// envelope, both lowered modes and evaluator reports over every example
// entry plus seeded evalgen/casegen programs. The reference checks all std
// first, in the same package order as ExportStd. Both passes then check the
// same callers in the same order. Nothing sorts or normalizes an output.
// The warm-work mutant exercises this path too; ordinary source-only
// agreement tests never exercise an installed graph.
func TestStdCacheAgreesWithSource(t *testing.T) {
	UseStdCache("")
	t.Cleanup(func() { resetStdCache(t) })
	root, err := filepath.Abs("../../examples")
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := filepath.Glob(filepath.Join(root, "*/cmd/*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs = append(dirs, filepath.Join(root, "editions/sketch"))
	var cases []stdCacheCase
	for _, dir := range dirs {
		cases = append(cases, stdCacheCase{name: strings.TrimPrefix(dir, root+"/"), dir: dir})
	}
	first, n := uint64(1), uint64(24)
	if s := os.Getenv("ERGO_STD_CACHE_SEED"); s != "" {
		first, err = strconv.ParseUint(s, 0, 64)
		if err != nil {
			t.Fatal(err)
		}
		n = 1
		cases = nil
	}
	for offset := uint64(0); offset < n; offset++ {
		seed := first + offset
		for _, p := range []struct{ name, source string }{
			{"eval", evalgen.Generate(seed).Source}, {"cases", casegen.Generate(seed).Source},
		} {
			cases = append(cases, stdCacheCase{name: fmt.Sprintf("%s/%d", p.name, seed), source: p.source, dir: writeTree(t, map[string]string{"main.ergo": p.source})})
		}
	}
	// Include diagnostics: an erased cached error row must not admit a caller.
	cases = append(cases, stdCacheCase{name: "diagnostic", source: "import strconv\nfunc main() { let n = strconv.Atoi(\"bad\") }\n"})
	resetStdCache(t)
	data, err := check.ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	reference := make([]map[string]string, len(cases))
	for i, c := range cases {
		reference[i] = stdCacheOutcome(t, c)
		if c.source != "" && c.name != "diagnostic" && reference[i]["diagnostics"] != "null" {
			t.Fatalf("%s does not check:\n%s\n%s", c.name, c.source, reference[i]["diagnostics"])
		}
	}
	resetStdCache(t)
	if err := check.InstallStd(data); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got := stdCacheOutcome(t, c)
		for key, want := range reference[i] {
			if got[key] != want {
				t.Fatalf("%s %s differs\nsource:\n%s\nfrom source:\n%s\ncached:\n%s\nreplay generated: ERGO_STD_CACHE_SEED=%s go test -run '^TestStdCacheAgreesWithSource$' ./internal/sketchfront",
					c.name, key, c.source, want, got[key], strings.TrimPrefix(strings.TrimPrefix(c.name, "eval/"), "cases/"))
			}
		}
	}
	if got := check.WorkDone(); sourceWork(got) {
		t.Fatalf("cached property checked std from source: %+v", got)
	}
}

// sourceWork reports std work done from source; decoding an installed
// snapshot is not.
func sourceWork(w check.Work) bool { return w.StdFilesParsed != 0 || w.StdPackagesChecked != 0 }

func stdCacheOutcome(t *testing.T, c stdCacheCase) map[string]string {
	t.Helper()
	out := map[string]string{}
	var a Analysis
	var err error
	if c.dir != "" {
		a, err = CheckDir(c.dir)
	} else {
		a = Check(c.source)
	}
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	marshal := func(x any) string {
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	out["diagnostics"] = marshal(a.Diagnostics)
	if len(a.Diagnostics) > 0 {
		return out
	}
	out["inspect"] = marshal(InspectGo(a.Checked()))
	if a.program == nil {
		t.Fatal("a valid property caller needs its loaded program")
	}
	for _, mode := range []struct {
		name  string
		lower func(lower.Unit) ([]lower.File, error)
	}{{"build", lower.Lower}, {"test", lower.LowerTests}} {
		files, err := mode.lower(a.program.unit())
		var b strings.Builder
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n", err)
		}
		for _, f := range files {
			fmt.Fprintf(&b, "== %s\n%s\n", f.Path, f.Data)
		}
		out["lower-"+mode.name] = b.String()
	}
	ev, err := a.Evaluation(eval.DefaultBudget(), c.dir)
	out["eval"] = fmt.Sprintf("%s %v", marshal(ev), err)
	return out
}

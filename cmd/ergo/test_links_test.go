package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/stage"
)

// readStageReport reads the counters a command wrote to ERGO_STAGE_REPORT.
func readStageReport(t *testing.T, path string) map[string]int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	if err := json.Unmarshal(data, &counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

// TestTestResultCache is the counted receipt of g-b2, and the law that a
// cached verdict is the verdict of a rerun. `ergo test ./...` over a module
// of passing packages and one failing package, counted by its stage report
// (ERGO_STAGE_REPORT):
//
//   - cold, each package's test binary is linked once;
//   - warm, each passing package replays its cached result, so its run
//     neither builds nor runs anything; the failing one, never cached,
//     runs again from the binary the action store holds: no compile, no
//     link, one process;
//   - --count 1 on an empty store runs every package from the module's one
//     test binary (TEST-one), lowering that module alone; after the cold
//     run it runs each package's own stored binary and builds nothing
//     (TEQ), and the output and exit status of each are the cold run's.
//
// It kills teq-stored-never (the module binary is built though every
// package's own is stored) and teq-stored-lowers (the probe of the store
// lowers each package's module), and teqb-shared-text (the module's test
// binary lowered as Go source text, not through IR, as binaries_ir and
// binaries_text count it).
//
// A run that differs in what it reads (a --run name, a GO* variable) is no
// hit; a go wrapper of the same release links with the same patched go,
// so it is one. It kills the g-b2 cache mutants (cache-*), which no
// other test reaches: none runs a package twice. It kills the obligation
// counters' mutants (ft0-native-not-counted, ft0-stored-not-counted,
// ft0-evaluated-not-counted): the cold run decides 2 obligations per
// passing package and 1 for the failing one natively, the warm run answers
// the passing ones from the store, and a check evaluates them.
// It builds Go because the claim is about the compiles, links and
// processes of real test builds.
func TestTestResultCache(t *testing.T) {
	if testing.Short() {
		t.Skip("the counts of real test builds run in the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module links\n")
	const passing = 3
	for i := range passing {
		smokeWrite(t, root, fmt.Sprintf("p%d/p.ergo", i), fmt.Sprintf("test A%d { assert %d == %d }\ntest B%d { assert true }\n", i, i, i, i))
	}
	smokeWrite(t, root, "q/q.ergo", "test Fails { assert 1 == 2 }\n")
	type outcome struct {
		stdout                                                string
		code, notes                                           int
		links, reused, compiles, processes, lowered, ir, text int64
		native, stored, evaluated, steps                      int64
	}
	run := func(env []string, args ...string) outcome {
		t.Helper()
		report := filepath.Join(t.TempDir(), "stage.json")
		// --native: the counts are the test binaries' (the evaluator,
		// tier 1, would answer these packages with no build).
		command := exec.Command(bin, append([]string{"test", "--native"}, args...)...)
		command.Dir = root
		// The counts are the direct path's, which a GOFLAGS of the
		// caller's (the mutant runner's -p) would turn away.
		command.Env = append(append(os.Environ(), "GOFLAGS=", "ERGO_STAGE_REPORT="+report), env...)
		out, stderr, code := runSmokeCommand(t, command)
		counts := readStageReport(t, report)
		return outcome{
			stdout: string(out), code: code,
			notes: strings.Count(string(stderr), cachedNote),
			links: counts["links"], reused: counts["links_reused"], compiles: counts["go_compiles"], processes: counts["processes"],
			native: counts["obligations_native"], stored: counts["obligations_stored"], evaluated: counts["obligations_evaluated"], steps: counts["eval_steps"],
			lowered: counts["modules_lowered"], ir: counts["binaries_ir"], text: counts["binaries_text"],
		}
	}
	// --count 1 before any run: the store holds no package's own binary,
	// so the module's one test binary is lowered and linked, and no
	// package's module is lowered to find that out.
	fresh := run(nil, "./...", "--count", "1")
	if fresh.links != 1 || fresh.lowered != 1 || fresh.ir != 1 || fresh.text != 0 {
		t.Fatalf("--count 1 on an empty store: %+v; want one module lowered and one link (the module's test binary, lowered through IR)", fresh)
	}
	cold := run(nil, "./...")
	if cold.code != 1 || cold.links != passing+1 || cold.notes != 0 || strings.Count(cold.stdout, "PASS (2 tests)") != passing || cold.native != 2*passing+1 || cold.stored != 0 {
		t.Fatalf("cold run: %+v", cold)
	}
	warm := run(nil, "./...")
	if warm.links != 0 || warm.reused != 1 || warm.compiles != 0 || warm.processes != 1 || warm.notes != passing || warm.native != 1 || warm.stored != 2*passing {
		t.Fatalf("warm run: %+v; want no link, 1 reused link and 1 process (the failing package: a result is found before the link), no compile, %d replays", warm, passing)
	}
	// --count 1 keeps no warm answer, so it runs every package again. The
	// store holds each package's own test binary (the cold run linked
	// them), so it runs those and builds nothing, not the module's one
	// binary (TEQ): no lowering, compile or link.
	again := run(nil, "./...", "--count", "1")
	if again.notes != 0 || again.links != 0 || again.lowered != 0 || again.compiles != 0 || again.reused != passing+1 || again.processes != passing+1 {
		t.Fatalf("--count 1 after cached runs: %+v; want no replay, lowering, compile or link, %d reused links and %d processes", again, passing+1, passing+1)
	}
	for _, o := range []outcome{warm, fresh, again} {
		if o.stdout != cold.stdout || o.code != cold.code {
			t.Fatalf("a cached verdict differs from a run:\nexit %d\n%s\nwant exit %d\n%s", o.code, o.stdout, cold.code, cold.stdout)
		}
	}

	// The obligation counters (test-speed FT0): a check evaluates the
	// package's two tests at check time.
	if c := runWarm(t, bin, root, physicalTempDir(t), nil, "check", "./p0"); c.code != 0 || c.counts["obligations_evaluated"] != 2 || c.counts["eval_steps"] == 0 {
		t.Fatalf("ergo check ./p0: exit %d, %v", c.code, c.counts)
	}

	// What the run reads keys its result.
	if one := run(nil, "./p0"); one.code != 0 || one.notes != 0 {
		t.Fatalf("./p0: %+v", one)
	}
	if picked := run(nil, "./p0", "--run", "A0"); picked.notes != 0 || strings.Contains(picked.stdout, "B0") || !strings.Contains(picked.stdout, "ok A0") {
		t.Fatalf("--run A0 after a cached ./p0: %+v", picked)
	}
	if o := run([]string{"GODEBUG=madvdontneed=1"}, "./p0"); o.notes != 0 || o.code != 0 {
		t.Fatalf("another GODEBUG replayed: %+v", o)
	}
	goCommand, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	if err := os.WriteFile(filepath.Join(wrapper, "go"), []byte("#!/bin/sh\nexec "+goCommand+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The go command that links is the patched release of the selected Go
	// (buildAction, IRT0b), not the PATH entry: a wrapper that runs the
	// same release links with the same go, so its run replays.
	// TestTestResultKeysTheLinkingGo keys a reinstalled linking go.
	if o := run([]string{"PATH=" + wrapper + string(os.PathListSeparator) + os.Getenv("PATH")}, "./p0"); o.notes != 1 || o.code != 0 {
		t.Fatalf("a go wrapper of the same release did not replay: %+v", o)
	}
}

// TestModuleTestBinaryOfARequiringModule is the counted guard of TEQ: for
// a module that requires another (examples catalog and pipeline, each with
// a go.mod), `ergo test --count 1 ./...` lowers and builds the module's one
// test binary and no package's own (modules_lowered 1, no fallback), and
// prints no fallback line. Its go commands also count the law ledger's
// check builds (rewrite.go), so the guard counts the lowered modules. It
// kills teq-shared-gomod-no-runtime: the shared load's go.mod without the
// runtime's require and replace, whose build fails, so each package was
// lowered and built alone.
// It builds Go because the claim is about the modules of a real test
// build; the examples are copied, as `ergo test` writes their law ledger.
func TestModuleTestBinaryOfARequiringModule(t *testing.T) {
	if testing.Short() {
		t.Skip("the counts of real test builds run in the full tier")
	}
	bin := buildTestCLI(t)
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	base := physicalTempDir(t)
	// The examples' go.mod files replace their modules with ../../modules.
	for _, dir := range []string{"examples/catalog", "examples/pipeline", "modules"} {
		if err := os.CopyFS(filepath.Join(base, dir), os.DirFS(filepath.Join(repo, dir))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"catalog", "pipeline"} {
		report := filepath.Join(t.TempDir(), "stage.json")
		command := exec.Command(bin, "test", "--count", "1", "./...")
		command.Dir = filepath.Join(base, "examples", name)
		command.Env = append(os.Environ(), "ERGO_SHARED_FALLBACK=", "ERGO_STAGE_REPORT="+report)
		out, stderr, code := runSmokeCommand(t, command)
		counts := readStageReport(t, report)
		if code != 0 || counts["modules_lowered"] != 1 || counts["shared_fallbacks"] != 0 || strings.Contains(string(stderr), "tested alone") {
			t.Errorf("%s: exit %d, %d modules lowered, %d fallbacks; want exit 0, 1 module (the module's test binary), none\n%s\n%s",
				name, code, counts["modules_lowered"], counts["shared_fallbacks"], out, stderr)
		}
	}
}

// TestSharedFallbackSaysSo: when the module's test binary does not build,
// `ergo test DIR/...` says so once on stderr with the go error's first
// line and counts it, then tests each package alone; with
// ERGO_SHARED_FALLBACK=fail (the tests and the gate) the run fails
// instead. It kills teq-fallback-silent and teq-fallback-not-strict.
func TestSharedFallbackSaysSo(t *testing.T) {
	const output = "# ergotest0\nergotest0/main.go:6:2: cannot find module providing package ergo.local/prototype/runtime/rt\nergotest1/main.go:7:2: another\n"
	const first = "ergotest0/main.go:6:2: cannot find module providing package ergo.local/prototype/runtime/rt"
	goErr := errors.New("Go build failed: exit status 1")
	var stderr strings.Builder
	before := stage.Get(stage.SharedFallbacks)
	shared, err := sharedFallback(&stderr, false, goErr, output)
	if shared || err != nil || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "tested alone: "+first+"\n") {
		t.Fatalf("fallback: shared %t, %v, stderr %q; want each package alone and one line naming %q", shared, err, stderr.String(), first)
	}
	if n := stage.Get(stage.SharedFallbacks) - before; n != 1 {
		t.Fatalf("%d fallbacks counted, want 1", n)
	}
	stderr.Reset()
	shared, err = sharedFallback(&stderr, true, goErr, output)
	if !shared || err == nil || !strings.Contains(err.Error(), first) || stderr.Len() != 0 {
		t.Fatalf("strict fallback: shared %t, %v, stderr %q; want the run to fail naming %q", shared, err, stderr.String(), first)
	}
}

// TestTestResultKeysTheLinkingGo: a test result is keyed by the go command
// that links the binary (its path, size and modification time), so a
// reinstalled Go misses. It kills cache-go-unkeyed, which the process test
// cannot reach since IRT0b: the linking go is the patched release in
// ergo's cache, which no test may replace.
func TestTestResultKeysTheLinkingGo(t *testing.T) {
	goPath := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tc := build.Toolchain{Go: goPath, Root: "/nonexistent/goroot", GOOS: "linux", GOARCH: "amd64"}
	ledger := filepath.Join(t.TempDir(), "no-ledger")
	before := testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger)
	if before == "" || before != testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger) {
		t.Fatalf("the key of one run is %q, then another", before)
	}
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\n# reinstalled\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if after := testResultKey(fileOptions{}, tc, []string{"-run", "A"}, 0, ledger); after == before {
		t.Fatal("a reinstalled go command keeps the test result's key")
	}
}

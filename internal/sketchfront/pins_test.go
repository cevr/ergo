package sketchfront

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/toolchain"
	"ergo.local/prototype/toolchain/pin"
)

// pinProgram is a program whose build links the runtime and std.
const pinProgram = "func main() needs Console {\n\tConsole.Println(\"pinned\")\n}\n"

// pinTestSets makes the std and runtime sets of target with the stock
// go command on PATH into a new directory, and a lock that names them.
func pinTestSets(t *testing.T, target PinTarget) (string, pinset.Lock) {
	t.Helper()
	out := t.TempDir()
	produced, err := ProducePins(PinConfig{Go: "go", Store: t.TempDir(), Out: out, Targets: []PinTarget{target}, Tags: [][]string{nil}})
	if err != nil {
		t.Fatal(err)
	}
	lock := pinset.Lock{Sets: map[string]string{}}
	for _, p := range produced {
		if !strings.Contains(p.Name, "-"+target.GOOS+"-"+target.GOARCH+"-") {
			t.Errorf("the set %s, made for %s/%s, is named for another target", p.Name, target.GOOS, target.GOARCH)
		}
		lock.Sets[p.Name] = p.Digest
	}
	if len(lock.Sets) != 2 {
		t.Fatalf("produced %v, want a std and a runtime set", produced)
	}
	return out, lock
}

// pinBuild builds pinProgram for target into a new store and returns the
// executable's bytes, the report and the std archives the go command
// gave the plan (stage.StdExports).
func pinBuild(t *testing.T, target PinTarget, pins build.Pins) ([]byte, build.Report, int64) {
	t.Helper()
	stage.Reset()
	output := filepath.Join(t.TempDir(), "app")
	var report build.Report
	var stderr bytes.Buffer
	tc := build.Toolchain{Go: "go", GOOS: target.GOOS, GOARCH: target.GOARCH, Pins: pins}
	_, err := Build(Check(pinProgram), BuildRequest{Mode: build.ModeBuild, Source: "/x/main.ergo", Output: output,
		Cache: build.Cache{Root: t.TempDir()}, Toolchain: tc, Report: &report, Stderr: &stderr})
	if err != nil || !report.Direct {
		t.Fatalf("build for %s/%s: %v (%s)\n%s", target.GOOS, target.GOARCH, err, report, stderr.String())
	}
	exe, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return exe, report, stage.Get(stage.StdExports)
}

// otherTarget is a target that is not the host's, so a set looked up by
// the host's platform is never the target's.
func otherTarget() PinTarget {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return PinTarget{GOOS: "linux", GOARCH: "amd64"}
	}
	return PinTarget{GOOS: "darwin", GOARCH: "arm64"}
}

// TestColdBuildCompilesNoPinPackage: with the pin sets of a target served
// from a file URL, and the direct path's store and Go's build cache empty,
// a build for that target takes every std archive and every runtime
// package from the sets: the go command exports no std archive (today it
// compiles about 206, plan §1.3), and the only compile is the program's
// own package. The executable is the bytes of the build without sets. The
// target is not the host's, and each set must be named for it, so a set
// keyed by the host's platform fails (cs-pin-wrong-target). It builds Go because the question is
// which compiles a real build runs and the executable it links.
func TestColdBuildCompilesNoPinPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go (full tier)")
	}
	target := otherTarget()
	out, lock := pinTestSets(t, target)
	want, ref, refStd := pinBuild(t, target, build.Pins{})
	if refStd == 0 || ref.Compiled < 2 {
		t.Fatalf("the build without sets exported %d std archives and compiled %d packages; the test needs both", refStd, ref.Compiled)
	}
	t.Setenv("GOCACHE", t.TempDir())
	var log bytes.Buffer
	pins := build.Pins{Source: pinset.Source{Dir: t.TempDir(), URL: "file://" + filepath.ToSlash(out), Lock: lock, Log: &log}}
	got, report, std := pinBuild(t, target, pins)
	if std != 0 || report.Compiled != 1 || report.Pinned != ref.Compiled-1 {
		t.Errorf("with sets: %d std archives from the go command, %d compiles, %d from the runtime set; want 0, 1 (main), %d (%s)\n%s",
			std, report.Compiled, report.Pinned, ref.Compiled-1, report, log.String())
	}
	if !bytes.Equal(got, want) {
		t.Error("the executable built from the sets is not the one built without them")
	}
}

// TestRefusedArchiveFallsBack: a served set whose bytes are not the ones
// the lock names is refused, with a line on the log that says so; the
// build compiles what the set would have held, nothing is installed, and
// the executable is the bytes of the build without sets
// (cs-pin-digest-unchecked). It builds Go because the fallback is a real
// build's.
func TestRefusedArchiveFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go (full tier)")
	}
	host := PinTarget{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	out, lock := pinTestSets(t, host)
	want, ref, _ := pinBuild(t, host, build.Pins{})
	// Each set is served with one entry more than the set the lock names.
	served := t.TempDir()
	installed := pinset.Source{Dir: t.TempDir(), URL: "file://" + filepath.ToSlash(out), Lock: lock}
	for name := range lock.Sets {
		set := installed.Open(name, "the test")
		if set == nil {
			t.Fatalf("the produced set %s does not install", name)
		}
		var entries []pinset.Entry
		paths := map[string]string{}
		for _, e := range set.Entries {
			entries = append(entries, e)
			paths[e.Name] = set.Path(e)
		}
		extra := entries[0]
		extra.Name = "zz-extra"
		entries = append(entries, extra)
		paths[extra.Name] = set.Path(extra)
		if _, err := pinset.Write(served, name, entries, paths); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	dir := t.TempDir()
	pins := build.Pins{Source: pinset.Source{Dir: dir, URL: "file://" + filepath.ToSlash(served), Lock: lock, Log: &log}}
	got, report, std := pinBuild(t, host, pins)
	for name := range lock.Sets {
		if !strings.Contains(log.String(), "ergo: pin archives "+name+" refused: its sha256 is ") {
			t.Errorf("the log does not say that %s was refused:\n%s", name, log.String())
		}
	}
	if std == 0 || report.Pinned != 0 || report.Compiled != ref.Compiled {
		t.Errorf("after refusals: %d std archives from the go command, %d runtime packages from a set, %d compiles; want some, 0, %d", std, report.Pinned, report.Compiled, ref.Compiled)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("refused sets left %v installed", entries)
	}
	if !bytes.Equal(got, want) {
		t.Error("the executable of the fallback is not the one built without sets")
	}
}

// lockTargets are the targets toolchain/pin/lock has sets for.
var lockTargets = []PinTarget{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "darwin", GOARCH: "arm64"}}

// TestPinArchivesMatchLocalBuild: the sets toolchain/pin/lock names are
// the sets this tree builds: the std, runtime and tools sets of
// linux/amd64 and darwin/arm64, made here with the pinned release and the patched compile
// (PatchedRelease), have the lock's sha256, and the lock's header names
// the release and the patch. A runtime, a std library or a patch edit
// without `go run ./scripts/pinsets -o DIR -lock` fails it. A runtime set
// is keyed by the host as well (its compile keys hold the host's compile),
// so on another host only the std and tools sets are compared. It builds
// Go because the question is the compiler's output.
func TestPinArchivesMatchLocalBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Go (full tier)")
	}
	lock, err := pinset.ParseLock(pin.Lock)
	if err != nil {
		t.Fatal(err)
	}
	root, err := build.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := toolchain.Select(os.Getenv, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := PatchedRelease(selected, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Go != toolchain.Version || lock.Patch != patched.Digest {
		t.Errorf("the lock is for %s and patch %s; the pin is %s and patch %s", lock.Go, lock.Patch, toolchain.Version, patched.Digest)
	}
	source, err := ReleaseSource(selected, root)
	if err != nil {
		t.Fatal(err)
	}
	produced, err := ProducePins(PinConfig{Go: patched.Go, GoRoot: patched.Root, Patch: patched.Digest, Source: source,
		Store: t.TempDir(), Out: t.TempDir(), Targets: lockTargets, Tools: true})
	if err != nil {
		t.Fatal(err)
	}
	// The lock's runtime sets were built on linux/amd64; another host
	// builds runtime sets of its own name.
	lockHost := runtime.GOOS+"/"+runtime.GOARCH == "linux/amd64"
	compared := func(name string) bool { return lockHost || !strings.HasPrefix(name, "runtime-") }
	var lines []string
	for _, p := range produced {
		lines = append(lines, p.LockLine())
		if want := lock.Sets[p.Name]; compared(p.Name) && want != p.Digest {
			t.Errorf("set %s: built sha256:%s, the lock says %q", p.Name, p.Digest, want)
		}
	}
	for name := range lock.Sets {
		if compared(name) && !slices.ContainsFunc(produced, func(p pinset.Produced) bool { return p.Name == name }) {
			t.Errorf("the lock names %s, which this tree does not build", name)
		}
	}
	if t.Failed() {
		t.Logf("the lock lines of this tree (go run ./scripts/pinsets -o DIR -lock writes them):\n%s", strings.Join(lines, "\n"))
	}
}

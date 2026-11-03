package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/testinputs"
)

// TestMain puts a small stand-in for go first on PATH. Go's test cache logs
// every file a test opens or stats, and on each cached lookup of this package
// go resolves the symlinks of every logged path again (search.InDir, to tell
// files inside the module from files outside it). Tests here fingerprint the
// tools on PATH, and under go test, go is the real toolchain (go test puts
// GOROOT/bin first), whose GOROOT holds about 12,000 files; see docs/research/
// gate-throughput.md for the measured lookup cost. The stand-in runs the real
// go and answers go env, which only fingerprinting asks, with a one-file
// GOROOT. git stays real: its exec path holds about 200 files here. Tests of
// the fingerprint rules install their own tools. The stand-in exists only in
// the process that runs the tests, under observation (testinputs.MainFunc).
func TestMain(m *testing.M) {
	os.Exit(testinputs.MainFunc(m, func() int {
		stub, err := os.MkdirTemp("", "gate-tools-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(stub)
		testScratch = stub
		startEnvironment = os.Environ()
		bin, goroot := filepath.Join(stub, "bin"), filepath.Join(stub, "goroot")
		files := map[string]string{
			filepath.Join(goroot, "VERSION"): "go1.0.0-stub\n",
		}
		for _, tool := range []string{"go", "git"} {
			if real, err := exec.LookPath(tool); err == nil {
				realTools[tool] = real
			}
		}
		if real := realTools["go"]; real != "" {
			files[filepath.Join(bin, "go")] = "#!/bin/sh\n" +
				"if [ \"$1\" = env ]; then echo '{\"GOVERSION\": \"go1.0.0-stub\", \"GOROOT\": \"" + goroot + "\"}'; exit 0; fi\n" +
				"exec " + real + " \"$@\"\n"
		}
		for path, content := range files {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				panic(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
				panic(err)
			}
		}
		os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return m.Run()
	}))
}

// realTools are the tools PATH named before TestMain put its stand-ins first.
var realTools = map[string]string{}

// startEnvironment is the environment the test binary started with.
var startEnvironment []string

// testScratch is TestMain's directory, removed when the tests end.
var testScratch string

// probeSource is a small executable standing in for a tool. It prints a
// version that never changes. Beside a file <self>.hang it first sleeps for
// 30 s, as a tool that never answers. self is its file with symlinks
// resolved: tests run it through a link on PATH, and os.Executable names the
// link on darwin (measured on darwin/arm64), the file on Linux.
const probeSource = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var build = "` + probeBuildMarker + `"

func main() {
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		os.Exit(2)
	}
	if _, err := os.Stat(self + ".hang"); err == nil {
		time.Sleep(30 * time.Second)
	}
	if len(os.Args) > 1 && os.Args[1] == "--build" {
		fmt.Println(build)
		return
	}
	fmt.Println("v1.0.0")
}
`

// probeBuildMarker is a string compiled into the probe; rewriting it in place
// changes the probe's bytes and leaves it runnable.
const probeBuildMarker = "probe-build-marker-a"

var probeOnce struct {
	sync.Once
	path string
	err  error
}

// probe builds probeSource once per test binary and returns a fresh copy of
// it at path.
func probe(t *testing.T, path string) string {
	t.Helper()
	if realTools["go"] == "" {
		t.Skip("no go to build the probe")
	}
	probeOnce.Do(func() {
		dir := filepath.Join(testScratch, "probe")
		if probeOnce.err = os.MkdirAll(dir, 0o755); probeOnce.err != nil {
			return
		}
		if probeOnce.err = os.WriteFile(filepath.Join(dir, "main.go"), []byte(probeSource), 0o644); probeOnce.err != nil {
			return
		}
		probeOnce.path = filepath.Join(dir, "probe")
		build := exec.Command(realTools["go"], "build", "-o", probeOnce.path, "main.go")
		build.Dir = dir
		// The environment TestMain started with: tests set HOME, which
		// would move the build cache.
		build.Env = append(slices.Clone(startEnvironment), "GOFLAGS=", "GOWORK=off", "GO111MODULE=off")
		if output, err := build.CombinedOutput(); err != nil {
			probeOnce.err = fmt.Errorf("building the probe: %v\n%s", err, output)
		}
	})
	if probeOnce.err != nil {
		t.Fatal(probeOnce.err)
	}
	copyFile(t, probeOnce.path, path)
	return path
}

// probeMarker is the offset of the marker compiled into a probe copy.
func probeMarker(t *testing.T, path string) int64 {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset := bytes.Index(content, []byte(probeBuildMarker))
	if offset < 0 {
		t.Fatalf("no marker in %s", path)
	}
	return int64(offset)
}

// rebuildProbe rewrites the marker compiled into a probe copy, in place,
// with a replacement of the same size.
func rebuildProbe(t *testing.T, path, replacement string) {
	t.Helper()
	if len(replacement) != len(probeBuildMarker) {
		t.Fatalf("replacement %q is not the marker's size", replacement)
	}
	rewriteInPlace(t, path, probeMarker(t, path), replacement)
}

// fingerprint is a tool's fingerprint as a fresh gate run computes it.
func fingerprint(t *testing.T, tool string) string {
	t.Helper()
	return fingerprintIn(t, t.TempDir(), tool)
}

// fingerprintIn is a tool's fingerprint as a fresh gate run over root
// computes it.
func fingerprintIn(t *testing.T, root, tool string) string {
	t.Helper()
	return (&gate{root: root, cacheDir: t.TempDir()}).toolVersion(tool)
}

func executable(t *testing.T, dir, name, content string) string {
	t.Helper()
	write(t, dir, name, content)
	path := filepath.Join(dir, name)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// rewriteInPlace overwrites bytes of a file without changing its size, its
// inode or anything but the bytes at offset.
func rewriteInPlace(t *testing.T, path string, offset int64, replacement string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt([]byte(replacement), offset); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	content, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

// link points PATH's entry for a tool at target, replacing any earlier link.
func link(t *testing.T, bin, name, target string) {
	t.Helper()
	path := filepath.Join(bin, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// A same-size, in-place rewrite within one filesystem timestamp tick keeps a
// file's size, inode, modification and change times on a coarse-clock
// filesystem (ext4 here: 199 of 200 attempts in review). The next fingerprint
// still sees it, because the gate hashes every installation on every run and
// keeps no memo a timestamp could vouch for. A memo keyed on (inode, size,
// change time) fails the first attempt; twenty keep the test cheap for Go's
// test cache, which re-stats every file a cached test opened.
//
// On darwin the kernel kills a signed binary whose pages no longer match its
// code signature, and once it has, it keeps killing that file after its bytes
// are restored; a new file with the same bytes runs (measured on darwin/arm64,
// go1.27.2: a copy that was rewritten and restored without running in between
// also runs). The fingerprint keys what the tool prints, so from the first
// rewrite on the probe answers "failed: ", and restoring the bytes restores
// the installation's part of the fingerprint, not the tool's answer.
func TestToolFingerprintSeesAnInPlaceRewriteWithinOneTimestampTick(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tool := probe(t, filepath.Join(t.TempDir(), "probe-tool"))
	link(t, bin, "probe-tool", tool)
	offset := probeMarker(t, tool)
	for attempt := range 20 {
		before := fingerprint(t, "probe-tool")
		rewriteInPlace(t, tool, offset, "probe-build-marker-b")
		after := fingerprint(t, "probe-tool")
		rewriteInPlace(t, tool, offset, probeBuildMarker)
		restored := fingerprint(t, "probe-tool")
		if after == before || restored == after {
			t.Fatalf("attempt %d: a same-size in-place rewrite kept the fingerprint", attempt)
		}
		if restored != before && !killedButRestored(before, restored) {
			t.Fatalf("attempt %d: restoring the bytes did not restore the fingerprint", attempt)
		}
	}
}

// killedButRestored reports whether, on darwin, restored is the fingerprint of
// the bytes before keyed but of a tool the kernel now kills.
func killedButRestored(before, restored string) bool {
	_, beforeBytes, _ := strings.Cut(before, "\x00file\x00")
	_, restoredBytes, _ := strings.Cut(restored, "\x00file\x00")
	return runtime.GOOS == "darwin" && strings.HasPrefix(restored, "failed: \x00") && beforeBytes != "" && restoredBytes == beforeBytes
}

// The same build under another prefix is another installation: a tool may
// load helpers relative to its path, as git does from its exec path. The path
// is keyed, but never in place of the bytes: returning to a path whose bytes
// changed meanwhile is a change.
func TestToolFingerprintKeysThePrefixAndTheBytes(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first := probe(t, filepath.Join(t.TempDir(), "probe-tool"))
	second := probe(t, filepath.Join(t.TempDir(), "probe-tool"))
	link(t, bin, "probe-tool", first)
	original := fingerprint(t, "probe-tool")
	if !strings.HasPrefix(original, "v1.0.0") || strings.Contains(original, "unlocated") || fingerprint(t, "probe-tool") != original {
		t.Fatalf("a binary has no stable fingerprint: %q", original)
	}
	link(t, bin, "probe-tool", second)
	moved := fingerprint(t, "probe-tool")
	if moved == original {
		t.Fatal("the same build under another prefix kept the fingerprint")
	}
	rebuildProbe(t, second, "probe-build-marker-b")
	if fingerprint(t, "probe-tool") == moved {
		t.Fatal("changing the bytes under an unchanged prefix kept the fingerprint")
	}
	rebuildProbe(t, first, "probe-build-marker-b")
	link(t, bin, "probe-tool", first)
	if fingerprint(t, "probe-tool") == original {
		t.Fatal("returning to a prefix whose bytes changed kept its old fingerprint")
	}
}

// A script can exec any file, and the gate cannot locate what it runs, so its
// fingerprint never matches a recorded pass.
func TestToolFingerprintOfAnUnlocatableLauncherNeverRepeats(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	executable(t, bin, "probe-tool", "#!/bin/sh\nexec /nonexistent/loop-probe-x \"$@\"\n")
	if fingerprint(t, "probe-tool") == fingerprint(t, "probe-tool") {
		t.Fatal("a launcher whose payload the gate cannot locate repeated its fingerprint")
	}
}

// go may be a launcher that switches toolchains; the GOROOT it reports holds
// the toolchain that runs, and every byte there is keyed.
func TestGoFingerprintKeysTheReportedToolchain(t *testing.T) {
	bin := t.TempDir()
	goroot := t.TempDir()
	write(t, goroot, "src/runtime/probe.go", "package runtime\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	executable(t, bin, "go", "#!/bin/sh\necho '{\"GOROOT\": \""+goroot+"\", \"GOVERSION\": \"go1.0.0\"}'\n")
	first := fingerprint(t, "go")
	if !strings.Contains(first, `"go1.0.0"`) || strings.Contains(first, "unlocated") || fingerprint(t, "go") != first {
		t.Fatalf("go has no stable fingerprint: %q", first)
	}
	rewriteInPlace(t, filepath.Join(goroot, "src/runtime/probe.go"), 0, "P")
	if fingerprint(t, "go") == first {
		t.Fatal("changing the reported toolchain's bytes kept the go fingerprint")
	}
}

// The real go, asked as the gate asks it, reports the GOROOT go env GOROOT
// prints. Only the subprocess opens files there, so Go's test cache logs none.
func TestRealGoEnvironmentReportsItsGoroot(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go")
	}
	query := exec.Command(realTools["go"], append([]string{"env", "-json"}, goEnvironmentNames...)...)
	query.Env = stepEnvironment()
	output, err := query.Output()
	if err != nil {
		t.Fatal(err)
	}
	goroot, err := reportedGoroot(output)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := exec.Command(realTools["go"], "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(goroot) || goroot != strings.TrimSpace(string(plain)) {
		t.Fatalf("go env -json reported GOROOT %q; go env GOROOT printed %q", goroot, plain)
	}
}

// git is an executable: its bytes are keyed, and so is its exec path, whose
// helpers git runs.
func TestExecutableFingerprintsKeyTheirInstallations(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if realTools["git"] == "" {
		t.Skip("no git")
	}
	link(t, bin, "git", realTools["git"])
	execPath := t.TempDir()
	write(t, execPath, "git-probe", "#!/bin/sh\n")
	t.Setenv("GIT_EXEC_PATH", execPath)
	first := fingerprint(t, "git")
	if strings.Contains(first, "unlocated") || !strings.Contains(first, execPath) {
		t.Fatalf("git's fingerprint does not key its exec path: %q", first)
	}
	rewriteInPlace(t, filepath.Join(execPath, "git-probe"), 3, "c")
	if fingerprint(t, "git") == first {
		t.Fatal("changing a helper in git's exec path kept the fingerprint")
	}
}

// A tool that does not answer a fingerprint's question in time keys as
// unlocated rather than holding the gate. Tools that fork a pipe holder,
// and a gate under nohup, are tested on Linux (tools_linux_test.go).
func TestToolFingerprintOfAHungToolNeverRepeats(t *testing.T) {
	shortQueryTimeout(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tool := probe(t, filepath.Join(t.TempDir(), "probe-tool"))
	write(t, filepath.Dir(tool), "probe-tool.hang", "")
	link(t, bin, "probe-tool", tool)
	assertHungToolUnlocated(t)
}

// shortQueryTimeout makes a fingerprint's tool queries time out after 200 ms
// for the test.
func shortQueryTimeout(t *testing.T) {
	previous := toolQueryTimeout
	toolQueryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { toolQueryTimeout = previous })
}

// assertHungToolUnlocated fingerprints probe-tool twice: each must return
// in well under the tool's 30 s hang, be unlocated and differ.
func assertHungToolUnlocated(t *testing.T) {
	t.Helper()
	start := time.Now()
	first := fingerprint(t, "probe-tool")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a hung tool held its fingerprint for %v", elapsed)
	}
	if !strings.Contains(first, "unlocated") || fingerprint(t, "probe-tool") == first {
		t.Fatalf("a hung tool repeated its fingerprint: %q", first)
	}
}

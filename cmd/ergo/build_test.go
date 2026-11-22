package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// goStoreVariable names a Go action store (the direct path's content-keyed
// plans, actions and blobs) that this process's emission caches use in
// place of their own: TestPackageNeverWritesUnderHome gives its inner run
// the outer run's store, which lies outside the HOME it watches.
const goStoreVariable = "ERGO_CMD_TEST_GO_STORE"

// processCache is the emission cache root (XDG_CACHE_HOME) of this test
// process (init).
var processCache string

// testXDG is a test's own emission cache root, parent/xdg, for a test that
// asserts on its cache: its modules, staging and private roots are its
// own, and its Go action store (ergo/go: the direct path's plans, actions
// and blobs, internal/sketch/build/direct.go) is the process's, linked in.
// That store is keyed by content (each compile by its inputs, each link
// by its archives), so a test that shares it builds the same bytes and
// compiles the runtime's packages once per process, not once per test
// (GS26: 4 CPU-s per cold program build).
func testXDG(t *testing.T, parent string) string {
	t.Helper()
	xdg := filepath.Join(parent, "xdg")
	store := filepath.Join(processCache, "ergo", "go")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(xdg, "ergo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(xdg, "ergo", "go")); err != nil {
		t.Fatal(err)
	}
	return xdg
}

// TestTestCachesShareTheGoStore counts the compiles of two builds in two
// tests' emission caches (testXDG): the second program differs from the
// first only in its main package, so it compiles that package alone and
// reuses the runtime's from the shared store. Without the share each test
// compiles the runtime again (GS26).
func TestTestCachesShareTheGoStore(t *testing.T) {
	t.Parallel()
	binary := buildTestCLI(t)
	report := regexp.MustCompile(`LINK=direct \(planned \w+, compiled (\d+), reused (\d+),`)
	var compiled, reused [2]string
	for i := range 2 {
		root := physicalTempDir(t)
		xdg := testXDG(t, t.TempDir())
		// The text names this process: TestPackageNeverWritesUnderHome's inner
		// run runs this test too, on the same store, so a fixed text would
		// find its main compiled already.
		smokeWrite(t, root, "main.ergo", "func main() needs Console { Console.Println(\"program "+string(rune('a'+i))+" "+strconv.Itoa(os.Getpid())+"\") }\n")
		// Without the caller's GOFLAGS (the gate sets -p): the direct path
		// it counts refuses to build under any.
		command := exec.Command(binary, "build", "main.ergo", "-o", "app", "-work")
		command.Dir = root
		command.Env = append(slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "GOFLAGS=") }), "XDG_CACHE_HOME="+xdg)
		out, errOut, code := runSmokeCommand(t, command)
		stdout, stderr := string(out), string(errOut)
		m := report.FindStringSubmatch(stderr)
		if code != 0 || m == nil {
			t.Fatalf("build %d: exit %d stdout %q stderr %q", i, code, stdout, stderr)
		}
		compiled[i], reused[i] = m[1], m[2]
	}
	if compiled[1] != "1" || reused[1] == "0" {
		t.Fatalf("the second test's build compiled %s packages and reused %s (the first compiled %s); want only its main compiled", compiled[1], reused[1], compiled[0])
	}
}

// innerRunVariable marks the package run that
// TestPackageNeverWritesUnderHome starts, which must not start another.
const innerRunVariable = "ERGO_CMD_TEST_INNER_RUN"

// TestPackageNeverWritesUnderHome runs this package's tests again, in this
// run's tier (-short when it is), with
// HOME set to an empty directory and XDG_CACHE_HOME unset, and requires the
// directory to stay empty: building and running programs, in every test,
// writes only into the tests' own temporary space. Go's caches and go env
// file keep the locations this process resolved, and the go command runs
// with a HOME and config directory of its own (goOwnHome), so the run
// measures Ergo's writes, not the go command's telemetry; Go's telemetry
// mode is off in the witnessed HOME (goTelemetryOff), so the compile and
// link that Ergo runs itself count nothing there either. The inner run's temporary directory is
// reached through a symbolic link, with GOTMPDIR unset, so its tests see
// a logical path that differs from the physical one, as on darwin. Its
// emission caches use this process's Go action store (goStoreVariable),
// which lies outside the witnessed HOME, so its builds may be cached ones;
// a write under HOME that only a cached build makes is still witnessed
// (mutant gs26b-cached-build-writes-home).
//
// HOME is checked whatever the inner run's exit status: a failing test
// there is reported by this package's own run and must not hide a write
// under HOME. The inner run fails this test only when it ran no test.
func TestPackageNeverWritesUnderHome(t *testing.T) {
	if os.Getenv(innerRunVariable) != "" {
		t.Skip("inside the run this test started")
	}
	home := t.TempDir()
	telemetryOff := goTelemetryOff(t, home)
	temp := t.TempDir()
	linkedTemp := filepath.Join(t.TempDir(), "linked-tmp")
	if err := os.Symlink(temp, linkedTemp); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV").Output()
	if err != nil {
		t.Fatal(err)
	}
	var goEnv map[string]string
	if err := json.Unmarshal(out, &goEnv); err != nil {
		t.Fatal(err)
	}
	wrapperDir := goOwnHome(t)
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV", "TMPDIR", "GOTMPDIR", "PATH"}, name)
	})
	env = append(env, "HOME="+home, innerRunVariable+"=1", "TMPDIR="+linkedTemp,
		"PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOCACHE="+goEnv["GOCACHE"], "GOMODCACHE="+goEnv["GOMODCACHE"], "GOPATH="+goEnv["GOPATH"], "GOENV="+goEnv["GOENV"])
	// The inner run's emission caches use this process's Go action store,
	// outside the witnessed HOME: the claim is where the package writes, not
	// that it builds from cold, and its cached builds still run every write
	// a build makes outside that store (GS26b).
	store, err := filepath.EvalSymlinks(filepath.Join(processCache, "ergo", "go"))
	if err != nil {
		if err := os.MkdirAll(filepath.Join(processCache, "ergo", "go"), 0o755); err != nil {
			t.Fatal(err)
		}
		store = filepath.Join(processCache, "ergo", "go")
	}
	env = append(env, goStoreVariable+"="+store)
	args := []string{"-test.count=1", "-test.v", "-test.skip=^TestPackageNeverWritesUnderHome$"}
	if testing.Short() {
		// The witness covers the tests of this run's tier: a -short run
		// starts a -short package run, not the full one (OPT9 r2).
		args = append(args, "-test.short")
	}
	child := exec.Command(os.Args[0], args...)
	child.Env = env
	stdout, stderr, code := runSmokeCommand(t, child)
	passed := regexp.MustCompile(`(?m)^--- PASS: `).FindAll(stdout, -1)
	failed := regexp.MustCompile(`(?m)^--- FAIL: \S+`).FindAll(stdout, -1)
	switch {
	case len(passed) == 0:
		t.Errorf("the package run with an empty HOME ran no test: exit %d\n%s\n%s", code, firstLines(stdout), firstLines(stderr))
	case code != 0:
		t.Logf("the package run with an empty HOME exited %d after %d passing tests; failing: %s", code, len(passed), bytes.Join(failed, []byte(", ")))
	}
	// The builds run the release's go command by its path, the patched
	// GOROOT's bin/go (internal/toolchain, Patch), which the wrapper on
	// PATH does not reach: that go command's telemetry directory, and the
	// directories above it, are the go command's writes, not Ergo's.
	telemetry := filepath.Join(home, ".config", "go", "telemetry")
	if runtime.GOOS == "darwin" {
		telemetry = filepath.Join(home, "Library", "Application Support", "go", "telemetry")
	}
	var written []string
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if p == telemetry {
			return filepath.SkipDir
		}
		if p != home && !slices.Contains(telemetryOff, p) && !strings.HasPrefix(telemetry, p+string(filepath.Separator)) {
			written = append(written, p)
		}
		return nil
	})
	if len(written) != 0 {
		t.Fatalf("the package's tests wrote under HOME: %v", written[:min(len(written), 20)])
	}
}

// goTelemetryOff turns Go's telemetry off in the witnessed HOME, as `go
// telemetry off` does: it writes "off" to the mode file of Go's telemetry
// directory, os.UserConfigDir()/go/telemetry/mode, which is
// $HOME/.config/go/telemetry/mode on Linux (the inner run has no
// XDG_CONFIG_HOME) and $HOME/Library/Application Support/go/telemetry/mode
// on darwin. It returns the paths it made, which the witness admits.
//
// goOwnHome keeps the go command's own state out of the witnessed HOME,
// but Ergo's direct build path (internal/sketch/build, direct.go) runs the
// toolchain's compile and link itself, with Ergo's HOME, as a user's build
// does; with no mode file they count into
// $HOME/.config/go/telemetry/local (the batch-18 head failed so). With
// the mode off they write nothing (x/telemetry: counter.Open does not open
// a file in mode off). The go command reads no GOTELEMETRY variable: its
// mode is this file (cmd/go/internal/telemetrycmd), so the environment
// cannot turn it off.
func goTelemetryOff(t *testing.T, home string) []string {
	t.Helper()
	config := filepath.Join(home, ".config")
	made := []string{config}
	if runtime.GOOS == "darwin" {
		made = []string{filepath.Join(home, "Library"), filepath.Join(home, "Library", "Application Support")}
		config = made[1]
	}
	made = append(made, filepath.Join(config, "go"), filepath.Join(config, "go", "telemetry"))
	if err := os.MkdirAll(made[len(made)-1], 0o755); err != nil {
		t.Fatal(err)
	}
	mode := filepath.Join(made[len(made)-1], "mode")
	if err := os.WriteFile(mode, []byte("off"), 0o644); err != nil {
		t.Fatal(err)
	}
	return append(made, mode)
}

// goOwnHome returns a directory holding a `go` that runs the real go
// command with its own HOME and XDG_CONFIG_HOME, both temporary
// directories outside the HOME the witness checks. The go command keeps
// state under os.UserConfigDir()/go/telemetry
// (cmd/vendor/golang.org/x/telemetry/internal/telemetry/dir.go, init), and
// GOTELEMETRYDIR is a non-settable go env value
// (cmd/go/internal/telemetrycmd/telemetry.go). os.UserConfigDir is
// $XDG_CONFIG_HOME or $HOME/.config on Linux, but $HOME/Library/Application
// Support on darwin, which ignores XDG_CONFIG_HOME (os/file.go). Release
// toolchains also read TEST_TELEMETRY_DIR
// (cmd/internal/telemetry/counter/counter.go), but that is an undocumented
// test hook, so this witness does not rely on it: a HOME of its own keeps
// the go command's writes out of the witnessed HOME on every platform
// through documented behaviour. Ergo's processes keep the witnessed HOME, so Ergo's
// own writes are still caught; GOCACHE, GOMODCACHE, GOPATH and GOENV are
// pinned by the caller to the values this process resolved.
func goOwnHome(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the go wrapper is a shell script")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	if realGo, err = filepath.Abs(realGo); err != nil {
		t.Fatal(err)
	}
	goHome := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := "#!/bin/sh\nHOME=" + quote(goHome) + " XDG_CONFIG_HOME=" + quote(filepath.Join(goHome, "config")) +
		" exec " + quote(realGo) + " \"$@\"\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuildWithoutAProducerImageIsPrivate: a compiler whose producer
// identity is process-scoped (as off Linux and darwin/arm64, or after a
// failed darwin verification) builds each module in a private root of its
// process and publishes nothing, so no other process can take its output
// under a key only it can trust. Each build still produces a program that
// runs.
func TestBuildWithoutAProducerImageIsPrivate(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := smokeDir(t)
	ownerPath := filepath.Join(root, "internal", "producer", "identity.go")
	unavailable := producerUnavailableSource(t, producerSmokeRead(t, ownerPath))
	binary := producerSmokeOverlays(t, root, directory, ownerPath, map[string]string{"unavailable": unavailable})["unavailable"]
	if err := os.WriteFile(filepath.Join(directory, "main.ergo"), []byte(sketchCleanSource), 0o644); err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(t.TempDir(), "xdg")
	private := filepath.Join(xdg, "ergo", "private") + string(filepath.Separator)
	var works []string
	for range 2 {
		stdout, stderr, code := cachedCLI(t, binary, directory, xdg, "build", "main.ergo", "-o", "app", "-work")
		if code != 0 {
			t.Fatalf("build: exit %d %s\n%s", code, stdout, stderr)
		}
		work := workDir(t, stderr)
		if !strings.HasPrefix(work, private) {
			t.Fatalf("WORK=%s, want a module under %s", work, private)
		}
		works = append(works, work)
		if out, err := exec.Command(filepath.Join(directory, "app")).Output(); err != nil || string(out) != "ok\n" {
			t.Fatalf("the built program printed %q: %v", out, err)
		}
	}
	// One private root per process: modules/KEY sits two levels below it.
	if filepath.Dir(filepath.Dir(works[0])) == filepath.Dir(filepath.Dir(works[1])) {
		t.Fatalf("two processes shared the private root of %s and %s", works[0], works[1])
	}
	if entries, err := os.ReadDir(filepath.Join(xdg, "ergo", "modules")); !errors.Is(err, fs.ErrNotExist) && (err != nil || len(entries) != 0) {
		t.Fatalf("a process-scoped compiler published %v (%v)", entries, err)
	}
}

// cachedCLI runs the CLI in directory with its emission cache under
// xdg/ergo, and returns stdout, stderr and the exit code.
func cachedCLI(t *testing.T, binary, directory, xdg string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg)
	stdout, stderr, code := runSmokeCommand(t, command)
	return string(stdout), string(stderr), code
}

var workLine = regexp.MustCompile(`(?m)^WORK=(.*)$`)

// workDir is the one WORK= line of stderr, which must name an absolute
// directory.
func workDir(t *testing.T, stderr string) string {
	t.Helper()
	found := workLine.FindAllStringSubmatch(stderr, -1)
	if len(found) != 1 || !filepath.IsAbs(found[0][1]) {
		t.Fatalf("stderr %q, want exactly one WORK=<absolute directory> line", stderr)
	}
	if info, err := os.Stat(found[0][1]); err != nil || !info.IsDir() {
		t.Fatalf("WORK=%s is not a directory: %v", found[0][1], err)
	}
	return found[0][1]
}

// moduleFile is a module file's digest, metadata and identity.
type moduleFile struct {
	digest [32]byte
	info   fs.FileInfo
}

// moduleState records every file of a module.
func moduleState(t *testing.T, dir string) map[string]moduleFile {
	t.Helper()
	state := map[string]moduleFile{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		state[p] = moduleFile{digest: sha256.Sum256(data), info: info}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// physicalTempDir is a test temporary directory with its links resolved.
func physicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// lineDirective matches a //line directive, path:line or path:line:column,
// and captures its path.
var lineDirective = regexp.MustCompile(`(?m)^//line (.+?):\d+(?::\d+)?$`)

// TestBuildWorkMapsASymlinkedSource: when the source is reached through a
// symbolic link to its directory, by a relative spelling from the linked
// directory (with and without a shell's PWD) or by an absolute spelling
// through the link, the //line directives of the published module name an
// relative path that, from the source's root, is the source file itself
// (os.SameFile), and the program runs. The
// links are made here, so the case runs on Linux whatever TMPDIR is.
func TestBuildWorkMapsASymlinkedSource(t *testing.T) {
	t.Parallel()
	binary := buildTestCLI(t)
	base := physicalTempDir(t)
	real := filepath.Join(base, "real-src")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(real, "main.ergo")
	if err := os.WriteFile(source, []byte(sketchCleanSource), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-src")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	xdg := testXDG(t, physicalTempDir(t))
	for _, c := range []struct {
		name, dir, spelling string
		env                 []string
	}{
		{"relative from the link, PWD inherited", link, "main.ergo", nil},
		{"relative from the link, PWD set as a shell does", link, "main.ergo", []string{"PWD=" + link}},
		{"absolute through the link", base, filepath.Join(link, "main.ergo"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			output := filepath.Join(physicalTempDir(t), "app")
			command := exec.Command(binary, "build", c.spelling, "-o", output, "-work")
			command.Dir = c.dir
			command.Env = append(append(os.Environ(), "XDG_CACHE_HOME="+xdg), c.env...)
			stdout, stderr, code := runSmokeCommand(t, command)
			if code != 0 {
				t.Fatalf("build: exit %d %s\n%s", code, stdout, stderr)
			}
			mainGo, err := os.ReadFile(filepath.Join(workDir(t, string(stderr)), "main.go"))
			if err != nil {
				t.Fatal(err)
			}
			found := lineDirective.FindSubmatch(mainGo)
			if found == nil {
				t.Fatalf("main.go has no //line directive:\n%s", mainGo)
			}
			target := string(found[1])
			targetInfo, err := os.Stat(filepath.Join(real, target))
			if err != nil || filepath.IsAbs(target) || !os.SameFile(targetInfo, sourceInfo) {
				t.Fatalf("//line names %s (%v), which is not the source %s", target, err, source)
			}
			if out, err := exec.Command(output).Output(); err != nil || string(out) != "ok\n" {
				t.Fatalf("app printed %q (%v)", out, err)
			}
		})
	}
}

// TestBuildWorkNamesTheCachedModule: build -work prints the executable on
// stdout and exactly one WORK= line on stderr naming the published module
// under $XDG_CACHE_HOME/ergo, whose //line directives name the source
// relative to its root. An unchanged build names the same module and rewrites none
// of its files; an edit names another module and changes the executable's
// behavior; a build without -work prints no WORK line; and only build
// takes -work. Spec D7, the cached build.
func TestBuildWorkNamesTheCachedModule(t *testing.T) {
	t.Parallel()
	binary := buildTestCLI(t)
	// The CLI admits the path it walks, which is physical when the
	// temporary directory is reached through a link (darwin's /var).
	root := physicalTempDir(t)
	xdg := testXDG(t, t.TempDir())
	source := filepath.Join(root, "main.ergo")
	write := func(word string) {
		text := "func main() needs Console {\n\tConsole.Println(\"" + word + "\")\n}\n"
		if err := os.WriteFile(source, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(want string) string {
		t.Helper()
		stdout, stderr, code := cachedCLI(t, binary, root, xdg, "build", "main.ergo", "-o", "app", "-work")
		if code != 0 || stdout != "app\n" {
			t.Fatalf("build -work: exit %d stdout %q\n%s", code, stdout, stderr)
		}
		work := workDir(t, stderr)
		if filepath.Dir(work) != filepath.Join(xdg, "ergo", "modules") {
			t.Fatalf("WORK=%s is not a module of the cache under %s", work, xdg)
		}
		out, err := exec.Command(filepath.Join(root, "app")).Output()
		if err != nil || string(out) != want {
			t.Fatalf("app printed %q (%v), want %q", out, err, want)
		}
		return work
	}
	write("one")
	cold := build("one\n")
	mainGo, err := os.ReadFile(filepath.Join(cold, "main.go"))
	if err != nil || !strings.Contains(string(mainGo), "//line main.ergo:") {
		t.Fatalf("main.go of %s does not map to %s (%v)", cold, source, err)
	}
	before := moduleState(t, cold)
	if warm := build("one\n"); warm != cold {
		t.Fatalf("unchanged build named %s, want %s", warm, cold)
	}
	after := moduleState(t, cold)
	if len(after) != len(before) {
		t.Fatalf("unchanged build changed the module's files")
	}
	for p, s := range before {
		a, ok := after[p]
		if !ok || a.digest != s.digest || !os.SameFile(a.info, s.info) || !a.info.ModTime().Equal(s.info.ModTime()) {
			t.Fatalf("unchanged build rewrote %s", p)
		}
	}
	write("two")
	if edited := build("two\n"); edited == cold {
		t.Fatal("an edited source reused the module")
	}
	if _, stderr, code := cachedCLI(t, binary, root, xdg, "build", "main.ergo", "-o", "app"); code != 0 || workLine.MatchString(stderr) {
		t.Fatalf("build without -work: exit %d stderr %q", code, stderr)
	}
	for _, command := range []string{"run", "check", "diagnostics"} {
		stdout, stderr, code := cachedCLI(t, binary, root, xdg, command, "-work", "main.ergo")
		if code == 0 || stdout != "" || !strings.Contains(stderr, "-work is only supported by build") {
			t.Fatalf("ergo %s -work: exit %d stdout %q stderr %q", command, code, stdout, stderr)
		}
	}
}

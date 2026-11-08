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
	"strings"
	"testing"
)

// init gives every CLI this package's tests start a private emission cache:
// it runs before TestMain captures the environment, so no test writes the
// developer's $XDG_CACHE_HOME or $HOME/.cache. The directory is not
// removed (follow-up: move this into TestMain, with cleanup, once
// main_test.go is this unit's to edit).
func init() {
	dir, err := os.MkdirTemp("", "ergo-cmd-test-cache-")
	if err != nil {
		panic("cmd/ergo tests: private cache directory: " + err.Error())
	}
	if err := os.Setenv("XDG_CACHE_HOME", dir); err != nil {
		panic(err)
	}
}

// innerRunVariable marks the package run that
// TestPackageNeverWritesUnderHome starts, which must not start another.
const innerRunVariable = "ERGO_CMD_TEST_INNER_RUN"

// TestPackageNeverWritesUnderHome runs this package's tests again with
// HOME set to an empty directory and XDG_CACHE_HOME unset, and requires the
// directory to stay empty: building and running programs, in every test,
// writes only into the tests' own temporary space. Go's caches and go env
// file keep the locations this process resolved, and the go command runs
// with a HOME and config directory of its own (goOwnHome), so the run
// measures Ergo's writes, not the go command's telemetry. The inner run's temporary directory is
// reached through a symbolic link, with GOTMPDIR unset, so its tests see
// a logical path that differs from the physical one, as on darwin.
//
// HOME is checked whatever the inner run's exit status: a failing test
// there is reported by this package's own run and must not hide a write
// under HOME. The inner run fails this test only when it ran no test.
func TestPackageNeverWritesUnderHome(t *testing.T) {
	if os.Getenv(innerRunVariable) != "" {
		t.Skip("inside the run this test started")
	}
	home := t.TempDir()
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
	child := exec.Command(os.Args[0], "-test.count=1", "-test.v", "-test.skip=^TestPackageNeverWritesUnderHome$")
	child.Env = env
	stdout, stderr, code := runSmokeCommand(t, 10*processDeadline, child)
	passed := regexp.MustCompile(`(?m)^--- PASS: `).FindAll(stdout, -1)
	failed := regexp.MustCompile(`(?m)^--- FAIL: \S+`).FindAll(stdout, -1)
	switch {
	case len(passed) == 0:
		t.Errorf("the package run with an empty HOME ran no test: exit %d\n%s\n%s", code, firstLines(stdout), firstLines(stderr))
	case code != 0:
		t.Logf("the package run with an empty HOME exited %d after %d passing tests; failing: %s", code, len(passed), bytes.Join(failed, []byte(", ")))
	}
	var written []string
	_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if p != home {
			written = append(written, p)
		}
		return nil
	})
	if len(written) != 0 {
		t.Fatalf("the package's tests wrote under HOME: %v", written[:min(len(written), 20)])
	}
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
	stdout, stderr, code := runSmokeCommand(t, processDeadline, command)
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
// absolute path that is the source file itself (os.SameFile), and the
// program runs. Which spelling the directive uses follows
// receipt.LocateSource; the property is that it opens the source. The
// links are made here, so the case runs on Linux whatever TMPDIR is.
func TestBuildWorkMapsASymlinkedSource(t *testing.T) {
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
	xdg := filepath.Join(physicalTempDir(t), "xdg")
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
			stdout, stderr, code := runSmokeCommand(t, processDeadline, command)
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
			targetInfo, err := os.Stat(target)
			if err != nil || !filepath.IsAbs(target) || !os.SameFile(targetInfo, sourceInfo) {
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
// under $XDG_CACHE_HOME/ergo, whose //line directives name the admitted
// source path. An unchanged build names the same module and rewrites none
// of its files; an edit names another module and changes the executable's
// behavior; a build without -work prints no WORK line; and only build
// takes -work. Spec D7, #224.
func TestBuildWorkNamesTheCachedModule(t *testing.T) {
	binary := buildTestCLI(t)
	// The CLI admits the path it walks, which is physical when the
	// temporary directory is reached through a link (darwin's /var).
	root := physicalTempDir(t)
	xdg := filepath.Join(t.TempDir(), "xdg")
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
	if err != nil || !strings.Contains(string(mainGo), "//line "+source+":") {
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

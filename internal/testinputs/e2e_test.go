//go:build linux && (amd64 || arm64)

package testinputs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/testinputs/testenv"
)

// The end-to-end cases run Go's real test cache over a fixture module whose
// test starts a process that reads a file (or variable) the test process
// itself never touches. Each case primes the cache, checks that an unchanged
// rerun is (cached), edits what the subprocess read, and requires a rerun
// that sees the edit. A control package runs the same test with a plain
// TestMain, as every test package did before Main: it must replay its stale
// result after the edit, which proves the case is a real counterexample to
// caching on the test process's own reads.
//
// The cases are the counterexamples of review rounds 1 to 4 of the declared
// (modelled) design that observation replaces, plus a subprocess started by
// production code, a variable change, names that need no quoting and the
// fail-closed paths. The rounds found them with Bun, Node and tsc; since
// 2026-10-26 Go is Ergo's only target, so each case whose mechanism is not
// specific to a JavaScript resolver runs on sh, cat or go instead, and the
// resolver-specific ones (bunfig preloads, tsconfig paths, package exports,
// bunx and npx) are gone with the tools.

type e2eCase struct {
	name  string
	tools []string          // skip unless each is on PATH
	files map[string]string // fixture files; "{{root}}" is the fixture root
	exec  []string          // files to make executable
	links map[string]string // symlinks: name to target
	env   []string          // variables for go test; "{{root}}" is the fixture root
	// imports and run make the probe: run's body returns the output of what
	// the test starts. Helpers: root() is the fixture root, command(name,
	// args...) runs a program there and returns its combined output.
	imports []string
	run     string
	setup   func(t *testing.T, f *fixture)
	before  string // the output before the edit contains this
	edit    func(t *testing.T, f *fixture)
	editEnv []string // replaces env for the runs after the edit
	want    string   // the output after the edit contains this
	// unobserved cases are never cached: the probe reports why, and the
	// edit step is skipped.
	unobserved string
	// wrap runs go test under this fixture-relative program, built first.
	wrap string
	// gateEnvironment runs go test with only the variables the gate passes
	// a Go test step (testenv.Environment).
	gateEnvironment bool
	// consistent replaces the rerun check after the edit: whether or not the
	// probe reruns, its output must equal a fresh (-count=1) run's.
	consistent bool
}

var e2eCases = []e2eCase{
	{
		name:  "script and the file it sources",
		tools: []string{"sh"},
		files: map[string]string{
			"scripts/compare.sh": ". ./scripts/codec.sh\necho \"$codec\"\n",
			"scripts/codec.sh":   "codec=codec-1\n",
		},
		run:    `return command("sh", "scripts/compare.sh")`,
		before: "codec-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "scripts/codec.sh", "codec=codec-2\n") },
		want:   "codec-2",
	},
	{
		name:  "R1-M1 transitively sourced file edited",
		tools: []string{"sh"},
		files: map[string]string{
			"src/main.sh": ". ./src/a.sh\necho \"$v\"\n",
			"src/a.sh":    ". ./src/b.sh\n",
			"src/b.sh":    "v=b-1\n",
		},
		run:    `return command("sh", "src/main.sh")`,
		before: "b-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "src/b.sh", "v=b-2\n") },
		want:   "b-2",
	},
	{
		name:   "R1-m3 symlink target edited",
		tools:  []string{"cat"},
		files:  map[string]string{"real/target.txt": "target-1\n"},
		links:  map[string]string{"src/link.txt": "../real/target.txt"},
		run:    `return command("cat", "src/link.txt")`,
		before: "target-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "real/target.txt", "target-2\n") },
		want:   "target-2",
	},
	{
		name:  "repository file read through a link from a temporary directory",
		tools: []string{"cat"},
		files: map[string]string{"data/value.txt": "linked-1\n"},
		run: `dir, err := os.MkdirTemp("", "link-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	link := filepath.Join(dir, "value.txt")
	if err := os.Symlink(filepath.Join(root(), "data", "value.txt"), link); err != nil {
		return nil, err
	}
	return command("cat", link)`,
		before: "linked-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "data/value.txt", "linked-2\n") },
		want:   "linked-2",
	},
	{
		name:  "R3-1 shebang script run directly",
		tools: []string{"sh"},
		files: map[string]string{
			"scripts/run.sh": "#!/bin/sh\n. \"$(dirname \"$0\")/dep.sh\"\necho \"$v\"\n",
			"scripts/dep.sh": "v=shebang-1\n",
		},
		exec:   []string{"scripts/run.sh"},
		run:    `return command(root() + "/scripts/run.sh")`,
		before: "shebang-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "scripts/dep.sh", "v=shebang-2\n") },
		want:   "shebang-2",
	},
	{
		name:   "R3-1 env cat",
		tools:  []string{"env", "cat"},
		files:  map[string]string{"main.txt": "env-1\n"},
		run:    `return command("env", "cat", "main.txt")`,
		before: "env-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "main.txt", "env-2\n") },
		want:   "env-2",
	},
	{
		name:   "R3-1 sh -c cat",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"main.txt": "sh-1\n"},
		run:    `return command("sh", "-c", "cat main.txt")`,
		before: "sh-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "main.txt", "sh-2\n") },
		want:   "sh-2",
	},
	{
		name:  "R3-2 function values, method expressions and opaque results",
		tools: []string{"cat"},
		files: map[string]string{
			"lib/lib.go": `package lib

import "os/exec"

type Runner struct{ Tool string }

func (r Runner) Command(args ...string) *exec.Cmd { return exec.Command(r.Tool, args...) }

var Make = func(args ...string) *exec.Cmd { return Runner{Tool: "cat"}.Command(args...) }

func Opaque() any { return Make }
`,
			"a.txt": "method-1\n",
			"b.txt": "opaque-1\n",
		},
		imports: []string{"fixture/lib"},
		run: `method := lib.Runner.Command(lib.Runner{Tool: "cat"}, "a.txt")
	method.Dir = root()
	a, err := method.CombinedOutput()
	if err != nil {
		return a, err
	}
	opaque := lib.Opaque().(func(...string) *exec.Cmd)("b.txt")
	opaque.Dir = root()
	b, err := opaque.CombinedOutput()
	return append(a, b...), err`,
		before: "method-1 | opaque-1",
		edit: func(t *testing.T, f *fixture) {
			f.write(t, "a.txt", "method-2\n")
			f.write(t, "b.txt", "opaque-2\n")
		},
		want: "method-2 | opaque-2",
	},
	{
		name:  "production code starts the process",
		tools: []string{"sh"},
		files: map[string]string{
			"lib/version.go": `package lib

import "os/exec"

// Version reports the fixture's version, as production code that shells
// out would.
func Version(root string) ([]byte, error) {
	command := exec.Command("sh", "scripts/version.sh")
	command.Dir = root
	return command.CombinedOutput()
}
`,
			"scripts/version.sh": "echo version-1\n",
		},
		imports: []string{"fixture/lib"},
		run:     `return lib.Version(root())`,
		before:  "version-1",
		edit:    func(t *testing.T, f *fixture) { f.write(t, "scripts/version.sh", "echo version-2\n") },
		want:    "version-2",
	},
	{
		name:   "R3-3 file added to a globbed directory",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"src/a.txt": "listed-a\n"},
		run:    `return command("sh", "-c", "cat src/*.txt")`,
		before: "listed-a",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "src/b.txt", "listed-b\n") },
		want:   "listed-a | listed-b",
	},
	{
		name:   "R4-1 inherited variable names the file read",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"pre.txt": "named-1\n"},
		env:    []string{"FIXTURE_FILE={{root}}/pre.txt"},
		run:    `return command("sh", "-c", "cat \"$FIXTURE_FILE\"")`,
		before: "named-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "pre.txt", "named-2\n") },
		want:   "named-2",
	},
	{
		name:  "R4-2 sh wrapper in a temporary directory",
		tools: []string{"sh", "cat"},
		files: map[string]string{"main.txt": "wrapper-1\n"},
		run: `dir, err := os.MkdirTemp("", "wrapper-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wrapper := filepath.Join(dir, "show")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec cat \"$@\"\n"), 0o755); err != nil {
		return nil, err
	}
	return command(wrapper, "main.txt")`,
		before: "wrapper-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "main.txt", "wrapper-2\n") },
		want:   "wrapper-2",
	},
	{
		name:  "R4-2 renamed cat",
		tools: []string{"cat"},
		files: map[string]string{"main.txt": "renamed-1\n"},
		run: `cat, err := exec.LookPath("cat")
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "renamed-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	renamed := filepath.Join(dir, "show")
	if err := os.Symlink(cat, renamed); err != nil {
		return nil, err
	}
	return command(renamed, "main.txt")`,
		before: "renamed-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "main.txt", "renamed-2\n") },
		want:   "renamed-2",
	},
	{
		name:  "R4-4 go generate runs cat",
		tools: []string{"cat", "go"},
		files: map[string]string{
			"gen/gen.go":  "package gen\n\n//go:generate cat gen.txt\n",
			"gen/gen.txt": "generate-1\n",
		},
		run:    `return command("go", "generate", "./gen")`,
		before: "generate-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "gen/gen.txt", "generate-2\n") },
		want:   "generate-2",
	},
	{
		name:   "R4-5 missing file added",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"src/keep.txt": "keep\n"},
		run:    `return command("sh", "-c", "cat src/later.txt 2>/dev/null || echo none")`,
		before: "none",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "src/later.txt", "later\n") },
		want:   "later",
	},
	{
		name:    "R4-8 JSON-decoded exec.Cmd",
		tools:   []string{"cat"},
		files:   map[string]string{"main.txt": "decoded-1\n"},
		imports: []string{"encoding/json"},
		run: `cat, err := exec.LookPath("cat")
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(map[string]any{"Path": cat, "Args": []string{"cat", "main.txt"}, "Dir": root()})
	if err != nil {
		return nil, err
	}
	var decoded exec.Cmd
	if err := json.Unmarshal(spec, &decoded); err != nil {
		return nil, err
	}
	return decoded.CombinedOutput()`,
		before: "decoded-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "main.txt", "decoded-2\n") },
		want:   "decoded-2",
	},
	{
		name:  "R4-m1 git config",
		tools: []string{"git"},
		env:   []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
		run:   `return command("git", "config", "--get", "fixture.value")`,
		setup: func(t *testing.T, f *fixture) {
			f.git(t, "init", "-q")
			f.git(t, "config", "fixture.value", "config-1")
			f.backdateTree(t, ".git", f.base)
		},
		before: "config-1",
		edit: func(t *testing.T, f *fixture) {
			f.git(t, "config", "fixture.value", "config-2")
			f.backdateTree(t, ".git", f.edited)
		},
		want: "config-2",
	},
	{
		name:    "variable passed to a process changes",
		tools:   []string{"sh"},
		env:     []string{"FIXTURE_VALUE=value-1"},
		run:     `return command("sh", "-c", "echo \"$FIXTURE_VALUE\"")`,
		before:  "value-1",
		edit:    func(t *testing.T, f *fixture) {},
		editEnv: []string{"FIXTURE_VALUE=value-2"},
		want:    "value-2",
	},
	{
		name:  "names that need quoting elsewhere",
		tools: []string{"cat"},
		files: map[string]string{
			"odd dir/it's \"q\" \\ $x;|&\xff.txt": "odd-1\n",
		},
		run:    `return command("cat", "odd dir/it's \"q\" \\ $x;|&\xff.txt")`,
		before: "odd-1",
		edit: func(t *testing.T, f *fixture) {
			f.write(t, "odd dir/it's \"q\" \\ $x;|&\xff.txt", "odd-2\n")
		},
		want: "odd-2",
	},
	// The probes of the round c2-1 review: each replayed a stale pass at
	// 00e0c9dd.
	{
		// The link names the root, whose own metadata the edit leaves alone:
		// removing the temporary directory looks the link up, which keys
		// its target.
		name:  "review P1 missing directory behind a link from a temporary directory",
		tools: []string{"sh", "cat"},
		files: map[string]string{"data/keep.txt": "keep\n"},
		run: `dir, err := os.MkdirTemp("", "p1-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.Symlink(root(), filepath.Join(dir, "repo")); err != nil {
		return nil, err
	}
	return command("sh", "-c", "cat \"$1/repo/data/sub/value.txt\" 2>/dev/null || echo none", "sh", dir)`,
		before: "none",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "data/sub/value.txt", "added\n") },
		want:   "added",
	},
	{
		name:  "review P2 link outside the root retargeted mid-run",
		tools: []string{"sh", "cat", "ln"},
		files: map[string]string{"a/value.txt": "a-1\n", "b/value.txt": "b-1\n"},
		run: `dir, err := os.MkdirTemp("", "p2-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	return command("sh", "-c", "ln -s \"$2/a\" \"$1/current\" && cat \"$1/current/value.txt\" && ln -sfn \"$2/b\" \"$1/current\" && cat \"$1/current/value.txt\"", "sh", dir, root())`,
		before: "a-1 | b-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "b/value.txt", "b-2\n") },
		want:   "a-1 | b-2",
	},
	{
		name:   "review P3 failed cd into a missing directory",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"fixtures/keep.txt": "keep\n"},
		run:    `return command("sh", "-c", "cd fixtures/sub 2>/dev/null && cat value.txt || echo none")`,
		before: "none",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "fixtures/sub/value.txt", "present\n") },
		want:   "present",
	},
	{
		name:   "review P3b cd through a link that is retargeted",
		tools:  []string{"sh", "cat"},
		files:  map[string]string{"a/value.txt": "a-1\n", "b/value.txt": "b-1\n", "dirs/keep.txt": "keep\n"},
		links:  map[string]string{"dirs/current": "../a"},
		run:    `return command("sh", "-c", "cd dirs/current && cat value.txt")`,
		before: "a-1",
		edit:   func(t *testing.T, f *fixture) { f.relink(t, "dirs/current", "../b") },
		want:   "b-1",
	},
	{
		name:   "review P4 file read through /proc/self/cwd",
		tools:  []string{"cat"},
		files:  map[string]string{"data/value.txt": "proc-1\n"},
		run:    `return command("cat", "/proc/self/cwd/data/value.txt")`,
		before: "proc-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "data/value.txt", "proc-2\n") },
		want:   "proc-2",
	},
	{
		name:  "review P5 read-only directory made writable and given a link",
		tools: []string{"sh", "cat", "chmod", "ln"},
		files: map[string]string{"data/value.txt": "sealed-1\n"},
		run: `dir, err := os.MkdirTemp("", "p5-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	defer os.Chmod(dir, 0o755)
	if err := os.Chmod(dir, 0o555); err != nil {
		return nil, err
	}
	return command("sh", "-c", "cat \"$1/value.txt\" 2>/dev/null || echo missing; chmod u+w \"$1\" && ln -s \"$2/data/value.txt\" \"$1/value.txt\" && cat \"$1/value.txt\"", "sh", dir, root())`,
		before: "missing | sealed-1",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "data/value.txt", "sealed-2\n") },
		want:   "missing | sealed-2",
	},
	{
		name:   "review P6 entry added to a listed directory whose time is restored",
		tools:  []string{"ls"},
		files:  map[string]string{"fixtures/dir/a.txt": "a\n"},
		setup:  func(t *testing.T, f *fixture) { f.backdate(t, "fixtures/dir", f.base) },
		run:    `return command("ls", "fixtures/dir")`,
		before: "a.txt",
		edit: func(t *testing.T, f *fixture) {
			f.write(t, "fixtures/dir/b.txt", "b\n")
			f.backdate(t, "fixtures/dir", f.base) // as tar -x, rsync -a and cp -a do
		},
		want: "a.txt | b.txt",
	},
	{
		// The variable reaches neither run, so the cached result is what a
		// fresh run prints. Through a plain environment, as at 00e0c9dd, the
		// fresh run prints "present" and the cached one "absent".
		name:            "review P7 variable exported after the prime, through the gate's environment",
		tools:           []string{"sh"},
		run:             `return command("sh", "-c", "echo \"${REVIEW_ADDED:-absent}\"")`,
		gateEnvironment: true,
		consistent:      true,
		before:          "absent",
		edit:            func(t *testing.T, f *fixture) {},
		editEnv:         []string{"REVIEW_ADDED=present"},
		want:            "absent",
	},
	{
		name: "m8 package initializer reads a file",
		files: map[string]string{
			"lib/init.go": `package lib

import "os"

// Value is read when the package initializes, before any test runs and
// before Go's test log records anything.
var Value string

func init() {
	data, _ := os.ReadFile("../data/init.txt")
	Value = string(data)
}
`,
			"data/init.txt": "init-1\n",
		},
		imports: []string{"fixture/lib"},
		run:     `return []byte(lib.Value), nil`,
		before:  "init-1",
		edit:    func(t *testing.T, f *fixture) { f.write(t, "data/init.txt", "init-2\n") },
		want:    "init-2",
	},
	{
		// Both files have the same size, mode and time, so only the link's
		// own Lstat tells them apart.
		name:   "c2-2 N3 read through an in-root link retargeted to a look-alike file",
		tools:  []string{"cat"},
		files:  map[string]string{"a/value.txt": "a-1\n", "b/value.txt": "b-1\n"},
		links:  map[string]string{"dirs/current": "../a"},
		run:    `return command("cat", "dirs/current/value.txt")`,
		before: "a-1",
		edit:   func(t *testing.T, f *fixture) { f.relink(t, "dirs/current", "../b") },
		want:   "b-1",
	},
	{
		// The shell that changed directory exits before its background
		// reader looks anything up, so the reader is reparented. The relink
		// keeps the link's and the directories' times, so the chdir's lookup
		// of the link does not change the key; only the reader's name under
		// the link does.
		name:  "c2-2 N2b cd through a link relinked with its time kept, reparented reader",
		tools: []string{"sh", "cat", "sleep", "touch"},
		files: map[string]string{"a/value.txt": "a-1\n", "b/value.txt": "b-22\n"},
		links: map[string]string{"dirs/current": "../a"},
		setup: func(t *testing.T, f *fixture) {
			for _, dir := range []string{"a", "b", "dirs"} {
				f.backdate(t, dir, f.base)
			}
			f.backdateLink(t, "dirs/current", f.base)
		},
		imports: []string{"time"},
		run: `dir, err := os.MkdirTemp("", "n2b-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out")
	if _, err := command("sh", "-c", "cd dirs/current && { (sleep 1; cat value.txt > \"$1\") >/dev/null 2>&1 & }", "sh", out); err != nil {
		return nil, err
	}
	for range 2400 { // 60 s: 5 s failed only a loaded host (fu-gate-flakes)
		if data, err := os.ReadFile(out); err == nil && len(data) > 0 {
			return data, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, os.ErrDeadlineExceeded`,
		before: "a-1",
		edit: func(t *testing.T, f *fixture) {
			f.relink(t, "dirs/current", "../b")
			f.backdateLink(t, "dirs/current", f.base)
			f.backdate(t, "dirs", f.base)
		},
		want: "b-22",
	},
	{
		// Review c2-3 probe D1: a directory opened through a link, without
		// O_DIRECTORY, then a name looked up relative to its descriptor. The
		// relink keeps every time, and the opened directory lists the same
		// entry (sub) either way, so only the name under the link changes the
		// key.
		name:    "c2-3 D1 openat on a directory opened through a link relinked with its time kept",
		tools:   []string{"touch"},
		files:   map[string]string{"a/sub/value.txt": "a-1\n", "b/sub/value.txt": "b-22\n"},
		links:   map[string]string{"dirs/current": "../a"},
		setup:   backdateLinkedDirs,
		imports: []string{"io", "syscall"},
		run: `d, err := syscall.Open(filepath.Join(root(), "dirs", "current"), syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(d)
	fd, err := syscall.Openat(d, "sub/value.txt", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "value.txt")
	defer file.Close()
	return io.ReadAll(file)`,
		before: "a-1",
		edit:   relinkLinkedDirs,
		want:   "b-22",
	},
	{
		// Review c2-3 probe D2: as D1, but fchdir to the descriptor, then a
		// name relative to the working directory.
		name:  "c2-3 D2 fchdir to a directory opened through a link relinked with its time kept",
		tools: []string{"touch"},
		files: map[string]string{"a/sub/value.txt": "a-1\n", "b/sub/value.txt": "b-22\n"},
		links: map[string]string{"dirs/current": "../a"},
		setup: backdateLinkedDirs,
		run: `back, err := os.Open(".")
	if err != nil {
		return nil, err
	}
	defer back.Close()
	defer back.Chdir()
	d, err := os.Open(filepath.Join(root(), "dirs", "current"))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := d.Chdir(); err != nil {
		return nil, err
	}
	return os.ReadFile("sub/value.txt")`,
		before: "a-1",
		edit:   relinkLinkedDirs,
		want:   "b-22",
	},
	{
		// connect looks the socket's path up: a file added there changes
		// the call's outcome. The path is relative to the probe's directory:
		// sun_path holds 108 bytes, and an absolute path under a long
		// temporary directory (the closure sweep's) fails with EINVAL.
		name:    "o1 unix socket path added",
		imports: []string{"net"},
		run: `conn, err := net.Dial("unix", filepath.Join("..", "run", "agent.sock"))
	if err != nil {
		return []byte(err.Error()), nil
	}
	conn.Close()
	return []byte("connected"), nil`,
		files:  map[string]string{"run/keep.txt": "keep\n"},
		before: "no such file",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "run/agent.sock", "") },
		want:   "connection refused",
	},
	{
		// Review c2-3 O-1: the call shape the listener's old handover let
		// through unnotified, and a second filter then failed with EPERM, from
		// a process the test starts. With the listener taken by pidfd_getfd
		// there is one filter and no exception, so no process forked between
		// two filters can use the shape; it is observed like any sendmsg.
		name: "c2-3 O-1 sendmsg on descriptor 3 with MSG_CMSG_CLOEXEC from a child",
		files: map[string]string{
			"run/keep.txt": "keep\n",
			"sendmsg3/sendmsg3.go": `package sendmsg3

import (
	"fmt"
	"os"
	"syscall"
)

// Variable names the socket path to send to; a process started with it
// set sends one datagram from descriptor 3 and exits.
const Variable = "SENDMSG3_PATH"

func init() {
	path := os.Getenv(Variable)
	if path == "" {
		return
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err == nil && fd != 3 {
		err = syscall.Dup3(fd, 3, 0)
	}
	if err == nil {
		err = syscall.Sendmsg(3, []byte("x"), nil, &syscall.SockaddrUnix{Name: path}, 0x40000000)
	}
	fmt.Print(err)
	os.Exit(0)
}
`,
		},
		imports: []string{"fixture/sendmsg3"},
		// The path is relative: sun_path holds 108 bytes, less than a long
		// temporary directory needs, and Go refuses a longer one with EINVAL.
		run: `child := exec.Command(os.Args[0])
	child.Dir = root()
	child.Env = append(os.Environ(), sendmsg3.Variable+"="+filepath.Join("run", "agent.sock"))
	return child.Output()`,
		before: "no such file",
		edit:   func(t *testing.T, f *fixture) { f.write(t, "run/agent.sock", "") },
		want:   "connection refused",
	},
	{
		name:       "c2-2 N5 inherited ERGO_TESTINPUTS_FILTERED",
		files:      map[string]string{"data/value.txt": "v-1\n"},
		env:        []string{"ERGO_TESTINPUTS_FILTERED=1"},
		run:        `return os.ReadFile(filepath.Join(root(), "data", "value.txt"))`,
		unobserved: "result not cacheable, inputs unobserved: ERGO_TESTINPUTS_FILTERED is set, but ",
	},
	{
		name:       "c2-2 N5 inherited ERGO_TESTINPUTS_LISTENER",
		files:      map[string]string{"data/value.txt": "v-1\n"},
		env:        []string{"ERGO_TESTINPUTS_LISTENER=3"},
		run:        `return os.ReadFile(filepath.Join(root(), "data", "value.txt"))`,
		unobserved: "result not cacheable, inputs unobserved: ERGO_TESTINPUTS_LISTENER is set, but ",
	},
	{
		name:       "fail closed: bpf",
		imports:    []string{"runtime", "syscall"},
		run:        `_, _, errno := syscall.RawSyscall(map[string]uintptr{"amd64": 321, "arm64": 280}[runtime.GOARCH], 0, 0, 0); return []byte(errno.Error()), nil`,
		unobserved: "result not cacheable, inputs unobserved: bpf",
	},
	{
		// Review c2-3 probe C1: an undumpable process refuses the observer's
		// memory reads, but the kernel still reads its struct msghdr and looks
		// the socket path up. Only the sendmsg runs undumpable, so its header
		// is the one read that fails. The path is relative: sun_path holds 108
		// bytes, less than a long temporary directory needs.
		name:    "c2-3 C1 fail closed: sendmsg from an undumpable process",
		imports: []string{"syscall"},
		run: `address := &syscall.SockaddrUnix{Name: filepath.Join("run", "agent.sock")}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
	err = syscall.Sendmsg(fd, []byte("x"), nil, address, 0)
	syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 1, 0)
	if err != nil {
		return []byte(err.Error()), nil
	}
	return []byte("sent"), nil`,
		files:      map[string]string{"run/keep.txt": "keep\n"},
		unobserved: "result not cacheable, inputs unobserved: read a message header: operation not permitted",
	},
	{
		name:       "fail closed: a system call newer than the table",
		imports:    []string{"syscall"},
		run:        `_, _, errno := syscall.RawSyscall(463, 0, 0, 0); return []byte(errno.Error()), nil`,
		unobserved: "result not cacheable, inputs unobserved: system call 463 is newer than the observer's table",
	},
	{
		name:       "fail closed: unshare",
		imports:    []string{"syscall"},
		run:        `_, _, errno := syscall.RawSyscall(syscall.SYS_UNSHARE, 0, 0, 0); return []byte(errno.Error()), nil`,
		unobserved: "result not cacheable, inputs unobserved: unshare",
	},
	{
		name:       "fail closed: a path with a newline",
		tools:      []string{"sh"},
		run:        `out, _ := command("sh", "-c", "cat \"$1\" 2>/dev/null; echo read", "sh", root()+"/new\nline"); return out, nil`,
		unobserved: "result not cacheable, inputs unobserved: a path holds a newline",
	},
	{
		name: "fail closed: seccomp refused",
		files: map[string]string{
			"blocker/main.go": blockerSource,
		},
		run:        `return []byte("ran"), nil`,
		wrap:       "blocker",
		unobserved: "result not cacheable, inputs unobserved: the test process installed no filter",
	},
}

// blockerSource fails every seccomp(2) call in its process tree with EPERM,
// then runs its arguments, so the test process cannot install its filter.
const blockerSource = `package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

type instruction struct {
	code   uint16
	jt, jf uint8
	k      uint32
}

func main() {
	runtime.LockOSThread()
	number := map[string]uint32{"amd64": 317, "arm64": 277}[runtime.GOARCH]
	program := []instruction{
		{code: 0x20, k: 0},                       // load the call number
		{code: 0x15, jf: 1, k: number},           // seccomp?
		{code: 0x06, k: 0x00050000 | 1},          // fail with EPERM
		{code: 0x06, k: 0x7fff0000},              // allow
	}
	prog := struct {
		len    uint16
		filter *instruction
	}{uint16(len(program)), &program[0]}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 38, 1, 0); errno != 0 {
		panic(errno)
	}
	if _, _, errno := syscall.RawSyscall(uintptr(number), 1, 1, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		panic(errno)
	}
	panic(syscall.Exec(os.Args[1], os.Args[1:], os.Environ()))
}
`

// probeSource is the fixture's test. observe chooses between Main and the
// plain TestMain of the control.
func probeSource(c e2eCase, pkg string, observe bool) string {
	testMain := "os.Exit(testinputs.Main(m))"
	if !observe {
		testMain = "_ = testinputs.ToolDigest\n\tos.Exit(m.Run())"
	}
	imports := ""
	for _, path := range c.imports {
		imports += "\t" + fmt.Sprintf("%q", path) + "\n"
	}
	return "package " + pkg + `

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
` + imports + `
	"fixture/testinputs"
)

// fixtureCase, logged, makes each fixture's test binary distinct, in every
// run of the cases: cmd/go keeps one test log per test binary and arguments,
// and a control whose test reads nothing through package os would otherwise
// replay an earlier run's result.
const fixtureCase = ` + fmt.Sprintf("%q", c.name+"/"+pkg+" in {{root}}") + `

func TestMain(m *testing.M) {
	` + testMain + `
}

func TestProbe(t *testing.T) {
	out, err := run()
	// One line, so the output is searchable as it was printed.
	t.Logf("fixture: %s", fixtureCase)
	t.Logf("output: %s", strings.ReplaceAll(string(bytes.TrimSpace(out)), "\n", " | "))
	if err != nil {
		t.Fatal(err)
	}
}

func root() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(dir)
}

func command(name string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...)
	c.Dir = root()
	return c.CombinedOutput()
}

func run() ([]byte, error) {
	` + c.run + `
}
`
}

// A fixture is a module holding a copy of this package's code, the case's
// files, an observed probe package and a plain control package.
type fixture struct {
	root           string
	base, edited   time.Time
	env            []string
	gate           bool
	goTool, runner string
}

// relink points a link at another target.
func (f *fixture) relink(t *testing.T, rel, target string) {
	t.Helper()
	if err := os.Remove(f.path(rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.path(rel)); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) path(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

// write writes a file dated as an edit: cmd/go refuses to cache a result
// that read a file modified within the last two seconds, and the date
// differs from the base date even if the size does not change.
func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	f.put(t, rel, content, f.edited)
}

func (f *fixture) put(t *testing.T, rel, content string, date time.Time) {
	t.Helper()
	path := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(content, "{{root}}", f.root)), 0o644); err != nil {
		t.Fatal(err)
	}
	f.backdate(t, rel, date)
}

func (f *fixture) backdate(t *testing.T, rel string, date time.Time) {
	t.Helper()
	if err := os.Chtimes(f.path(rel), date, date); err != nil {
		t.Fatal(err)
	}
}

// backdateLink dates a link itself, as touch -h does; os.Chtimes follows it.
func (f *fixture) backdateLink(t *testing.T, rel string, date time.Time) {
	t.Helper()
	if output, err := exec.Command("touch", "-h", "-d", fmt.Sprintf("@%d", date.Unix()), f.path(rel)).CombinedOutput(); err != nil {
		t.Fatalf("touch -h %s: %v\n%s", rel, err, output)
	}
}

// backdateLinkedDirs dates the directories of the D1 and D2 cases and their
// link alike, so a relink with its time kept leaves them as they were.
func backdateLinkedDirs(t *testing.T, f *fixture) {
	for _, dir := range []string{"a", "a/sub", "b", "b/sub", "dirs"} {
		f.backdate(t, dir, f.base)
	}
	f.backdateLink(t, "dirs/current", f.base)
}

// relinkLinkedDirs points the D1 and D2 cases' link at b, keeping the
// link's and its directory's times.
func relinkLinkedDirs(t *testing.T, f *fixture) {
	f.relink(t, "dirs/current", "../b")
	f.backdateLink(t, "dirs/current", f.base)
	f.backdate(t, "dirs", f.base)
}

// backdateTree dates every file below rel, as backdate does one.
func (f *fixture) backdateTree(t *testing.T, rel string, date time.Time) {
	t.Helper()
	err := filepath.WalkDir(f.path(rel), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		return os.Chtimes(path, date, date)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) git(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	command.Dir = f.root
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

// sunPath is the size of sun_path in Linux's sockaddr_un, with its NUL.
const sunPath = 108

// longRoot returns dir, or a new directory below it whose path is longer
// than sunPath, so no socket below it has an absolute address that fits.
// Each fixture root is long on every host, so a case that names a socket
// by its absolute path fails on every host, not only under a long TMPDIR
// (batch 9: the closure sweep's TMPDIR made o1 fail with EINVAL).
func longRoot(t *testing.T, dir string) string {
	t.Helper()
	if n := sunPath - len(dir); n > 0 {
		dir = filepath.Join(dir, strings.Repeat("p", n))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newFixture(t *testing.T, c e2eCase) *fixture {
	t.Helper()
	now := time.Now()
	f := &fixture{root: t.TempDir(), base: now.Add(-2 * time.Hour), edited: now.Add(-time.Hour)}
	if resolved, err := filepath.EvalSymlinks(f.root); err == nil {
		f.root = resolved
	}
	f.root = longRoot(t, f.root)
	f.put(t, "go.mod", "module fixture\n\ngo 1.24\n", f.base)
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := filepath.Glob("testenv/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range append(sources, environment...) {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(data), `"ergo.local/prototype/internal/testinputs/`, `"fixture/testinputs/`)
		f.put(t, "testinputs/"+source, text, f.base)
	}
	for rel, content := range c.files {
		f.put(t, rel, content, f.base)
	}
	for _, rel := range c.exec {
		if err := os.Chmod(f.path(rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range c.links {
		if err := os.MkdirAll(filepath.Dir(f.path(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, f.path(rel)); err != nil {
			t.Fatal(err)
		}
	}
	f.put(t, "probe/probe_test.go", probeSource(c, "probe", true), f.base)
	if c.unobserved == "" {
		f.put(t, "control/control_test.go", probeSource(c, "control", false), f.base)
	}
	if c.setup != nil {
		c.setup(t, f)
	}
	f.env = expand(c.env, f.root)
	f.gate = c.gateEnvironment
	if f.goTool, err = exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	f.runner = f.goTool
	if c.wrap != "" {
		f.runner = filepath.Join(t.TempDir(), "wrapper")
		build := exec.Command(f.goTool, "build", "-trimpath", "-o", f.runner, "./"+c.wrap)
		build.Dir = f.root
		build.Env = f.environment()
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", c.wrap, err, output)
		}
	}
	return f
}

func expand(env []string, root string) []string {
	var out []string
	for _, entry := range env {
		out = append(out, strings.ReplaceAll(entry, "{{root}}", root))
	}
	return out
}

// environment is the go command's: the test's own, without settings that
// would change caching, with the fixture's variables.
func (f *fixture) environment() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GOFLAGS", "GOWORK", ToolDigest, "BUN_INSPECT_PRELOAD", "FIXTURE_VALUE":
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GOFLAGS=", "GOWORK=off", ToolDigest+"=e2e")
	if os.Getenv("ERGO_E2E_DEBUG") == "1" {
		env = append(env, "GODEBUG=gocachetest=1")
	}
	env = append(env, f.env...)
	if f.gate {
		return testenv.Environment(env)
	}
	return env
}

// result is one package's outcome in a go test run: its own part of the
// output, and with ERGO_E2E_DEBUG=1 the whole output, for failure reports.
type result struct {
	ok, cached bool
	output     string
	full       string
}

// report is what a failure shows of a result.
func (r result) report() string {
	if r.full != "" {
		return r.output + "\n--- whole go test output ---\n" + r.full
	}
	return r.output
}

var resultLine = regexp.MustCompile(`(?m)^(ok|FAIL)\s+fixture/(probe|control)\s+(.*)$`)

// goTest runs the fixture's tests and returns each package's result.
func (f *fixture) goTest(t *testing.T, packages ...string) map[string]result {
	t.Helper()
	return f.goTestFlags(t, nil, packages...)
}

// goTestFlags is goTest with extra go test flags.
func (f *fixture) goTestFlags(t *testing.T, flags []string, packages ...string) map[string]result {
	t.Helper()
	args := append([]string{"test", "-trimpath", "-v"}, flags...)
	for _, pkg := range packages {
		args = append(args, "./"+pkg)
	}
	command := exec.Command(f.runner, args...)
	if f.runner != f.goTool {
		command.Args = append([]string{f.runner, f.goTool}, args...)
	}
	command.Dir = f.root
	command.Env = f.environment()
	output, _ := command.CombinedOutput()
	text := string(output)
	results := map[string]result{}
	// go test -v prints each package's output and then its result line.
	start := 0
	for _, match := range resultLine.FindAllStringSubmatchIndex(text, -1) {
		pkg := text[match[4]:match[5]]
		status := text[match[6]:match[7]]
		results[pkg] = result{
			ok:     text[match[2]:match[3]] == "ok",
			cached: strings.Contains(status, "(cached)"),
			output: text[start:match[1]],
		}
		if os.Getenv("ERGO_E2E_DEBUG") == "1" {
			r := results[pkg]
			r.full = text
			results[pkg] = r
		}
		start = match[1]
	}
	for _, pkg := range packages {
		if _, ok := results[pkg]; !ok {
			t.Fatalf("go %s: no result for %s:\n%s", strings.Join(args, " "), pkg, text)
		}
	}
	return results
}

// probeOutput returns what the probe logged as its subprocess's output.
func probeOutput(text string) string {
	var outputs []string
	for line := range strings.Lines(text) {
		if _, output, ok := strings.Cut(line, ": output: "); ok {
			outputs = append(outputs, strings.TrimSuffix(output, "\n"))
		}
	}
	return strings.Join(outputs, "\n")
}

func TestCacheCases(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test over fixture modules")
	}
	_ = os.Getenv(ToolDigest) // the tools the cases run are inputs of this result
	for _, c := range e2eCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, tool := range c.tools {
				if _, err := exec.LookPath(tool); err != nil {
					t.Skipf("%s is not on PATH", tool)
				}
			}
			f := newFixture(t, c)
			if c.unobserved != "" {
				for run := 1; run <= 2; run++ {
					r := f.goTest(t, "probe")["probe"]
					if !r.ok || r.cached || !strings.Contains(r.output, "testinputs: "+c.unobserved) {
						t.Fatalf("run %d: want an uncached pass reporting %q:\n%s", run, c.unobserved, r.report())
					}
				}
				t.Logf("receipt: two runs, neither cached, each reporting %q", c.unobserved)
				return
			}
			both := []string{"probe", "control"}
			check := func(step string, results map[string]result, cached map[string]bool, contains string) {
				t.Helper()
				for _, pkg := range both {
					r := results[pkg]
					if !r.ok || r.cached != cached[pkg] {
						t.Fatalf("%s: %s: want ok, cached %v:\n%s", step, pkg, cached[pkg], r.report())
					}
				}
				if contains != "" && !strings.Contains(probeOutput(results["probe"].output), contains) {
					t.Fatalf("%s: probe output lacks %q:\n%s", step, contains, results["probe"].report())
				}
			}
			prime := f.goTest(t, both...)
			check("prime", prime, map[string]bool{"probe": false, "control": false}, c.before)
			if !c.consistent && strings.Contains(probeOutput(prime["probe"].output), c.want) {
				t.Fatalf("prime: the probe already printed %q, which only the edit should produce:\n%s", c.want, prime["probe"].report())
			}
			check("unchanged", f.goTest(t, both...), map[string]bool{"probe": true, "control": true}, c.before)
			c.edit(t, f)
			if c.editEnv != nil {
				f.env = expand(c.editEnv, f.root)
			}
			if c.consistent {
				replayed := f.goTest(t, "probe")["probe"]
				fresh := f.goTestFlags(t, []string{"-count=1"}, "probe")["probe"]
				if !replayed.ok || !fresh.ok || probeOutput(replayed.output) != probeOutput(fresh.output) || !strings.Contains(probeOutput(fresh.output), c.want) {
					t.Fatalf("edited: the probe (cached %v) printed %q, a fresh run %q, want both %q:\n%s\n%s", replayed.cached, probeOutput(replayed.output), probeOutput(fresh.output), c.want, replayed.report(), fresh.report())
				}
				t.Logf("receipt: after the edit the probe (cached %v) printed %q, as a fresh run did", replayed.cached, probeOutput(replayed.output))
				return
			}
			edited := f.goTest(t, both...)
			check("edited", edited, map[string]bool{"probe": false, "control": true}, c.want)
			if strings.Contains(probeOutput(edited["control"].output), c.want) {
				t.Fatalf("edited: the control saw the edit, so the case does not need observation:\n%s", edited["control"].report())
			}
			check("edited, unchanged", f.goTest(t, "probe", "control"), map[string]bool{"probe": true, "control": true}, c.want)
			t.Logf("receipt: after the edit the plain control replayed (cached) %q; the observed probe reran and printed %q", probeOutput(edited["control"].output), probeOutput(edited["probe"].output))
		})
	}
}

// TestGuardSeesNewTestPackages runs the coverage guard in a module copied
// from this one, in a linked worktree (whose Git index lies outside the
// module root) and in a plain checkout, and adds a test package without
// TestMain: untracked, then committed in the worktree. The guard must rerun
// and fail each time, never replay its earlier pass.
func TestGuardSeesNewTestPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test over fixture modules")
	}
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	for _, worktree := range []bool{true, false} {
		name := "plain checkout, untracked package"
		if worktree {
			name = "linked worktree, untracked then committed package"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := guardFixture(t)
			if worktree {
				linked := filepath.Join(t.TempDir(), "linked")
				f.git(t, "worktree", "add", "--quiet", "-b", "linked", linked)
				f.root = linked
				// cmd/go caches no result that read a file changed in the
				// last two seconds.
				f.backdateTree(t, ".", f.base)
			}
			const guard = "internal/testinputs/guard"
			if r := f.goTestPackage(t, guard); !r.ok || r.cached {
				t.Fatalf("prime: want an uncached pass:\n%s", r.output)
			}
			if r := f.goTestPackage(t, guard); !r.ok || !r.cached {
				t.Fatalf("unchanged: want a cached pass:\n%s", r.output)
			}
			f.write(t, "internal/reviewnew/new_test.go", "package reviewnew\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {}\n")
			steps := []string{"untracked"}
			if worktree {
				steps = append(steps, "committed")
			}
			for _, step := range steps {
				if step == "committed" {
					f.git(t, "add", "internal/reviewnew/new_test.go")
					f.git(t, "commit", "--quiet", "-m", "add a test package without TestMain")
				}
				r := f.goTestPackage(t, guard)
				if r.ok || r.cached || !strings.Contains(r.output, "internal/reviewnew: no TestMain") {
					t.Fatalf("%s: want an uncached failure naming internal/reviewnew (ok %v, cached %v):\n%s", step, r.ok, r.cached, r.output)
				}
			}
			t.Logf("receipt: the guard reran and failed after the package was added (%s)", strings.Join(steps, ", then "))
		})
	}
}

// guardFixture makes a Git repository holding a module named as this one,
// with this package's code and its guard, committed.
func guardFixture(t *testing.T) *fixture {
	t.Helper()
	now := time.Now()
	f := &fixture{root: t.TempDir(), base: now.Add(-2 * time.Hour), edited: now.Add(-time.Hour)}
	if resolved, err := filepath.EvalSymlinks(f.root); err == nil {
		f.root = resolved
	}
	f.put(t, "go.mod", "module ergo.local/prototype\n\ngo 1.24\n", f.base)
	var sources []string
	for _, pattern := range []string{"*.go", "testenv/*.go", "guard/*.go"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, matches...)
	}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") && !strings.HasPrefix(source, "guard/") {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		f.put(t, "internal/testinputs/"+source, string(data), f.base)
	}
	// cmd/go keeps one test log per test binary, and -trimpath would make
	// every fixture's guard the same binary; a logged constant makes each
	// distinct.
	f.put(t, "internal/testinputs/guard/fixture_test.go", "package guard\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) { t.Log("+fmt.Sprintf("%q", f.root)+") }\n", f.base)
	var err error
	if f.goTool, err = exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	f.runner = f.goTool
	f.git(t, "init", "--quiet", "-b", "main")
	f.git(t, "add", "-A")
	f.git(t, "commit", "--quiet", "-m", "fixture")
	return f
}

// goTestPackage runs go test on one package of a module and returns its
// result, by its full import path.
func (f *fixture) goTestPackage(t *testing.T, pkg string) result {
	t.Helper()
	command := exec.Command(f.goTool, "test", "-trimpath", "./"+pkg)
	command.Dir = f.root
	command.Env = f.environment()
	output, _ := command.CombinedOutput()
	text := string(output)
	line := regexp.MustCompile(`(?m)^(ok|FAIL)\s+\S+/` + regexp.QuoteMeta(pkg) + `(\s.*)?$`).FindStringSubmatch(text)
	if line == nil {
		t.Fatalf("go test ./%s: no result:\n%s", pkg, text)
	}
	return result{ok: line[1] == "ok", cached: strings.Contains(line[2], "(cached)"), output: text}
}

func TestCacheCasesAreNamedOnce(t *testing.T) {
	var names []string
	for _, c := range e2eCases {
		if slices.Contains(names, c.name) {
			t.Errorf("case %q is listed twice", c.name)
		}
		names = append(names, c.name)
		if c.unobserved == "" && (c.edit == nil || c.want == "") {
			t.Errorf("case %q has no edit or no expected output", c.name)
		}
	}
}

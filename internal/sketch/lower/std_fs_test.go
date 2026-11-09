package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fsLib reads through fs.Files and turns each failure kind into a line,
// by a catch that lists the whole row: the arm taken names the kind.
// It holds no import, so each program writes its own first.
const fsLib = `
func read(path string) string needs fs.Files {
	let text = fs.Files.ReadFile(path) catch {
	case fs.NotFound as n:
		return "NotFound \(n.Path)"
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	"text \(strings.TrimSpace(text))"
}

func list(path string) string needs fs.Files {
	let names = fs.Files.ReadDir(path) catch {
	case fs.NotFound as n:
		return "NotFound \(n.Path)"
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	"\(len(names)): \(strings.Join(names, " "))"
}

func stat(path string) string needs fs.Files {
	let info = fs.Files.Stat(path) catch {
	case fs.NotFound as n:
		return "NotFound \(n.Path)"
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	if info.Dir {
		"\(info.Name) dir"
	} else {
		"\(info.Name) \(info.Size)"
	}
}

// viaValue calls an operation on a value of fs.Files, not on the
// capability.
func viaValue(f fs.Files) string {
	let text = f.ReadFile("a.md") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "value failed"
	}
	"value \(strings.TrimSpace(text))"
}
`

// fsMemory is package memory: a program's own Files provider over a map
// of paths to text, with the names its root directory lists.
const fsMemory = `import fs

type Files { Docs Map[string, string], Names []string }

func ReadFile(m Files, path string) string fails fs.NotFound | fs.Denied | fs.Failed {
	match m.Docs.Get(path) {
	case Some[string] as s:
		s.Value
	case None:
		return fs.NotFound{Path: path}
	}
}

func ReadDir(m Files, path string) []string fails fs.NotFound | fs.Denied | fs.Failed {
	if path != "." {
		return fs.NotFound{Path: path}
	}
	m.Names
}

func Stat(m Files, path string) fs.Info fails fs.NotFound | fs.Denied | fs.Failed {
	if path == "." {
		return fs.Info{Name: ".", Size: 0, Dir: true}
	}
	let text = try ReadFile(m, path)
	fs.Info{Name: path, Size: len(text), Dir: false}
}
`

// TestFSFilesMemory: fs.Files is an open std protocol (check.SealedStd;
// no std extern returns it), so a program's own provider in a package of
// the program binds it in a context and passes as a value, and the same
// functions give the host's lines for the paths the memory holds.
func TestFSFilesMemory(t *testing.T) {
	needsBuild(t)
	mem := checkPackage(t, "memory", []pkgSource{{"/ergo-test/memory/files.ergo", fsMemory}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/ergo-test/main/main.ergo", "import fs\nimport memory\nimport strings\n" + fsLib + `
func run() needs Console | fs.Files {
	Console.Println(read("a.md"))
	Console.Println(list("."))
	Console.Println(stat("a.md"))
	Console.Println(stat("."))
	Console.Println(read("missing.md"))
	Console.Println(list("sub"))
}

func main() needs Console {
	let files = memory.Files{Docs: Map[string, string]{"a.md": "alpha\n", "b.md": "beta\n"}, Names: []string{"a.md", "b.md", "sub"}}
	with context { fs.Files: files } { run() }
	Console.Println(viaValue(files))
}
`}}, pkgImporter{"memory": mem.Package})
	root.Imports = []Unit{mem}
	p := mustLowerUnit(t, "fsMemory", root, false)
	buildAndRun(t, []*lowered{p}, map[string]string{"fsMemory": strings.Join([]string{
		"text alpha",
		"3: a.md b.md sub",
		"a.md 6",
		". dir",
		"NotFound missing.md",
		"NotFound sub",
		"value alpha",
	}, "\n") + "\n"})
}

// fsWritesProgram writes through fs.Writes and reads back through
// fs.Files, both bound to one fs.Host, in a working directory that holds
// kept.md (mode 0640), linked.md (with a second hard link), a read-only
// directory locked and no directory missing.
const fsWritesProgram = "import fs\n" + `
func show(path string) string needs fs.Files {
	let text = fs.Files.ReadFile(path) catch {
	case fs.NotFound as n:
		return "NotFound \(n.Path)"
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	"text \(text)"
}

func put(path string, text string) string needs fs.Writes {
	fs.Writes.WriteFile(path, text) catch {
	case fs.NotFound as n:
		return "NotFound \(n.Path)"
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	"wrote \(path)"
}

func mkdir(path string) string needs fs.Writes {
	fs.Writes.MkdirAll(path) catch {
	case fs.Denied as d:
		return "Denied \(d.Path)"
	case fs.Failed as f:
		return "Failed \(f.Path): \(f.Reason)"
	}
	"made \(path)"
}

func run() needs Console | fs.Files | fs.Writes {
	Console.Println(mkdir("a/b"))
	Console.Println(mkdir("a/b"))
	Console.Println(put("a/b/x.md", "one"))
	Console.Println(show("a/b/x.md"))
	Console.Println(put("a/b/x.md", "two"))
	Console.Println(show("a/b/x.md"))
	Console.Println(put("new.md", "fresh"))
	Console.Println(put("kept.md", "kept two"))
	Console.Println(put("linked.md", "linked two"))
	Console.Println(show("linked.md"))
	Console.Println(put("locked/y.md", "no"))
	Console.Println(put("missing/y.md", "no"))
	Console.Println(put("a", "no"))
	Console.Println(mkdir("new.md/c"))
	Console.Println(mkdir("locked/c"))
}

// main binds one fs.Host as both capabilities.
func main() needs Console | Ambient {
	with context { fs.Files: fs.NewHost(), fs.Writes: fs.NewHost() } { run() }
}
`

// TestFSWritesRuns: std fs writes the host file system through fs.Writes
// (stdlib.md, fs). MkdirAll makes the missing parents and is no failure
// for a directory that is there. WriteFile makes a new file with mode 0666
// less the umask, and replaces a file that is there by a new one renamed
// over it: the new file keeps the old mode, the text read back is the new
// text, and a second hard link of the old file keeps the old text. The new
// file is made beside the target, not in TMPDIR, so the program runs with
// a TMPDIR that does not exist. A write into a read-only directory is
// Denied, under a missing directory NotFound, over a directory Failed. No
// temporary file is left.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestFSWritesRuns(t *testing.T) {
	needsBuild(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes into a directory of mode 0555, so Denied cannot be made")
	}
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	if err := os.WriteFile(at("kept.md"), []byte("kept one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(at("kept.md"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at("linked.md"), []byte("linked one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(at("linked.md"), at("linked-old.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(at("locked"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(at("locked"), 0o755) })
	// The mode a new file gets under this process's umask, found the way
	// the body makes one: an open with mode 0666.
	probe := filepath.Join(t.TempDir(), "probe")
	pf, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	pf.Close()
	pi, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	newMode := pi.Mode().Perm()

	exes := tableExes(t, "fsWrites")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	want := strings.Join([]string{
		"made a/b",
		"made a/b",
		"wrote a/b/x.md",
		"text one",
		"wrote a/b/x.md",
		"text two",
		"wrote new.md",
		"wrote kept.md",
		"wrote linked.md",
		"text linked two",
		"Denied locked/y.md",
		"NotFound missing/y.md",
		"Failed a: is a directory",
		"Failed new.md/c: not a directory",
		"Denied locked/c",
	}, "\n") + "\n"
	stdout, stderr, code := runInEnv(t, dir, []string{"TMPDIR=/nonexistent/loop-probe-x"}, exes["fsWrites"])
	if stdout != want || stderr != "" || code != 0 {
		t.Fatalf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
	}
	for name, text := range map[string]string{"a/b/x.md": "two", "new.md": "fresh", "kept.md": "kept two", "linked.md": "linked two", "linked-old.md": "linked one"} {
		if got, err := os.ReadFile(at(name)); string(got) != text || err != nil {
			t.Errorf("%s holds %q, %v; want %q", name, got, err, text)
		}
	}
	for name, mode := range map[string]os.FileMode{"new.md": newMode, "a/b/x.md": newMode, "kept.md": 0o640} {
		fi, err := os.Stat(at(name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if fi.Mode().Perm() != mode {
			t.Errorf("%s: mode %v; want %v", name, fi.Mode().Perm(), mode)
		}
	}
	if fi, err := os.Stat(at("a/b")); err != nil || !fi.IsDir() {
		t.Errorf("a/b is not a directory: %v", err)
	}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), ".tmp") {
			t.Errorf("a temporary file is left: %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// runIn runs exe with args in the working directory dir.
func runIn(t *testing.T, dir, exe string, args ...string) (string, string, int) {
	t.Helper()
	return runInEnv(t, dir, nil, exe, args...)
}

// runInEnv runs exe with args in the working directory dir, with env
// added to this process's environment.
func runInEnv(t *testing.T, dir string, env []string, exe string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

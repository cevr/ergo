package lower

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fsSafeProgram makes a private key directory and file, replaces a shared
// file by a private one, appends to a log, renames it, sets its mode and
// removes a file, through fs.Host bound to fs.Files and fs.Writes. It
// prints the failures of a second Remove, a Rename of a missing file and
// a Remove of a directory with entries, then the bits Perm gives each of
// the 216 modes, owner first.
const fsSafeProgram = "import fs\n" + `
func run() fails fs.NotFound | fs.Denied | fs.Failed needs Console | fs.Files | fs.Writes {
	try fs.Writes.MkdirAllMode("keys/sub", fs.PrivateDir())
	try fs.Writes.WriteFileMode("keys/sub/key", "secret", fs.Private())
	try fs.Writes.WriteFileMode("shared", "old", fs.Shared())
	try fs.Writes.WriteFileMode("shared", "new", fs.Private())
	try fs.Writes.AppendFile("log", "a\n")
	try fs.Writes.AppendFile("log", "b\n")
	let log = try fs.Files.ReadFile("log")
	let same = log == "a\nb\n"
	Console.Println("log \(same)")
	try fs.Writes.Rename("log", "log.1")
	try fs.Writes.Chmod("log.1", fs.Mode{Owner: fs.ReadWrite, Group: fs.ReadOnly, Other: fs.NoAccess})
	try fs.Writes.WriteFile("gone", "x")
	try fs.Writes.Remove("gone")
	fs.Writes.Remove("gone") catch {
	case fs.NotFound as n:
		Console.Println("NotFound \(n.Path)")
	case fs.Denied, fs.Failed:
		Console.Println("other")
	}
	fs.Writes.Rename("none", "x") catch {
	case fs.NotFound as n:
		Console.Println("NotFound \(n.Path)")
	case fs.Denied, fs.Failed:
		Console.Println("other")
	}
	fs.Writes.Remove("keys") catch {
	case fs.Failed as f:
		Console.Println("Failed \(f.Path): \(f.Reason)")
	case fs.NotFound, fs.Denied:
		Console.Println("other")
	}
	let all = []fs.Access{fs.NoAccess, fs.Execute, fs.ReadOnly, fs.ReadExecute, fs.ReadWrite, fs.ReadWriteExecute}
	let mut line = ""
	for o in all {
		for g in all {
			for x in all {
				line = line + " \(fs.Perm(fs.Mode{Owner: o, Group: g, Other: x}))"
			}
		}
	}
	Console.Println(line)
}

func main() needs Console | Ambient {
	with context { fs.Files: fs.NewHost(), fs.Writes: fs.NewHost() } {
		run() catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			Console.Println("failed")
		}
	}
}
`

// TestFSSafeRuns: the writes of std-fs-safe through fs.Host (stdlib.md,
// fs 11 to 14). The program runs under umask 0, so a mode the body does
// not set exactly shows as 0666 or 0777: WriteFileMode and MkdirAllMode
// give exactly the mode asked (a key 0600 in a directory 0700), a file
// that was there with a wider mode gets the narrower one, AppendFile adds
// at the end, Rename moves, Chmod sets the mode, Remove removes, and each
// failure names its path. Perm is owner*64 + group*8 + other over the
// bits read 4, write 2, execute 1.
func TestFSSafeRuns(t *testing.T) {
	dir := t.TempDir()
	p := mustLower(t, "fsSafe", fsSafeProgram)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	bits := []int{0, 1, 4, 5, 6, 7}
	var perms strings.Builder
	for _, o := range bits {
		for _, g := range bits {
			for _, x := range bits {
				fmt.Fprintf(&perms, " %d", o*64+g*8+x)
			}
		}
	}
	want := strings.Join([]string{
		"log true",
		"NotFound gone",
		"NotFound none",
		"Failed keys: directory not empty",
		perms.String(),
	}, "\n") + "\n"
	// No test of this package that runs at the same time makes a file:
	// the parallel tests wait until the sequential ones end.
	old := syscall.Umask(0)
	stdout, stderr, code := runIn(t, dir, exes["fsSafe"])
	syscall.Umask(old)
	if stdout != want || stderr != "" || code != 0 {
		t.Fatalf("exit %d\nstdout %q\nwant   %q\nstderr %q", code, stdout, want, stderr)
	}
	for name, mode := range map[string]os.FileMode{"keys": 0o700, "keys/sub": 0o700, "keys/sub/key": 0o600, "shared": 0o600, "log.1": 0o640} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if fi.Mode().Perm() != mode {
			t.Errorf("%s: mode %o; want %o", name, fi.Mode().Perm(), mode)
		}
	}
	for name, text := range map[string]string{"keys/sub/key": "secret", "shared": "new", "log.1": "a\nb\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); string(got) != text || err != nil {
			t.Errorf("%s holds %q, %v; want %q", name, got, err, text)
		}
	}
	for _, name := range []string{"log", "gone"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
}

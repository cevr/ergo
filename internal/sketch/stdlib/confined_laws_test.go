package stdlib_test

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestFSConfinedLaws runs the laws of fstest.Confined (rows.md, Test's ambient)
// against the Go bodies the test's ambient lowers to: each case makes its
// folder with TestFolderOpen, as a with or a law case does, resolves every
// path through FSConfine, as fs.NewTestHost's Host does, and ends with
// TestFolderRemove. It is the ledger's runner for the three laws, over
// seeded names and texts, and it holds the runtime's half of each: a path
// out of the folder by "..", by an absolute path or through a link that
// points out is Denied; each case's folder starts empty and is gone after
// the case. The Ergo laws run under ergo test too (examples/kvstore).
func TestFSConfinedLaws(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 8))
	names := []string{"", ".", "..", "x", "../x", "a/../../b", "/", "\x00"}
	for range 100 {
		names = append(names, randomText(r))
	}
	// A case is one folder, as the runner gives one to each law case.
	inCase := func(t *testing.T, body func(root string)) {
		t.Helper()
		root := v1.TestFolderOpen()
		body(root)
		v1.TestFolderRemove(root)
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the case's folder %s is still there after the case: %v", root, err)
		}
	}
	denied := func(root, path string) bool {
		_, err := v1.FSConfine(root, path)
		var d v1.Denied
		return errors.As(err, &d) && d.Path == path
	}
	laws := map[string]lawledger.Law{
		// Relative and absolute paths out of the folder, and a path through
		// a link inside it that points out, a link chain included.
		"fstest Confined.OutsideIsDenied": {Statement: `OutsideIsDenied(name string) {
		WriteFile("../"+name, "probe") catch {
		case fs.Denied:
			WriteFile(Folder()+"/../"+name, "probe") catch {
			case fs.Denied:
				return true
			case fs.NotFound, fs.Failed:
				return false
			}
			return false
		case fs.NotFound, fs.Failed:
			return false
		}
		false
	}`, Check: func(t *testing.T) {
			for _, name := range names {
				inCase(t, func(root string) {
					if !denied(root, "../"+name) || !denied(root, root+"/../"+name) {
						t.Errorf("OutsideIsDenied(%q): a path out of the folder is not Denied", name)
					}
				})
			}
			inCase(t, func(root string) {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("out", filepath.Join(root, "chain")); err != nil {
					t.Fatal(err)
				}
				for _, p := range []string{"out", "out/x", "chain/x/y", root + "/out/x"} {
					if !denied(root, p) {
						t.Errorf("OutsideIsDenied: %q leads out through a link and is not Denied", p)
					}
				}
			})
		}},
		// A path in the folder, by either spelling, resolves into it, and
		// a write through one is read through the other.
		"fstest Confined.InsideIsWritten": {Statement: `InsideIsWritten(text string) {
		try WriteFile(Folder()+"/inside", text)
		try ReadFile("inside") == text
	}`, Check: func(t *testing.T) {
			for _, text := range names {
				inCase(t, func(root string) {
					abs, err := v1.FSConfine(root, root+"/inside")
					if err != nil {
						t.Fatalf("InsideIsWritten: %v", err)
					}
					if err := v1.FSWriteFile(abs, text); err != nil {
						t.Fatalf("InsideIsWritten: %v", err)
					}
					rel, err := v1.FSConfine(root, "inside")
					if err != nil {
						t.Fatalf("InsideIsWritten: %v", err)
					}
					if got, err := v1.FSReadFile(rel); err != nil || got != text {
						t.Errorf("InsideIsWritten(%q): read %q, %v", text, got, err)
					}
				})
			}
		}},
		// Each case's folder is new and empty, though the case before left
		// a file in its own.
		"fstest Confined.EachCaseStartsEmpty": {Statement: `EachCaseStartsEmpty(text string) {
		let names = try ReadDir(".")
		try WriteFile("left-by-a-case", text)
		len(names) == 0
	}`, Check: func(t *testing.T) {
			seen := map[string]bool{}
			for _, text := range names[:20] {
				inCase(t, func(root string) {
					if seen[root] {
						t.Errorf("EachCaseStartsEmpty: the folder %s is a second case's", root)
					}
					seen[root] = true
					dot, _ := v1.FSConfine(root, ".")
					if got, err := v1.FSReadDir(dot); err != nil || len(got) != 0 {
						t.Errorf("EachCaseStartsEmpty: the folder holds %v, %v", got, err)
					}
					left, _ := v1.FSConfine(root, "left-by-a-case")
					if err := v1.FSWriteFile(left, text); err != nil {
						t.Fatal(err)
					}
					// A read-only directory the case made is removed too.
					locked := filepath.Join(root, "locked")
					if err := os.Mkdir(locked, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(locked, "f"), nil, 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(locked, 0o500); err != nil {
						t.Fatal(err)
					}
				})
			}
		}},
	}
	lawledger.Run(t, laws)
}

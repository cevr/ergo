package v1

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// withUmask runs f under the process umask mask and restores the old one.
// No test of this package is parallel, so no other test makes a file
// meanwhile.
func withUmask(mask int, f func()) {
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	f()
}

// permOf is the permission bits of the file at path, or -1.
func permOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// TestFSWriteFileModeIsExact: WriteFileMode gives the file exactly the
// mode asked, whatever the umask (stdlib.md, fs 11). The new file is
// created with that mode, so with umask 0 the file is no wider than the
// mode from its creation on, before any byte is in it; a file that was at
// the path, with a wider mode, gets the mode too. WriteFile after it keeps
// the mode (ModeIsKept).
func TestFSWriteFileModeIsExact(t *testing.T) {
	for _, mask := range []int{0, 0o002, 0o022, 0o077, 0o777} {
		for _, perm := range []fs.FileMode{0o600, 0o644, 0o640, 0o400, 0o755} {
			dir := t.TempDir()
			path := filepath.Join(dir, "key")
			var created []fs.FileMode
			afterCreate = func(tmp string) {
				fi, err := os.Stat(tmp)
				if err != nil {
					t.Errorf("stat new file: %v", err)
					return
				}
				if fi.Size() != 0 {
					t.Errorf("the new file holds %d bytes at its creation", fi.Size())
				}
				created = append(created, fi.Mode().Perm())
			}
			withUmask(mask, func() {
				if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
				if err := FSWriteFileMode(path, "secret", int(perm)); err != nil {
					t.Fatalf("FSWriteFileMode: %v", err)
				}
				if err := FSWriteFile(path, "secret two"); err != nil {
					t.Fatalf("FSWriteFile: %v", err)
				}
			})
			afterCreate = nil
			if len(created) != 2 {
				t.Fatalf("afterCreate ran %d times; want 2", len(created))
			}
			if want := perm &^ fs.FileMode(mask); created[0] != want {
				t.Errorf("umask %o, mode %o: the new file was created with %o; want %o (mode less the umask, never wider)", mask, perm, created[0], want)
			}
			if got := permOf(t, path); got != perm {
				t.Errorf("umask %o, mode %o: the file has mode %o after WriteFileMode and WriteFile; want %o", mask, perm, got, perm)
			}
			if got, _ := os.ReadFile(path); string(got) != "secret two" {
				t.Errorf("the file holds %q", got)
			}
		}
	}
}

// TestFSMkdirAllModeIsExact: MkdirAllMode gives each directory it makes
// exactly the mode, whatever the umask; a directory that is there keeps
// its mode; its failures are MkdirAll's.
func TestFSMkdirAllModeIsExact(t *testing.T) {
	for _, mask := range []int{0, 0o022, 0o077} {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "kept"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, "kept"), 0o751); err != nil {
			t.Fatal(err)
		}
		withUmask(mask, func() {
			for _, p := range []string{"kept/a/b/", "kept/a/b", "kept/c"} {
				if err := FSMkdirAllMode(filepath.Join(dir, p), 0o750); err != nil {
					t.Fatalf("FSMkdirAllMode(%s): %v", p, err)
				}
			}
		})
		for p, want := range map[string]fs.FileMode{"kept": 0o751, "kept/a": 0o750, "kept/a/b": 0o750, "kept/c": 0o750} {
			if got := permOf(t, filepath.Join(dir, p)); got != want {
				t.Errorf("umask %o: %s has mode %o; want %o", mask, p, got, want)
			}
		}
		file := filepath.Join(dir, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := FSMkdirAllMode(filepath.Join(file, "x"), 0o700); err != (Failed{Path: filepath.Join(file, "x"), Reason: "not a directory"}) {
			t.Errorf("FSMkdirAllMode under a file = %#v", err)
		}
		if err := FSMkdirAllMode("", 0o700); err != (Failed{Path: "", Reason: "no such file or directory"}) {
			t.Errorf("FSMkdirAllMode(\"\") = %#v", err)
		}
	}
}

// TestFSRenameRemoveFailures: Rename names the missing side; Remove of a
// directory with entries is Failed, of a missing path NotFound; Chmod of
// a missing path is NotFound; AppendFile to a directory is Failed.
func TestFSRenameRemoveFailures(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	if err := os.WriteFile(at("a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(at("full/in"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		got  error
		want error
	}{
		{"rename missing from", FSRename(at("none"), at("b")), NotFound{Path: at("none")}},
		{"rename into a missing directory", FSRename(at("a"), at("no/b")), NotFound{Path: at("no/b")}},
		{"remove missing", FSRemove(at("none")), NotFound{Path: at("none")}},
		{"remove a full directory", FSRemove(at("full")), Failed{Path: at("full"), Reason: "directory not empty"}},
		{"chmod missing", FSChmod(at("none"), 0o600), NotFound{Path: at("none")}},
		{"append to a directory", FSAppendFile(at("full"), "x"), Failed{Path: at("full"), Reason: "is a directory"}},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %#v; want %#v", c.name, c.got, c.want)
		}
	}
	if err := FSRemove(at("full/in")); err != nil {
		t.Errorf("remove an empty directory: %v", err)
	}
}

// TestFSAppendFileIsOneWrite: appends from many goroutines at once each
// land whole, at the end, as one write of an O_APPEND file does.
func TestFSAppendFileIsOneWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				line := fmt.Sprintf("%d-%d-%s\n", w, i, strings.Repeat("x", 200))
				if err := FSAppendFile(path, line); err != nil {
					t.Errorf("FSAppendFile: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != writers*each {
		t.Fatalf("%d lines; want %d", len(lines), writers*each)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if !strings.HasSuffix(l, strings.Repeat("x", 200)) || seen[l] {
			t.Fatalf("a torn or repeated line: %q", l)
		}
		seen[l] = true
	}
}

// fsModelFile is the model's view of one file: its text and its mode.
type fsModelFile struct {
	text string
	mode fs.FileMode
}

// TestFSWritesModel: generated sequences of WriteFile, WriteFileMode,
// AppendFile, Chmod, Rename, Remove, ReadFile and Stat on a few names in
// one directory, under a umask drawn per sequence, against a model that is
// a map of name to text and mode. Every result, failure kind, text, size
// and mode agrees with the model after each step. A failing seed prints
// the steps that led to it.
func TestFSWritesModel(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	masks := []int{0, 0o002, 0o022, 0o077}
	perms := []fs.FileMode{0o600, 0o644, 0o700, 0o400, 0o640, 0o755}
	texts := []string{"", "x", "line\n", "é世", "\x00\xff", strings.Repeat("z", 5000)}
	for seed := uint64(1); seed <= 100; seed++ {
		r := rand.New(rand.NewPCG(seed, 9))
		dir := t.TempDir()
		mask := masks[r.IntN(len(masks))]
		fresh := 0o666 &^ fs.FileMode(mask)
		model := map[string]fsModelFile{}
		var steps []string
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, umask %o, after\n  %s\n%s", seed, mask, strings.Join(steps, "\n  "), fmt.Sprintf(format, args...))
		}
		withUmask(mask, func() {
			for range 30 {
				name := names[r.IntN(len(names))]
				path := filepath.Join(dir, name)
				m, there := model[name]
				var want, got error
				switch op := r.IntN(8); op {
				case 0:
					text := texts[r.IntN(len(texts))]
					steps = append(steps, fmt.Sprintf("WriteFile(%s, %q)", name, text))
					got = FSWriteFile(path, text)
					mode := fresh
					if there {
						mode = m.mode
					}
					model[name] = fsModelFile{text, mode}
				case 1:
					text, perm := texts[r.IntN(len(texts))], perms[r.IntN(len(perms))]
					steps = append(steps, fmt.Sprintf("WriteFileMode(%s, %q, %o)", name, text, perm))
					got = FSWriteFileMode(path, text, int(perm))
					model[name] = fsModelFile{text, perm}
				case 2:
					text := texts[r.IntN(len(texts))]
					steps = append(steps, fmt.Sprintf("AppendFile(%s, %q)", name, text))
					got = FSAppendFile(path, text)
					if there && m.mode&0o200 == 0 && os.Geteuid() != 0 {
						want = Denied{Path: path}
					} else if there {
						model[name] = fsModelFile{m.text + text, m.mode}
					} else {
						model[name] = fsModelFile{text, fresh}
					}
				case 3:
					perm := perms[r.IntN(len(perms))]
					steps = append(steps, fmt.Sprintf("Chmod(%s, %o)", name, perm))
					got = FSChmod(path, int(perm))
					if there {
						model[name] = fsModelFile{m.text, perm}
					} else {
						want = NotFound{Path: path}
					}
				case 4:
					to := names[r.IntN(len(names))]
					steps = append(steps, fmt.Sprintf("Rename(%s, %s)", name, to))
					got = FSRename(path, filepath.Join(dir, to))
					if there {
						delete(model, name)
						model[to] = m
					} else {
						want = NotFound{Path: path}
					}
				case 5:
					steps = append(steps, fmt.Sprintf("Remove(%s)", name))
					got = FSRemove(path)
					if there {
						delete(model, name)
					} else {
						want = NotFound{Path: path}
					}
				case 6:
					steps = append(steps, fmt.Sprintf("ReadFile(%s)", name))
					text, err := FSReadFile(path)
					got = err
					if !there {
						want = NotFound{Path: path}
					} else if text != m.text {
						fail("ReadFile(%s) = %q; the model holds %q", name, text, m.text)
					}
				case 7:
					steps = append(steps, fmt.Sprintf("Stat(%s)", name))
					info, err := FSStat(path)
					got = err
					if !there {
						want = NotFound{Path: path}
					} else if info != (Info{Name: name, Size: len(m.text), Perm: int(m.mode)}) {
						fail("Stat(%s) = %#v; the model holds %d bytes, mode %o", name, info, len(m.text), m.mode)
					}
				}
				if got != want {
					fail("the last step gave %#v; the model gives %#v", got, want)
				}
				// Every name agrees with the model after every step: its
				// presence, its text and its mode.
				for _, n := range names {
					data, err := os.ReadFile(filepath.Join(dir, n))
					mf, ok := model[n]
					switch {
					case !ok && !errors.Is(err, fs.ErrNotExist):
						fail("%s is on disk (%v); the model has none", n, err)
					case ok && err != nil:
						fail("%s: %v; the model holds %q", n, err, mf.text)
					case ok && string(data) != mf.text:
						fail("%s holds %q; the model holds %q", n, data, mf.text)
					case ok && permOf(t, filepath.Join(dir, n)) != mf.mode:
						fail("%s has mode %o; the model has %o", n, permOf(t, filepath.Join(dir, n)), mf.mode)
					}
				}
			}
		})
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != len(model) {
			t.Fatalf("seed %d: %d entries on disk, %d in the model: a temporary file is left", seed, len(entries), len(model))
		}
	}
}

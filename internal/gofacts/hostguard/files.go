package hostguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"ergo.local/prototype/runtime/v1/hostfs"
)

// The guard of the file rows (HOST-ROW-2; gofacts.Hosts): each call runs
// in each host state's own working directory, which holds that state's
// own fixtures, and is given relative paths, so a call that follows the
// state reads or changes that state's files and no other. The guard sets
// a fixture up and reads the outcome back through Go calls that are not
// the row under test (os.OpenFile, File.Read, File.Stat on an absolute
// path), so no row vouches for itself. A row that returns a slice is
// checked to give the caller its own (a write to it is not seen by the
// next call). Each check removes what it made.

// fileCalls are the guarded file calls, one per file row.
func fileCalls() map[string]func(s ambient) error {
	return map[string]func(s ambient) error{
		"os.ReadFile":            readFileCheck(os.ReadFile),
		"os.WriteFile":           writeFileCheck,
		hostfs.Path + ".ReadDir": readDirCheck,
		hostfs.Path + ".Stat":    statCheck(hostfs.Stat, false),
		hostfs.Path + ".Lstat":   statCheck(hostfs.Lstat, true),
		hostfs.Path + ".Statfs":  statfsCheck,
		"os.Mkdir": func(s ambient) error {
			return madeDir(s, "hg-mkdir", "hg-mkdir", func() error { return os.Mkdir("hg-mkdir", 0o755) })
		},
		"os.MkdirAll": func(s ambient) error {
			return madeDir(s, "hg-mkdirall", "hg-mkdirall/a/b", func() error { return os.MkdirAll("hg-mkdirall/a/b", 0o755) })
		},
		"os.Remove": func(s ambient) error {
			return removed(s, "hg-remove", false, func() error { return os.Remove("hg-remove") })
		},
		"os.RemoveAll": func(s ambient) error {
			return removed(s, "hg-removeall", true, func() error { return os.RemoveAll("hg-removeall") })
		},
		"os.Rename":   renameCheck,
		"os.Readlink": readlinkCheck,
		"os.Chmod":    chmodCheck,
		// The path forms of (*os.File).Chdir and Chown (S2-FILE's
		// follow-up).
		"os.Chdir":  chdirCheck,
		"os.Chown":  chownCheck(os.Chown, false),
		"os.Lchown": chownCheck(os.Lchown, true),
	}
}

// chdirCheck: Chdir of a relative path enters the state's directory of
// that name, so a relative read then reads the state's file. The check
// returns to the state's directory.
func chdirCheck(s ambient) error {
	defer clean(s, "hg-chdir")
	defer os.Chdir(s.dir)
	if err := os.Mkdir(at(s, "hg-chdir"), 0o755); err != nil {
		return err
	}
	if err := put(s, "hg-chdir/inner", stateText(s)); err != nil {
		return err
	}
	if err := os.Chdir("hg-chdir"); err != nil {
		return err
	}
	if got, err := os.ReadFile("inner"); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after Chdir(hg-chdir) in %s, inner reads (%q, %v)", s.dir, got, err)
	}
	return nil
}

// chownCheck: Chown (Lchown) to the process's own ids of a relative path
// changes the state's file and leaves its contents; on a symbolic link to
// a missing file Chown follows the link and fails NotExist, and Lchown
// changes the link itself and succeeds.
func chownCheck(chown func(string, int, int) error, lchown bool) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-chown", "hg-chown-link")
		if err := put(s, "hg-chown", stateText(s)); err != nil {
			return err
		}
		if err := errors.Join(chown("hg-chown", os.Getuid(), os.Getgid()), chown("hg-chown", -1, -1)); err != nil {
			return fmt.Errorf("chown of hg-chown in %s to the process's ids fails: %v", s.dir, err)
		}
		if got, err := get(s, "hg-chown"); err != nil || !bytes.Equal(got, stateText(s)) {
			return fmt.Errorf("after chown the file is (%q, %v)", got, err)
		}
		if err := os.Symlink("hg-chown-missing", at(s, "hg-chown-link")); err != nil {
			return err
		}
		err := chown("hg-chown-link", -1, -1)
		if lchown && err != nil || !lchown && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("chown of a link to a missing file in %s is %v (lchown %v)", s.dir, err, lchown)
		}
		return nil
	}
}

// stateText is the content of s's fixture: it differs between the states,
// in its bytes and its length, so a call that keeps another state's file
// is seen.
func stateText(s ambient) []byte { return []byte("fixture of HOME=" + s.home + " TZ=" + s.tz + "\n") }

// at is name inside s's working directory, as an absolute path.
func at(s ambient, name string) string { return filepath.Join(s.dir, name) }

// put makes the file name in s's directory with text, through
// os.OpenFile (not a row under test).
func put(s ambient, name string, text []byte) error {
	f, err := os.OpenFile(at(s, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(text)
	return errors.Join(err, f.Close())
}

// get reads the file name in s's directory through os.Open.
func get(s ambient, name string) ([]byte, error) {
	f, err := os.Open(at(s, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// exists reports whether name is in s's directory, through os.Open.
func exists(s ambient, name string) bool {
	f, err := os.Open(at(s, name))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// clean removes what a check made, through the absolute path.
func clean(s ambient, names ...string) {
	for _, n := range names {
		os.RemoveAll(at(s, n))
	}
}

// readFileCheck: ReadFile of a relative path gives the state's fixture,
// and its result is the caller's.
func readFileCheck(readFile func(string) ([]byte, error)) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-read")
		if err := put(s, "hg-read", stateText(s)); err != nil {
			return err
		}
		got, err := readFile("hg-read")
		if err != nil || !bytes.Equal(got, stateText(s)) {
			return fmt.Errorf("ReadFile(hg-read) is (%q, %v) in %s, want its fixture %q", got, err, s.dir, stateText(s))
		}
		for i := range got {
			got[i] = 'x'
		}
		if again, err := readFile("hg-read"); err != nil || !bytes.Equal(again, stateText(s)) {
			return fmt.Errorf("ReadFile(hg-read) after a write to its last result is (%q, %v); the result is not the caller's", again, err)
		}
		return nil
	}
}

// writeFileCheck: WriteFile of a relative path writes the state's file,
// and keeps nothing of its argument.
func writeFileCheck(s ambient) error {
	defer clean(s, "hg-write")
	data := stateText(s)
	if err := os.WriteFile("hg-write", data, 0o644); err != nil {
		return err
	}
	want := slices.Clone(data)
	for i := range data {
		data[i] = 'x'
	}
	if got, err := get(s, "hg-write"); err != nil || !bytes.Equal(got, want) {
		return fmt.Errorf("WriteFile(hg-write) in %s left (%q, %v), want %q", s.dir, got, err, want)
	}
	return nil
}

// readDirCheck: ReadDir(".") lists the state's directory, and its result
// is the caller's.
func readDirCheck(s ambient) error {
	defer clean(s, "hg-dir")
	if err := os.Mkdir(at(s, "hg-dir"), 0o755); err != nil {
		return err
	}
	if err := put(s, "hg-dir/"+filepath.Base(s.home), stateText(s)); err != nil {
		return err
	}
	names := func(es []hostfs.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}
	first, err := hostfs.ReadDir("hg-dir")
	if want := []string{filepath.Base(s.home)}; err != nil || !slices.Equal(names(first), want) {
		return fmt.Errorf("ReadDir(hg-dir) is (%v, %v) in %s, want %v", names(first), err, s.dir, want)
	}
	fresh := names(first)
	for i := range first {
		first[i] = hostfs.Entry{}
	}
	if again, err := hostfs.ReadDir("hg-dir"); err != nil || !slices.Equal(names(again), fresh) {
		return fmt.Errorf("ReadDir(hg-dir) after a write to its last result is (%d entries, %v); the result is not the caller's", len(again), err)
	}
	return nil
}

// statCheck: Stat or Lstat of a relative path reads the state's file: its
// size, and, through a link, the link (Lstat) or its target (Stat).
func statCheck(stat func(string) (hostfs.Info, error), lstat bool) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-stat", "hg-stat-link")
		if err := put(s, "hg-stat", stateText(s)); err != nil {
			return err
		}
		if err := os.Symlink("hg-stat", at(s, "hg-stat-link")); err != nil {
			return err
		}
		fi, err := stat("hg-stat-link")
		if err != nil {
			return err
		}
		link := fi.Mode&fs.ModeSymlink != 0
		if link != lstat || fi.Name != "hg-stat-link" || fi.IsDir || !lstat && fi.Size != int64(len(stateText(s))) {
			return fmt.Errorf("stat(hg-stat-link) in %s is %+v, link %v; want the link %v, size %d", s.dir, fi, link, lstat, len(stateText(s)))
		}
		return nil
	}
}

// madeDir: the call makes the directory dir in the state's directory.
func madeDir(s ambient, top, dir string, call func() error) error {
	defer clean(s, top)
	if err := call(); err != nil {
		return err
	}
	f, err := os.Open(at(s, dir))
	if err != nil {
		return fmt.Errorf("after the call, %s in %s: %v", dir, s.dir, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.IsDir() {
		return fmt.Errorf("after the call, %s in %s is no directory (%v)", dir, s.dir, err)
	}
	return nil
}

// removed: the call removes name, a file or a tree, from the state's
// directory.
func removed(s ambient, name string, tree bool, call func() error) error {
	defer clean(s, name)
	if tree {
		if err := os.MkdirAll(at(s, name+"/a"), 0o755); err != nil {
			return err
		}
		if err := put(s, name+"/a/f", stateText(s)); err != nil {
			return err
		}
	} else if err := put(s, name, stateText(s)); err != nil {
		return err
	}
	if err := call(); err != nil {
		return err
	}
	if exists(s, name) {
		return fmt.Errorf("after the call, %s is still in %s", name, s.dir)
	}
	return nil
}

// renameCheck: Rename moves the state's file within its directory.
func renameCheck(s ambient) error {
	defer clean(s, "hg-from", "hg-to")
	if err := put(s, "hg-from", stateText(s)); err != nil {
		return err
	}
	if err := os.Rename("hg-from", "hg-to"); err != nil {
		return err
	}
	if got, err := get(s, "hg-to"); err != nil || !bytes.Equal(got, stateText(s)) || exists(s, "hg-from") {
		return fmt.Errorf("after Rename(hg-from, hg-to) in %s, hg-to is (%q, %v)", s.dir, got, err)
	}
	return nil
}

// readlinkCheck: Readlink reads the state's link, whose target differs
// between the states.
func readlinkCheck(s ambient) error {
	defer clean(s, "hg-link")
	target := "target-of-" + filepath.Base(s.home)
	if err := os.Symlink(target, at(s, "hg-link")); err != nil {
		return err
	}
	if got, err := os.Readlink("hg-link"); err != nil || got != target {
		return fmt.Errorf("Readlink(hg-link) is (%q, %v) in %s, want %q", got, err, s.dir, target)
	}
	return nil
}

// chmodCheck: Chmod changes the state's file's mode.
func chmodCheck(s ambient) error {
	defer clean(s, "hg-chmod")
	if err := put(s, "hg-chmod", stateText(s)); err != nil {
		return err
	}
	if err := os.Chmod("hg-chmod", 0o600); err != nil {
		return err
	}
	f, err := os.Open(at(s, "hg-chmod"))
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("after Chmod(hg-chmod, 0600) in %s, the mode is %v (%v)", s.dir, fi.Mode(), err)
	}
	return nil
}

// statfsCheck: Statfs of a relative path resolves in the active state's
// working directory and returns valid filesystem byte capacities.
func statfsCheck(s ambient) error {
	dir := "hg-statfs-" + filepath.Base(s.dir)
	defer clean(s, dir)
	if err := os.Mkdir(at(s, dir), 0o755); err != nil {
		return err
	}
	info, err := hostfs.Statfs(dir)
	if err != nil {
		return err
	}
	if info.Total <= 0 || info.Free < 0 || info.Available < 0 || info.Free > info.Total || info.Available > info.Total {
		return fmt.Errorf("Statfs(%s) in %s returned invalid metrics %+v", dir, s.dir, info)
	}
	if _, err := hostfs.Statfs("hg-statfs-missing"); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Statfs(hg-statfs-missing) in %s is %v, want NotExist", s.dir, err)
	}
	return nil
}

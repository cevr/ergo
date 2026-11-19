package hostguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"ergo.local/prototype/runtime/v1/hostfs"
)

// The guard of the open-file rows (HOST-ROW-3 slice 2; gofacts.Hosts):
// as the file rows' guard, each check runs in each host state's own
// working directory with its own fixture. Open, Create and OpenFile get a
// relative path, so a call that follows the state opens that state's
// file, and the handle each returns is new (a second open gives another
// that reads from the start). A method gets a handle opened on the
// absolute path, so it is checked on the file of its handle; the outcome
// is read back through calls that are not the row under test (put and
// get: os.OpenFile and File.Read on an absolute path, os.ReadFile). Read
// and ReadAt must write only b[:n] and keep no reference to b. Each check
// removes what it made.

// handleCalls are the guarded open-file calls, one per row.
func handleCalls() map[string]func(s ambient) error {
	return map[string]func(s ambient) error{
		"os.Open":             openCheck,
		"os.Create":           createCheck,
		"os.OpenFile":         openFileCheck,
		"os.File.Read":        readCheck,
		"os.File.ReadAt":      readAtCheck,
		"os.File.Write":       writeCheck(func(f *os.File, b []byte) (int, error) { return f.Write(b) }),
		"os.File.WriteString": writeCheck(func(f *os.File, b []byte) (int, error) { return f.WriteString(string(b)) }),
		"os.File.Seek":        seekCheck,
		"os.File.Close":       closeCheck,
		"os.File.Name":        nameCheck,
		"os.File.Sync":        syncCheck,
		"os.File.Truncate":    truncateCheck,
		// The rest of *os.File's methods (S2-FILE).
		"os.File.WriteAt":          writeAtCheck,
		"os.File.Chmod":            fileChmodCheck,
		"os.File.Chown":            fileChownCheck,
		"os.File.Chdir":            fileChdirCheck,
		"os.File.Readdirnames":     readdirnamesCheck,
		"os.File.SetDeadline":      deadlineCheck((*os.File).SetDeadline, true, true),
		"os.File.SetReadDeadline":  deadlineCheck((*os.File).SetReadDeadline, true, false),
		"os.File.SetWriteDeadline": deadlineCheck((*os.File).SetWriteDeadline, false, true),
		// The targets of (*os.File).Stat, ReadDir and Readdir (S2-FILE-B).
		hostfs.Path + ".FileStat":    fileStatCheck,
		hostfs.Path + ".FileReadDir": fileEntriesCheck(false),
		hostfs.Path + ".FileReaddir": fileEntriesCheck(true),
	}
}

// fileStatCheck: FileStat gives the metadata of its handle's file, the
// state's fixture (its name, its size, not a directory), and leaves the
// handle open at its offset.
func fileStatCheck(s ambient) error {
	defer clean(s, "hg-fstat")
	f, err := fixture(s, "hg-fstat", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := hostfs.FileStat(f)
	if err != nil || fi.Name != "hg-fstat" || fi.IsDir || fi.Size != int64(len(stateText(s))) || fi.Mode.Perm() == 0 {
		return fmt.Errorf("FileStat of %s/hg-fstat is (%+v, %v), want its name, size %d", s.dir, fi, err, len(stateText(s)))
	}
	if got, err := readAll(f); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after FileStat the handle reads (%q, %v)", got, err)
	}
	return nil
}

// fileEntriesCheck: FileReadDir (or FileReaddir, info) gives the entries
// of its handle's directory, at most 2 at a time, then io.EOF: the
// state's own file with its size, and a directory as one.
func fileEntriesCheck(info bool) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-fents")
		own := "of-" + filepath.Base(s.home)
		if err := os.MkdirAll(at(s, "hg-fents/sub"), 0o755); err != nil {
			return err
		}
		if err := errors.Join(put(s, "hg-fents/a", nil), put(s, "hg-fents/"+own, stateText(s))); err != nil {
			return err
		}
		f, err := os.Open(at(s, "hg-fents"))
		if err != nil {
			return err
		}
		defer f.Close()
		var got []string
		for {
			var names []string
			var err error
			if info {
				var is []hostfs.Info
				is, err = hostfs.FileReaddir(f, 2)
				for _, i := range is {
					names = append(names, fmt.Sprintf("%s %v %d", i.Name, i.IsDir, map[bool]int64{false: i.Size}[i.IsDir]))
				}
			} else {
				var es []hostfs.Entry
				es, err = hostfs.FileReadDir(f, 2)
				for _, e := range es {
					size := 0
					if e.Name == own {
						size = len(stateText(s))
					}
					names = append(names, fmt.Sprintf("%s %v %d", e.Name, e.IsDir, size))
				}
			}
			if len(names) > 2 {
				return fmt.Errorf("a read of 2 entries gives %q", names)
			}
			got = append(got, names...)
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		slices.Sort(got)
		want := []string{"a false 0", fmt.Sprintf("%s false %d", own, len(stateText(s))), "sub true 0"}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return fmt.Errorf("the entries of %s/hg-fents are %q, want %q", s.dir, got, want)
		}
		return nil
	}
}

// writeAtCheck: WriteAt puts b at off, leaves the handle's offset, and
// keeps no reference to b.
func writeAtCheck(s ambient) error {
	defer clean(s, "hg-fwriteat")
	f, err := fixture(s, "hg-fwriteat", os.O_WRONLY)
	if err != nil {
		return err
	}
	data := []byte("AB")
	n, werr := f.WriteAt(data, 2)
	for i := range data {
		data[i] = 'x'
	}
	_, w2 := f.Write([]byte("Z"))
	if err := errors.Join(werr, w2, f.Close()); err != nil || n != 2 {
		return fmt.Errorf("WriteAt(AB, 2) then Write(Z) is (%d, %v)", n, err)
	}
	text := stateText(s)
	want := append(append([]byte("Z"), text[1:2]...), "AB"...)
	want = append(want, text[4:]...)
	if got, err := get(s, "hg-fwriteat"); err != nil || !bytes.Equal(got, want) {
		return fmt.Errorf("after WriteAt(AB, 2) and Write(Z) in %s the file is (%q, %v), want %q", s.dir, got, err, want)
	}
	return nil
}

// fileChmodCheck: Chmod changes the mode of its handle's file.
func fileChmodCheck(s ambient) error {
	defer clean(s, "hg-fchmod")
	f, err := fixture(s, "hg-fchmod", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, m := range []os.FileMode{0o600, 0o640} {
		if err := f.Chmod(m); err != nil {
			return err
		}
		if fi, err := os.Stat(at(s, "hg-fchmod")); err != nil || fi.Mode().Perm() != m {
			return fmt.Errorf("after Chmod(%v) in %s the file's mode is %v (%v)", m, s.dir, fi.Mode(), err)
		}
	}
	return nil
}

// fileChownCheck: Chown to the process's own ids succeeds and leaves the
// file's contents; a negative id leaves that id.
func fileChownCheck(s ambient) error {
	defer clean(s, "hg-fchown")
	f, err := fixture(s, "hg-fchown", os.O_RDONLY)
	if err != nil {
		return err
	}
	if err := errors.Join(f.Chown(os.Getuid(), os.Getgid()), f.Chown(-1, -1), f.Close()); err != nil {
		return fmt.Errorf("Chown to the process's ids in %s fails: %v", s.dir, err)
	}
	if got, err := get(s, "hg-fchown"); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after Chown the file is (%q, %v)", got, err)
	}
	return nil
}

// fileChdirCheck: Chdir makes its handle's directory the working
// directory, so a relative path then resolves inside it. The check
// returns to the state's directory.
func fileChdirCheck(s ambient) error {
	defer clean(s, "hg-fchdir")
	defer os.Chdir(s.dir)
	if err := os.Mkdir(at(s, "hg-fchdir"), 0o755); err != nil {
		return err
	}
	if err := put(s, "hg-fchdir/inner", stateText(s)); err != nil {
		return err
	}
	f, err := os.Open(at(s, "hg-fchdir"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chdir(); err != nil {
		return err
	}
	if got, err := os.ReadFile("inner"); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after Chdir to %s/hg-fchdir, inner reads (%q, %v)", s.dir, got, err)
	}
	return nil
}

// readdirnamesCheck: Readdirnames gives the names of its handle's
// directory, n at a time, then io.EOF, in a slice the caller owns.
func readdirnamesCheck(s ambient) error {
	defer clean(s, "hg-fnames")
	if err := os.Mkdir(at(s, "hg-fnames"), 0o755); err != nil {
		return err
	}
	want := []string{"a", "b", "of-" + filepath.Base(s.home)}
	for _, n := range want {
		if err := put(s, "hg-fnames/"+n, nil); err != nil {
			return err
		}
	}
	slices.Sort(want)
	f, err := os.Open(at(s, "hg-fnames"))
	if err != nil {
		return err
	}
	defer f.Close()
	var got []string
	for {
		ns, err := f.Readdirnames(2)
		if len(ns) > 2 {
			return fmt.Errorf("Readdirnames(2) gives %q", ns)
		}
		got = append(got, ns...)
		if len(ns) > 0 {
			ns[0] = "x" // the caller's: the next call is not changed
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return fmt.Errorf("Readdirnames of %s/hg-fnames gives %q, want %q", s.dir, got, want)
	}
	return nil
}

// deadlineCheck: a deadline on a regular file is os.ErrNoDeadline; on a
// pipe's ends (os.Pipe, no row under test) a past deadline makes the
// next read (r) or write (w) fail with os.ErrDeadlineExceeded, and a zero
// one clears it.
func deadlineCheck(set func(*os.File, time.Time) error, r, w bool) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-fdeadline")
		f, err := fixture(s, "hg-fdeadline", os.O_RDONLY)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := set(f, time.Now()); !errors.Is(err, os.ErrNoDeadline) {
			return fmt.Errorf("a deadline on a regular file is %v, want os.ErrNoDeadline", err)
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		defer pr.Close()
		defer pw.Close()
		past := time.Unix(1, 0)
		if err := errors.Join(set(pr, past), set(pw, past)); err != nil {
			return err
		}
		_, werr := pw.Write([]byte("x"))
		if w != errors.Is(werr, os.ErrDeadlineExceeded) {
			return fmt.Errorf("a write after a past deadline is %v (the setter covers writes: %v)", werr, w)
		}
		if r || !w {
			// With no write deadline the byte the write put is there,
			// so only a read deadline makes the read fail.
			_, rerr := pr.Read(make([]byte, 1))
			if r != errors.Is(rerr, os.ErrDeadlineExceeded) || !r && rerr != nil {
				return fmt.Errorf("a read after a past deadline is %v (the setter covers reads: %v)", rerr, r)
			}
		}
		if err := errors.Join(set(pr, time.Time{}), set(pw, time.Time{})); err != nil {
			return err
		}
		if _, err := pw.Write([]byte("y")); err != nil {
			return fmt.Errorf("a write after the deadline is cleared fails: %v", err)
		}
		return nil
	}
}

// fixture puts the state's fixture at name and opens it on its absolute
// path through os.OpenFile (no row under test), with flag.
func fixture(s ambient, name string, flag int) (*os.File, error) {
	if err := put(s, name, stateText(s)); err != nil {
		return nil, err
	}
	return os.OpenFile(at(s, name), flag, 0)
}

// readAll reads f to its end through io.ReadAll.
func readAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }

// openCheck: Open of a relative path reads the state's fixture, and a
// second Open is another handle, at the file's start.
func openCheck(s ambient) error {
	defer clean(s, "hg-open")
	if err := put(s, "hg-open", stateText(s)); err != nil {
		return err
	}
	f, err := os.Open("hg-open")
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := readAll(f)
	if err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("Open(hg-open) reads (%q, %v) in %s, want %q", got, err, s.dir, stateText(s))
	}
	g, err := os.Open("hg-open")
	if err != nil {
		return err
	}
	defer g.Close()
	if again, err := readAll(g); err != nil || !bytes.Equal(again, stateText(s)) || f.Fd() == g.Fd() {
		return fmt.Errorf("a second Open(hg-open) reads (%q, %v), fd %d after %d; want a new handle at the start", again, err, g.Fd(), f.Fd())
	}
	return nil
}

// createCheck: Create of a relative path makes the state's file, empty
// and writable.
func createCheck(s ambient) error {
	defer clean(s, "hg-create")
	if err := put(s, "hg-create", []byte("old contents to truncate")); err != nil {
		return err
	}
	f, err := os.Create("hg-create")
	if err != nil {
		return err
	}
	_, werr := f.Write(stateText(s))
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	if got, err := get(s, "hg-create"); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after Create(hg-create) and a write in %s, the file is (%q, %v)", s.dir, got, err)
	}
	return nil
}

// openFileCheck: OpenFile of a relative path with O_APPEND appends to the
// state's file.
func openFileCheck(s ambient) error {
	defer clean(s, "hg-openfile")
	if err := put(s, "hg-openfile", stateText(s)); err != nil {
		return err
	}
	f, err := os.OpenFile("hg-openfile", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write([]byte("tail\n"))
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	want := append(stateText(s), "tail\n"...)
	if got, err := get(s, "hg-openfile"); err != nil || !bytes.Equal(got, want) {
		return fmt.Errorf("after OpenFile(hg-openfile, O_APPEND) in %s, the file is (%q, %v), want %q", s.dir, got, err, want)
	}
	return nil
}

// readCheck: Read fills b[:n] from the handle's offset, writes nothing
// past n, and keeps no reference to b: a later write to b changes
// nothing the next Read gives.
func readCheck(s ambient) error {
	defer clean(s, "hg-fread")
	f, err := fixture(s, "hg-fread", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	text := stateText(s)
	b := bytes.Repeat([]byte{'#'}, len(text)+8)
	n, err := f.Read(b[:4])
	if err != nil || n != 4 || !bytes.Equal(b[:4], text[:4]) || b[4] != '#' {
		return fmt.Errorf("Read(b[:4]) is (%d, %v), b %q, in %s; want the fixture's first 4 bytes and nothing past them", n, err, b, s.dir)
	}
	for i := range b {
		b[i] = 'x'
	}
	c := make([]byte, len(text))
	n, err = f.Read(c)
	if err != nil || !bytes.Equal(c[:n], text[4:]) {
		return fmt.Errorf("a second Read is (%d, %v), %q; want the rest of the fixture, unchanged by a write to the first b", n, err, c[:n])
	}
	return nil
}

// readAtCheck: ReadAt fills b from off, leaves the handle's offset, and
// keeps no reference to b.
func readAtCheck(s ambient) error {
	defer clean(s, "hg-freadat")
	f, err := fixture(s, "hg-freadat", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	text := stateText(s)
	b := make([]byte, 5)
	if n, err := f.ReadAt(b, 3); err != nil || n != 5 || !bytes.Equal(b, text[3:8]) {
		return fmt.Errorf("ReadAt(b, 3) is (%d, %v), %q, in %s; want %q", n, err, b, s.dir, text[3:8])
	}
	for i := range b {
		b[i] = 'x'
	}
	if got, err := readAll(f); err != nil || !bytes.Equal(got, text) {
		return fmt.Errorf("after ReadAt the handle reads (%q, %v) from its offset; want the whole fixture", got, err)
	}
	return nil
}

// writeCheck: Write or WriteString puts b at the handle's offset and
// keeps no reference to b.
func writeCheck(write func(*os.File, []byte) (int, error)) func(s ambient) error {
	return func(s ambient) error {
		defer clean(s, "hg-fwrite")
		f, err := fixture(s, "hg-fwrite", os.O_WRONLY)
		if err != nil {
			return err
		}
		data := []byte("NEW")
		n, werr := write(f, data)
		for i := range data {
			data[i] = 'x'
		}
		if err := errors.Join(werr, f.Close()); err != nil || n != 3 {
			return fmt.Errorf("write is (%d, %v)", n, err)
		}
		want := append([]byte("NEW"), stateText(s)[3:]...)
		if got, err := get(s, "hg-fwrite"); err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("after the write in %s the file is (%q, %v), want %q", s.dir, got, err, want)
		}
		return nil
	}
}

// seekCheck: Seek moves the handle's offset, so the next read starts there.
func seekCheck(s ambient) error {
	defer clean(s, "hg-fseek")
	f, err := fixture(s, "hg-fseek", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	if off, err := f.Seek(6, io.SeekStart); err != nil || off != 6 {
		return fmt.Errorf("Seek(6, start) is (%d, %v)", off, err)
	}
	if got, err := readAll(f); err != nil || !bytes.Equal(got, stateText(s)[6:]) {
		return fmt.Errorf("after Seek(6) the handle reads (%q, %v), want %q", got, err, stateText(s)[6:])
	}
	return nil
}

// closeCheck: Close closes its handle: a read on it then fails with
// os.ErrClosed, and the file is unchanged.
func closeCheck(s ambient) error {
	defer clean(s, "hg-fclose")
	f, err := fixture(s, "hg-fclose", os.O_RDONLY)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("a Read after Close fails with %v, want os.ErrClosed", err)
	}
	if got, err := get(s, "hg-fclose"); err != nil || !bytes.Equal(got, stateText(s)) {
		return fmt.Errorf("after Close the file is (%q, %v)", got, err)
	}
	return nil
}

// nameCheck: Name is the name the handle was opened with.
func nameCheck(s ambient) error {
	defer clean(s, "hg-fname")
	f, err := fixture(s, "hg-fname", os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	if got := f.Name(); got != at(s, "hg-fname") {
		return fmt.Errorf("Name() is %q, want %q", got, at(s, "hg-fname"))
	}
	return nil
}

// syncCheck: Sync succeeds and leaves what was written.
func syncCheck(s ambient) error {
	defer clean(s, "hg-fsync")
	f, err := fixture(s, "hg-fsync", os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	_, werr := f.Write([]byte("z"))
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if got, err := get(s, "hg-fsync"); err != nil || !bytes.Equal(got, append(stateText(s), 'z')) {
		return fmt.Errorf("after Sync the file is (%q, %v)", got, err)
	}
	return nil
}

// truncateCheck: Truncate sets the handle's file's size.
func truncateCheck(s ambient) error {
	defer clean(s, "hg-ftrunc")
	f, err := fixture(s, "hg-ftrunc", os.O_WRONLY)
	if err != nil {
		return err
	}
	if err := errors.Join(f.Truncate(5), f.Close()); err != nil {
		return err
	}
	if got, err := get(s, "hg-ftrunc"); err != nil || !bytes.Equal(got, stateText(s)[:5]) {
		return fmt.Errorf("after Truncate(5) the file is (%q, %v), want %q", got, err, stateText(s)[:5])
	}
	return nil
}

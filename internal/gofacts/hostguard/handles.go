package hostguard

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
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

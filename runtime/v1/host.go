package v1

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"iter"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// The bodies of std os (docs/spec/stdlib.md, host packages). Each is a
// top-level function of basic and slice types with no hidden argument, so
// BodyOf binds it as written. The names carry their std package, because
// the lowering keys runtime bindings by bare name. They read the host
// process, so none is pure.

// Exit is os.Exit, the failure that ends main with status Code. When it is
// main's only reason, report writes Message verbatim (docs/spec/runtime.md,
// Exit causes). It is a record of a v1 package, so the lowering binds
// os.Exit to this Go type (docs/spec/stdlib.md, Types 4).
type Exit struct {
	Code    int
	Message string
}

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's. report writes it when Exit is not main's only reason.
func (e Exit) Error() string {
	return "os.Exit{Code:" + strconv.Itoa(e.Code) + " Message:" + e.Message + "}"
}

// OSArgs is the body of os.Host's Args: the arguments after the program
// name, in a fresh slice. A process started with no argv at all has none.
func OSArgs() []string {
	if len(os.Args) < 2 {
		return []string{}
	}
	return append([]string{}, os.Args[1:]...)
}

// OSHasEnv reports whether the environment variable key is set, even to
// the empty string.
func OSHasEnv(key string) bool {
	_, ok := os.LookupEnv(key)
	return ok
}

// OSGetenv is the value of the environment variable key; it is empty when
// key is unset, so os.Host's Getenv asks OSHasEnv first.
func OSGetenv(key string) string { return os.Getenv(key) }

// The bodies of std fs (docs/spec/stdlib.md, fs). Each reads or writes
// the host file system and returns one of the three failures below, so
// none is pure. No body keeps a file open after it returns.

// NotFound is fs.NotFound: no file is at Path. It is a record of a v1
// package, so the lowering binds fs.NotFound to this Go type
// (docs/spec/stdlib.md, Types 4).
type NotFound struct{ Path string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (e NotFound) Error() string { return "fs.NotFound{Path:" + e.Path + "}" }

// Denied is fs.Denied: the process may not read Path.
type Denied struct{ Path string }

// Error is the failure's text, as NotFound's is.
func (e Denied) Error() string { return "fs.Denied{Path:" + e.Path + "}" }

// Failed is fs.Failed: any other failure at Path. Reason is the host's
// text for it without the operation and the path.
type Failed struct {
	Path   string
	Reason string
}

// Error is the failure's text, as NotFound's is.
func (e Failed) Error() string { return "fs.Failed{Path:" + e.Path + " Reason:" + e.Reason + "}" }

// Info is fs.Info: the last element of a path, its size in bytes,
// whether it is a directory, and its permission bits.
type Info struct {
	Name string
	Size int
	Dir  bool
	Perm int
}

// fsErr is the std fs failure of err, a failure of an operation at path:
// NotFound for fs.ErrNotExist, Denied for fs.ErrPermission, and Failed
// with the cause of a *fs.PathError or *os.LinkError (or err's text)
// otherwise.
func fsErr(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return NotFound{Path: path}
	case errors.Is(err, fs.ErrPermission):
		return Denied{Path: path}
	}
	return Failed{Path: path, Reason: fsReason(err)}
}

// fsReason is the host's text for err without the operation and the
// paths: the cause of a *fs.PathError or *os.LinkError, else err's text.
func fsReason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}

// FSReadFile is the body of fs.Host's ReadFile: the whole file at path as
// text.
func FSReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fsErr(path, err)
	}
	return string(data), nil
}

// FSReadDir is the body of fs.Host's ReadDir: the names of the entries of
// the directory at path, sorted by name (Go's os.ReadDir sorts them).
func FSReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fsErr(path, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// FSStat is the body of fs.Host's Stat: the file at path, after symbolic
// links.
func FSStat(path string) (Info, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Info{}, fsErr(path, err)
	}
	return Info{Name: fi.Name(), Size: int(fi.Size()), Dir: fi.IsDir(), Perm: int(fi.Mode().Perm())}, nil
}

// beforeRename, when it is set, runs on the complete, closed new file of
// FSWriteFile just before the rename. An error from it stops the write at
// that point, as a crash there would, so a test sees the old file whole
// and the new file removed. Only tests set it.
var beforeRename func(tmp string) error

// FSWriteFile is the body of fs.Host's WriteFile: path holds text after
// it. It makes a new file in the directory of path (so the rename stays on
// one device), writes text, syncs it, gives it the mode of the file at
// path if there is one, closes it and renames it over path. So a reader
// sees the old text or the new, never a part. A new file is opened with
// mode 0666, so the umask applies, as for Go's os.WriteFile. On any
// failure the new file is removed. A symbolic link at path is followed,
// as Go's os.WriteFile and Effect's FileSystem.writeFile follow it: the
// file the link names gets the text and the link stays (a link to a
// missing name makes that file; a link to a directory is Failed "is a
// directory", as a directory is). The new file is made beside the file
// the link names, so the rename replaces that file, not the link.
func FSWriteFile(path string, text string) error {
	return writeFile(path, text, 0, false)
}

// FSWriteFileMode is the body of fs.Host's WriteFileMode: FSWriteFile,
// but the file at path ends with exactly the permission bits perm, also
// when a file was there. The new file is opened with perm (the umask can
// only narrow it), then given exactly perm before any byte is written, so
// no reader outside perm can open it at any point.
func FSWriteFileMode(path string, text string, perm int) error {
	return writeFile(path, text, fs.FileMode(perm)&fs.ModePerm, true)
}

// afterCreate, when it is set, runs on the new file of writeFile just
// after it is created, before its mode is set and before any byte is
// written. Only tests set it, to see the mode a file is created with.
var afterCreate func(tmp string)

// writeFile writes text to path through a new file renamed over it. With
// exact, the new file is created with perm and given exactly perm before
// it is written; otherwise it is created with 0666 less the umask and
// takes the mode of the file at path, if one is there.
func writeFile(path string, text string, perm fs.FileMode, exact bool) error {
	dest, err := followLink(path)
	if err != nil {
		return fsErr(path, err)
	}
	mode, keep := perm, exact
	switch fi, err := os.Stat(dest); {
	case err == nil && fi.IsDir():
		return Failed{Path: path, Reason: "is a directory"}
	case err == nil && !exact:
		mode, keep = fi.Mode().Perm(), true
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fsErr(path, err)
	}
	create := fs.FileMode(0o666)
	if exact {
		create = perm
	}
	f, err := createSibling(dest, create)
	if err != nil {
		return fsErr(path, err)
	}
	tmp := f.Name()
	if afterCreate != nil {
		afterCreate(tmp)
	}
	if exact {
		// Before the first byte: the umask may have narrowed perm.
		err = f.Chmod(mode)
	}
	if err == nil {
		err = fillSibling(f, text, mode, keep && !exact)
	} else {
		_ = f.Close()
	}
	if err == nil && beforeRename != nil {
		err = beforeRename(tmp)
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fsErr(path, err)
	}
	return nil
}

// maxLinks is the most symbolic links followLink follows; one more is
// ELOOP, as for Linux's open.
const maxLinks = 40

// followLink is the path of the file that an open of path for writing
// reaches: while the last element of path is a symbolic link, it is the
// link's text, read relative to the directory of the link. It does not
// clean the path, so ".." after a linked directory stays the host's to
// resolve, as it is for open. A path that is no link, or that Lstat cannot
// read, is returned as it is; the Stat after it reports the failure.
func followLink(path string) (string, error) {
	for n := 0; ; n++ {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		if n == maxLinks {
			return "", &fs.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
		}
		text, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(text) {
			text = path[:strings.LastIndexByte(path, os.PathSeparator)+1] + text
		}
		path = text
	}
}

// createSibling creates a new file beside path, named path.tmpN for a
// random N, with mode perm less the umask. It retries a name that exists.
func createSibling(path string, perm fs.FileMode) (*os.File, error) {
	for i := 0; ; i++ {
		name := path + ".tmp" + strconv.FormatUint(uint64(rand.Uint32()), 10)
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) && i < 100 {
			continue
		}
		return f, err
	}
}

// fillSibling writes text to f, syncs it, sets mode when keep is true, and
// closes f in every case.
func fillSibling(f *os.File, text string, mode fs.FileMode, keep bool) error {
	_, err := f.WriteString(text)
	if err == nil {
		err = f.Sync()
	}
	if err == nil && keep {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// FSMkdirAll is the body of fs.Host's MkdirAll: os.MkdirAll with mode
// 0777, so the umask applies. Its row has no NotFound, so a missing path
// (the empty path, or a parent removed during the call) is Failed with the
// host's reason.
func FSMkdirAll(path string) error {
	err := os.MkdirAll(path, 0o777)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return Failed{Path: path, Reason: fsReason(err)}
	}
	return fsErr(path, err)
}

// FSMkdirAllMode is the body of fs.Host's MkdirAllMode: FSMkdirAll, but
// each directory it makes is made with perm (the umask can only narrow
// it) and then given exactly perm. A directory that is there keeps its
// mode. Its row is FSMkdirAll's.
func FSMkdirAllMode(path string, perm int) error {
	err := mkdirAllExact(path, fs.FileMode(perm)&fs.ModePerm)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return Failed{Path: path, Reason: fsReason(err)}
	}
	return fsErr(path, err)
}

// mkdirAllExact makes path and each missing parent, as os.MkdirAll does,
// and gives each directory it makes exactly perm.
func mkdirAllExact(path string, perm fs.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		if fi.IsDir() {
			return nil
		}
		return &fs.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR}
	}
	trimmed := strings.TrimRight(path, string(os.PathSeparator))
	if parent := filepath.Dir(trimmed); trimmed != "" && parent != trimmed && parent != "." {
		if err := mkdirAllExact(parent, perm); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, perm); err != nil {
		// Another writer may have made it since the Stat.
		if fi, serr := os.Lstat(path); serr == nil && fi.IsDir() {
			return nil
		}
		return err
	}
	return os.Chmod(path, perm)
}

// FSAppendFile is the body of fs.Host's AppendFile: text at the end of
// the file at path, in one write of a file opened with O_APPEND, so two
// appends never interleave their texts on a local file system. A missing
// file is made with mode 0666 less the umask, as Go's os.OpenFile with
// O_CREATE makes it. The file is not synced.
func FSAppendFile(path string, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return fsErr(path, err)
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fsErr(path, err)
	}
	return nil
}

// FSChmod is the body of fs.Host's Chmod: os.Chmod with the permission
// bits perm. A symbolic link is followed.
func FSChmod(path string, perm int) error {
	if err := os.Chmod(path, fs.FileMode(perm)&fs.ModePerm); err != nil {
		return fsErr(path, err)
	}
	return nil
}

// FSRename is the body of fs.Host's Rename: os.Rename. The host says only
// that a name is missing, not which: NotFound names from when nothing is
// at from, and to otherwise (a missing directory on the way to to). Every
// other failure names from.
func FSRename(from string, to string) error {
	err := os.Rename(from, to)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if _, lerr := os.Lstat(from); lerr == nil {
			return NotFound{Path: to}
		}
		return NotFound{Path: from}
	}
	return fsErr(from, err)
}

// FSRemove is the body of fs.Host's Remove: os.Remove, which removes a
// file, a symbolic link (not what it names) or an empty directory.
func FSRemove(path string) error {
	if err := os.Remove(path); err != nil {
		return fsErr(path, err)
	}
	return nil
}

// The bodies of std io (docs/spec/stdlib.md, io). Each writes or reads a
// standard stream of the host process, so none is pure. A write is one
// unbuffered WriteString, as Console.Println is, so writes of Console and
// io keep program order. A read of standard input waits for the host and
// cannot be stopped: it is an uninterruptible host wait, as a read of a
// file is.

// ReadFailed is io.ReadFailed: a read of standard input failed. It is a
// record of a v1 package, so the lowering binds io.ReadFailed to this Go
// type (docs/spec/stdlib.md, Types 4).
type ReadFailed struct {
	Reason string
}

// Error is the failure's text, its Ergo name and field.
func (e ReadFailed) Error() string { return "io.ReadFailed{Reason:" + e.Reason + "}" }

// IOPrint is the body of io.Host's Print: text on standard output, as it
// is, in one write.
func IOPrint(text string) {
	_, _ = os.Stdout.WriteString(text)
}

// IOEprint is the body of io.Errors' Eprint: text on standard error, as it
// is, in one write.
func IOEprint(text string) {
	_, _ = os.Stderr.WriteString(text)
}

// IOIsTerminal is the body of io.Host's IsTerminal: whether standard
// output is a character device, a terminal.
func IOIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// IOReadAll is the body of io.Host's ReadAll: the rest of standard input,
// from where the last read stopped.
func IOReadAll() (string, error) {
	return stdinReader().readAll()
}

// IOLines is the body of io.Host's Lines: the rest of standard input, one
// line at a time, each without its line feed; a last line with no line
// feed is a line. A line has no length limit (bufio.Reader, not
// bufio.Scanner). A read failure other than the end of the input panics:
// a Seq has no failure row, and ending it early would hide the failure.
// A break leaves the rest of the input in the shared reader for the next
// read.
func IOLines() iter.Seq[string] {
	return stdinReader().lines()
}

// lineReader is one buffered reader that every read of its input goes
// through, so a read starts where the one before it stopped: the bytes
// that the buffer read ahead stay for the next read. Its lock makes each
// read whole when two tasks read at once.
type lineReader struct {
	mu sync.Mutex
	br *bufio.Reader
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReader(r)}
}

// stdin is the process's one lineReader over os.Stdin, and stdinFile the
// os.Stdin it reads. A Go test that sets os.Stdin to a new file gets a new
// reader; a program never sets it.
var (
	stdinMu   sync.Mutex
	stdin     *lineReader
	stdinFile *os.File
)

// stdinReader is the shared reader of standard input.
func stdinReader() *lineReader {
	stdinMu.Lock()
	defer stdinMu.Unlock()
	if stdin == nil || stdinFile != os.Stdin {
		stdin, stdinFile = newLineReader(os.Stdin), os.Stdin
	}
	return stdin
}

// readAll is the rest of the input.
func (l *lineReader) readAll() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := io.ReadAll(l.br)
	if err != nil {
		return "", ReadFailed{Reason: fsReason(err)}
	}
	return string(data), nil
}

// readLine is the next line with its line feed, if it has one.
func (l *lineReader) readLine() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.br.ReadString('\n')
}

// lines is the rest of the input, as IOLines gives it.
func (l *lineReader) lines() iter.Seq[string] {
	return func(yield func(string) bool) {
		for {
			line, err := l.readLine()
			if err != nil && err != io.EOF {
				panic("io.Stdin.Lines: " + err.Error())
			}
			if strings.HasSuffix(line, "\n") {
				if !yield(line[:len(line)-1]) {
					return
				}
			} else if line != "" && !yield(line) {
				return
			}
			if err == io.EOF {
				return
			}
		}
	}
}

package v1

import (
	"bufio"
	"bytes"
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

	"ergo.local/prototype/runtime/rt"
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

// Info is fs.Info: the last element of a path, its size in bytes, and
// whether it is a directory.
type Info struct {
	Name string
	Size int
	Dir  bool
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
	return Info{Name: fi.Name(), Size: int(fi.Size()), Dir: fi.IsDir()}, nil
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
	dest, err := followLink(path)
	if err != nil {
		return fsErr(path, err)
	}
	var mode fs.FileMode
	keep := false
	switch fi, err := os.Stat(dest); {
	case err == nil && fi.IsDir():
		return Failed{Path: path, Reason: "is a directory"}
	case err == nil:
		mode, keep = fi.Mode().Perm(), true
	case !errors.Is(err, fs.ErrNotExist):
		return fsErr(path, err)
	}
	f, err := createSibling(dest)
	if err != nil {
		return fsErr(path, err)
	}
	tmp := f.Name()
	err = fillSibling(f, text, mode, keep)
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
// random N, with mode 0666 less the umask. It retries a name that exists.
func createSibling(path string) (*os.File, error) {
	for i := 0; ; i++ {
		name := path + ".tmp" + strconv.FormatUint(uint64(rand.Uint32()), 10)
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
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

// The bodies of std io (docs/spec/stdlib.md, io). Each writes or reads a
// standard stream of the host process, so none is pure. A write is one
// unbuffered WriteString, as Console.Println is, so writes of Console and
// io keep program order. A read of standard input waits for the host on
// the caller's task, and a stop of that task ends the wait (io 3; batch-12
// C3); a read of a file is still an uninterruptible host wait.

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

// IOReadAll is the body of io.Host's ReadAll on t, the caller's task: the
// rest of standard input, from where the last read stopped. A stop of t
// ends the wait and raises rt.Interrupted; the text it had read stays for
// the next read (stdlib.md, io 3).
func IOReadAll(t *rt.Task) (string, error) {
	return stdinReader().readAll(t)
}

// IOLines is the body of io.Host's Lines on t, the iterating task: the
// rest of standard input, one line at a time, each without its line feed;
// a last line with no line feed is a line. A line has no length limit
// (bufio.Reader, not bufio.Scanner). A read failure other than the end of
// the input panics: a Seq has no failure row, and ending it early would
// hide the failure. A break leaves the rest of the input in the shared
// reader for the next read. Each step is an interruption point of t: a
// stop ends the wait for the next line and raises rt.Interrupted, and the
// line read after the stop stays for the next read.
func IOLines(t *rt.Task) iter.Seq[string] {
	return stdinReader().lines(t)
}

// lineReader is one buffered reader that every read of its input goes
// through, so a read starts where the one before it stopped: the bytes
// that the buffer read ahead stay for the next read.
//
// A read that may wait on the host runs on a goroutine of its own, at most
// one at a time (reading), so the waiting task can leave on a stop while
// the read goes on: the line it lands goes to pending, which the next read
// takes first, so no byte is lost and the order holds. A line that the
// buffer holds whole is read in place, since that read cannot wait. Its
// lock makes each line whole when two tasks read at once. Prior art: Go's
// reader goroutine with a select on its channel and ctx.Done(); Effect's
// Terminal.readInput, a stream that interruption ends.
type lineReader struct {
	mu      sync.Mutex
	br      *bufio.Reader // read by the reading goroutine alone while reading
	pending []string      // the lines read and not taken, each with its line feed if it had one
	err     error         // the read error after pending, taken once (io.EOF at the end)
	reading bool          // a read goroutine is out
	landed  chan struct{} // closed when the read out lands
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReader(r)}
}

// next is the next line, with its line feed if it has one, or the read
// error (io.EOF at the end of the input). ok is false when stop closes
// first: then nothing is taken.
func (l *lineReader) next(stop <-chan struct{}) (line string, err error, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		switch {
		case len(l.pending) > 0:
			line = l.pending[0]
			l.pending = l.pending[1:]
			return line, nil, true
		case l.err != nil:
			err, l.err = l.err, nil
			return "", err, true
		case !l.reading && l.whole():
			line, err = l.br.ReadString('\n')
			return line, err, true
		case !l.reading:
			l.reading = true
			l.landed = make(chan struct{})
			go l.read()
		}
		landed := l.landed
		l.mu.Unlock()
		select {
		case <-landed:
		case <-stop:
			l.mu.Lock()
			return "", nil, false
		}
		l.mu.Lock()
	}
}

// whole reports whether the buffer holds a whole line, so a read cannot
// wait. l.mu is held and no read is out.
func (l *lineReader) whole() bool {
	b, _ := l.br.Peek(l.br.Buffered())
	return bytes.IndexByte(b, '\n') >= 0
}

// read is the read goroutine: it reads one line, which may wait on the
// host, and lands it.
func (l *lineReader) read() {
	line, err := l.br.ReadString('\n')
	l.mu.Lock()
	if line != "" {
		l.pending = append(l.pending, line)
	}
	l.err = err
	l.reading = false
	close(l.landed)
	l.mu.Unlock()
}

// unread puts text, read and not delivered, back in front of the lines
// not taken, one line each.
func (l *lineReader) unread(text string) {
	if text == "" {
		return
	}
	var lines []string
	for text != "" {
		i := strings.IndexByte(text, '\n') + 1
		if i == 0 {
			i = len(text)
		}
		lines = append(lines, text[:i])
		text = text[i:]
	}
	l.mu.Lock()
	l.pending = append(lines, l.pending...)
	l.mu.Unlock()
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

// readAll is the rest of the input, read on t. A stop of t puts back what
// it read and raises rt.Interrupted.
func (l *lineReader) readAll(t *rt.Task) (string, error) {
	t.Check()
	stop := t.Done()
	var b strings.Builder
	for {
		line, err, ok := l.next(stop)
		if !ok {
			l.unread(b.String())
			panic(rt.Interrupted)
		}
		b.WriteString(line)
		switch {
		case err == io.EOF:
			return b.String(), nil
		case err != nil:
			return "", ReadFailed{Reason: fsReason(err)}
		}
	}
}

// lines is the rest of the input, as IOLines gives it, read on t.
func (l *lineReader) lines(t *rt.Task) iter.Seq[string] {
	return func(yield func(string) bool) {
		for {
			t.Check()
			line, err, ok := l.next(t.Done())
			switch {
			case !ok:
				panic(rt.Interrupted)
			case err == io.EOF:
				return
			case err != nil:
				panic("io.Stdin.Lines: " + err.Error())
			}
			if !yield(strings.TrimSuffix(line, "\n")) {
				return
			}
		}
	}
}

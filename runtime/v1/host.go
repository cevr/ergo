package v1

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"strconv"
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
// failure the new file is removed. A symbolic link at path is replaced by
// a regular file, not followed; its target keeps its text.
func FSWriteFile(path string, text string) error {
	var mode fs.FileMode
	keep := false
	switch fi, err := os.Stat(path); {
	case err == nil && fi.IsDir():
		return Failed{Path: path, Reason: "is a directory"}
	case err == nil:
		mode, keep = fi.Mode().Perm(), true
	case !errors.Is(err, fs.ErrNotExist):
		return fsErr(path, err)
	}
	f, err := createSibling(path)
	if err != nil {
		return fsErr(path, err)
	}
	tmp := f.Name()
	err = fillSibling(f, text, mode, keep)
	if err == nil && beforeRename != nil {
		err = beforeRename(tmp)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fsErr(path, err)
	}
	return nil
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

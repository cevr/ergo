package v1

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of std fs's bounded mapping (CAP-FILE): MapBounded opens a
// file once, checks the open descriptor (regular, at most a limit), and
// maps it, keeping the file's identity and its path as the host names it,
// so a file swapped in after a stat by path is never read, and a later
// path is compared with the file that was read (SameAs).

// NotRegular is fs.NotRegular: MapBounded's failure for a path that is
// not a regular file (a directory, a pipe, a device).
type NotRegular struct{ Path string }

// Error is the failure's text, as NotFound's is.
func (e NotRegular) Error() string { return "fs.NotRegular{Path:" + e.Path + "}" }

// TooLarge is fs.TooLarge: MapBounded's failure for a file of more than
// Limit bytes; Size is the file's size when it was opened.
type TooLarge struct {
	Path  string
	Size  int
	Limit int
}

// Error is the failure's text, as NotFound's is.
func (e TooLarge) Error() string {
	return "fs.TooLarge{Path:" + e.Path + " Size:" + strconv.Itoa(e.Size) + " Limit:" + strconv.Itoa(e.Limit) + "}"
}

// FSMapBounded is the body of fs.MapBounded: the Mapped of the regular
// file at path, of at most limit bytes. It opens the file non-blocking (a
// pipe put at path never blocks the open), describes the open descriptor,
// and fails NotRegular for any file but a regular one and TooLarge for
// one over limit, before anything is read. The Mapped keeps the file's
// identity (SameAs) and the path the host gives for the descriptor
// (PhysicalPath).
//
//ergo:host
//ergo:wait none
func FSMapBounded(path string, limit int) (*Mapped, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fsErr(path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fsErr(path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, NotRegular{Path: path}
	}
	size := info.Size()
	if size > int64(limit) || int64(int(size)) != size {
		return nil, TooLarge{Path: path, Size: int(size), Limit: limit}
	}
	m := &Mapped{phys: physicalPath(f, path)}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		m.dev, m.ino, m.known = uint64(st.Dev), uint64(st.Ino), true
	}
	if size == 0 {
		return m, nil
	}
	data, err := fsMap(f, int(size))
	if err != nil {
		return nil, Failed{Path: path, Reason: err.Error()}
	}
	m.text, m.data = unsafe.String(&data[0], len(data)), data
	m.id = rt.MapRegister(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), path)
	return m, nil
}

// FSSameAs is the body of fs.SameAs: whether the file at path, its links
// followed (as fs.SameFile reads a path), is the file m was mapped from.
// A Mapped that kept no identity (MapText, MapFile) is the same as no
// file.
//
//ergo:host
//ergo:wait none
func FSSameAs(m *Mapped, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fsErr(path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && m.known && uint64(st.Dev) == m.dev && uint64(st.Ino) == m.ino, nil
}

// Size is the length of the mapped text in bytes: the file's size when it
// was opened.
//
//ergo:wait none
func (m *Mapped) Size() int { return len(m.text) }

// PhysicalPath is the absolute path the host gave for the file when it
// was opened by MapBounded (its links resolved), or "" for a Mapped that
// MapText or MapFile made.
//
//ergo:wait none
func (m *Mapped) PhysicalPath() string { return m.phys }

// absResolved is path with its links resolved and made absolute: the
// physical path where the host has no name for a descriptor.
func absResolved(path string) string {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		p = path
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Package hostfs gives a file's metadata as values (HOST-ROW-3). Go's
// os.Stat, os.Lstat and os.ReadDir return fs.FileInfo and fs.DirEntry,
// interfaces whose methods any Go type may implement: a row on those
// methods would vouch for every implementation a value can carry. Here
// each function calls os's, reads the result's methods once, on the
// implementation os itself returns, and returns a struct the caller owns,
// so reading a field is a field access and needs no row at all.
//
// Each function is whole in its own declaration (no helper), so a row's
// source hash covers everything the row vouches for.
//
// It is plain Go: no hidden argument, no runtime/v1 import, no state of
// its own. It is not a runtime body package (v1.Owns): its functions are
// bound as any Go package's are, and each is a host row (gofacts.Hosts)
// with its reason, its source hash and its guard (hostguard), the review
// a user's Go package gets.
package hostfs

import (
	"io/fs"
	"os"
	"time"
)

// Path is the package's import path.
const Path = "ergo.local/prototype/runtime/v1/hostfs"

// Info is one file's metadata, as fs.FileInfo's methods give it.
type Info struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

// Entry is one directory entry, as fs.DirEntry's methods give it without
// a further stat: its name, whether it is a directory, and its type bits.
type Entry struct {
	Name  string
	IsDir bool
	Type  fs.FileMode
}

// Stat is os.Stat's metadata of name, following a symbolic link.
func Stat(name string) (Info, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}, nil
}

// Lstat is os.Lstat's metadata of name, not following a symbolic link.
func Lstat(name string) (Info, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}, nil
}

// ReadDir is os.ReadDir's entries of the directory name, sorted by name;
// on an error it returns the entries it read before it, as os.ReadDir does.
func ReadDir(name string) ([]Entry, error) {
	ds, err := os.ReadDir(name)
	es := make([]Entry, len(ds))
	for i, d := range ds {
		es[i] = Entry{Name: d.Name(), IsDir: d.IsDir(), Type: d.Type()}
	}
	return es, err
}

// The methods of *os.File that return fs.FileInfo or fs.DirEntry
// (S2-FILE-B): each is bound as the method itself, `f.Stat()`, whose
// target is the function here, called with the handle first
// (gobind.ValueResults). None keeps the handle.

// FileStat is (*os.File).Stat's metadata of f's file (fstat).
func FileStat(f *os.File) (Info, error) {
	fi, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	return Info{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}, nil
}

// FileReadDir is (*os.File).ReadDir: the next entries of f's directory
// from the handle's position, at most n when n > 0 (then io.EOF at the
// end), else all of the rest, in directory order. On an error it returns
// the entries it read before it, as Go's does.
func FileReadDir(f *os.File, n int) ([]Entry, error) {
	ds, err := f.ReadDir(n)
	es := make([]Entry, len(ds))
	for i, d := range ds {
		es[i] = Entry{Name: d.Name(), IsDir: d.IsDir(), Type: d.Type()}
	}
	return es, err
}

// FileReaddir is (*os.File).Readdir: as FileReadDir, with each entry's
// metadata (an lstat of it), as Go's does.
func FileReaddir(f *os.File, n int) ([]Info, error) {
	fis, err := f.Readdir(n)
	is := make([]Info, len(fis))
	for i, fi := range fis {
		is[i] = Info{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime(), IsDir: fi.IsDir()}
	}
	return is, err
}

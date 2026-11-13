package sketchfront

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// GoPackage is the loader's check.GoSource: the non-test Go files of a Go
// package of the module's own, the Go module of the go.mod beside ergo.mod
// (docs/spec/interop.md, Trusted Go 3). A trusted package's extern binds a
// body there under the hidden-argument convention; the checker parses the
// files and runs no Go tool. Each file read joins the program's texts, so
// the analysed revision, and the build key with it, changes with the
// bodies the lowering calls by.
func (l *loader) GoPackage(importPath string) (fs.FS, bool) {
	if l.goMod == nil || l.goMod.module == "" || strings.Contains(importPath, "..") {
		return nil, false
	}
	rel, ok := strings.CutPrefix(importPath, l.goMod.module+"/")
	if !ok || rel == "" {
		return nil, false
	}
	entries, err := l.fsys.ReadDir(filepath.Join(l.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, false
	}
	files := goFiles{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := l.fsys.ReadFile(filepath.Join(l.root, filepath.FromSlash(rel), name))
		if err != nil {
			return nil, false
		}
		l.texts[path.Join(rel, name)] = string(data)
		files[name] = goFile{entry: e, data: data}
	}
	if len(files) == 0 {
		return nil, false
	}
	return files, true
}

// goFiles are one Go package's files by name, the fs.FS v1.ReadBodies
// reads: it lists and reads them, and opens nothing.
type goFiles map[string]goFile

type goFile struct {
	entry fs.DirEntry
	data  []byte
}

var errNoOpen = errors.New("sketchfront: a Go package's files are read whole")

func (g goFiles) Open(string) (fs.File, error) { return nil, errNoOpen }

func (g goFiles) ReadDir(dir string) ([]fs.DirEntry, error) {
	if dir != "." {
		return nil, fs.ErrNotExist
	}
	var out []fs.DirEntry
	for _, f := range g {
		out = append(out, f.entry)
	}
	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}

func (g goFiles) ReadFile(name string) ([]byte, error) {
	f, ok := g[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return f.data, nil
}

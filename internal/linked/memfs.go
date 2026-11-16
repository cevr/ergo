package linked

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing/fstest"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketchfront"
)

// memRoot is where a module is checked: an absolute folder that names no
// folder of the host, so nothing outside the module is read.
const memRoot = "/linked"

// memFS is a module's files in memory, at memRoot, for sketchfront's
// loader.
type memFS struct{ m fstest.MapFS }

func (f memFS) rel(name string) string {
	r := strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), memRoot), "/")
	if r == "" {
		return "."
	}
	return r
}

func (f memFS) ReadDir(dir string) ([]fs.DirEntry, error) { return fs.ReadDir(f.m, f.rel(dir)) }
func (f memFS) ReadFile(name string) ([]byte, error)      { return fs.ReadFile(f.m, f.rel(name)) }
func (f memFS) Stat(name string) (fs.FileInfo, error)     { return fs.Stat(f.m, f.rel(name)) }
func (f memFS) Lstat(name string) (fs.FileInfo, error)    { return fs.Stat(f.m, f.rel(name)) }

// checkModule checks the module whose files are files (by path in the
// module) in memory, as the program whose root package is the folder
// exports ("" is the module's root).
func checkModule(files map[string]string, exports string) (sketchfront.Analysis, error) {
	m := fstest.MapFS{}
	for p, text := range files {
		m[p] = &fstest.MapFile{Data: []byte(text), Mode: 0o644}
	}
	return sketchfront.CheckDirFS(memFS{m}, path.Join(memRoot, exports), gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
}

// readModule is the files of the module at dir on the host, by path in
// the module, without the folders in skip (the generated ones).
func readModule(dir string, skip map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skip[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if path.Ext(rel) != ".ergo" && path.Base(rel) != "ergo.mod" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	return out, err
}

package sketchfront

import (
	"bytes"
	"cmp"
	"errors"
	"go/build"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/fingerprint"
)

// GoPackage is the loader's check.GoSource: the non-test Go files of a Go
// package of the module's own, the Go module of the go.mod beside ergo.mod
// (docs/spec/interop.md, Trusted Go 3). A trusted package's extern binds a
// body there under the hidden-argument convention; the checker parses the
// files and runs no Go tool. Each file read joins the program's texts, so
// the analysed revision, and the build key with it, changes with the
// bodies the lowering calls by.
func (l *loader) GoPackage(importPath string) (fs.FS, bool) {
	files, ok := l.goPackage(importPath)
	parts := []string{}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		parts = append(parts, name, string(files[name].data))
	}
	if l.goBodies == nil {
		l.goBodies = map[string]string{}
	}
	l.goBodies[importPath] = ""
	if ok {
		l.goBodies[importPath] = fingerprint.Of(parts...)
		return files, true
	}
	return nil, false
}

// goPackage is GoPackage's files, unrecorded.
func (l *loader) goPackage(importPath string) (goFiles, bool) {
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
		if !l.buildsFor(name, data) {
			continue // go build leaves the file out for this target
		}
		l.texts[path.Join(rel, name)] = string(data)
		files[name] = goFile{entry: e, data: data}
	}
	if len(files) == 0 {
		return nil, false
	}
	return files, true
}

// buildsFor reports whether go build compiles the Go file name, whose text
// is data, for the loader's target: its name's _GOOS and _GOARCH suffixes
// and its //go:build line, read with the target's GOOS and GOARCH and the
// goexperiment tags of GOEXPERIMENT from the environment, which go build
// reads too (U1 SIMD-SCAN: a package of a module that holds one kernel
// file for a vector build and one for every other build declares each
// body twice, and only one of the two is compiled). cgo is off, as for
// every build of an Ergo program.
func (l *loader) buildsFor(name string, data []byte) bool {
	return goBuilds(l.platform, l.experiments, name, data)
}

// goBuilds is buildsFor for a platform (the host's GOOS or GOARCH where
// one is empty) and an experiment set.
func goBuilds(platform gobind.Platform, experiments []string, name string, data []byte) bool {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = cmp.Or(platform.GOOS, runtime.GOOS), cmp.Or(platform.GOARCH, runtime.GOARCH), false
	ctx.ToolTags = slices.Clone(ctx.ToolTags)
	for _, e := range experiments {
		ctx.ToolTags = append(ctx.ToolTags, "goexperiment."+e)
	}
	ctx.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	ok, err := ctx.MatchFile(".", name)
	return err == nil && ok
}

// goExperiments is the set of Go experiments GOEXPERIMENT turns on, read
// from the environment, else from the go env file, as the go command reads
// it: a comma-separated list where "noX" turns X off and "none" turns off
// every one named before it. Sorted, each once. The toolchain's default
// experiments are not listed: they are the toolchain's, in every key that
// names the toolchain.
func goExperiments() []string {
	v, ok := os.LookupEnv("GOEXPERIMENT")
	if !ok {
		v = goEnvFile()["GOEXPERIMENT"]
	}
	on := map[string]bool{}
	for _, e := range strings.Split(v, ",") {
		switch e = strings.TrimSpace(e); {
		case e == "":
		case e == "none":
			clear(on)
		case strings.HasPrefix(e, "no"):
			delete(on, strings.TrimPrefix(e, "no"))
		default:
			on[e] = true
		}
	}
	out := make([]string, 0, len(on))
	for e := range on {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
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

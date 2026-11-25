package sketchfront

import (
	"path"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// CheckDirOverlay checks a package with replacement contents for existing
// files. Absolute, clean keys identify files. Editors and content mappers
// can check an unsaved buffer without writing it into the owner's project.
// The overlay is read only for this call; it adds no files or directories.
func CheckDirOverlay(dir string, contents map[string]string) (Analysis, error) {
	return checkDir(overlayFS{contents: contents}, dir)
}

// OverlayFS returns an FS that serves replacement contents for clean absolute
// file paths from memory and reads all other paths from the host file system.
func OverlayFS(contents map[string]string) FS {
	return overlayFS{contents: contents}
}

// CheckRootOverlay re-checks only the root package of a folder Analysis with
// one file replaced in memory, reusing all already-checked imports and module
// metadata from a. It returns the diagnostics of the re-checked root package
// and ok=true when a came from a folder check containing absFile.
func (a Analysis) CheckRootOverlay(absFile, text string) ([]report.Diagnostic, bool) {
	if a.program == nil || a.program.root == nil || a.program.rootCheck == nil {
		return nil, false
	}
	l := a.program.rootCheck
	p := a.program.root
	clean := filepath.Clean(absFile)
	idx := -1
	for i, f := range p.files {
		if filepath.Clean(f.abs) == clean {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	parsed, err := syntax.Parse(text)
	if err != nil {
		d := syntaxDiagnostic(err)
		d.File = p.files[idx].rel
		return []report.Diagnostic{d}, true
	}
	parsed.Name = p.files[idx].rel
	files := make([]*syntax.File, len(p.files))
	for i, f := range p.files {
		if i == idx {
			files[i] = parsed
			continue
		}
		sf, perr := syntax.Parse(f.text)
		if perr != nil {
			d := syntaxDiagnostic(perr)
			d.File = f.rel
			return []report.Diagnostic{d}, true
		}
		sf.Name = f.rel
		files[i] = sf
	}
	path := p.path
	if !l.noMain {
		path = "main"
	}
	_, found := check.CheckTestOptions(path, files, l, l.testOf(p), l.opts)
	out := make([]report.Diagnostic, 0, len(found))
	for _, d := range found {
		rel := ""
		if d.File >= 0 && d.File < len(p.files) {
			rel = p.files[d.File].rel
		}
		out = append(out, report.Diagnostic{
			Code:    d.Code,
			File:    rel,
			Message: d.Message,
			Span:    report.Span{Offset: d.Offset, Length: d.Length, Line: d.Line, Column: d.Column},
			Fix:     reportFix(d.Fix),
		})
	}
	return out, true
}

type overlayFS struct {
	hostFS
	contents map[string]string
}

func (f overlayFS) ReadFile(name string) ([]byte, error) {
	if text, ok := f.contents[filepath.Clean(name)]; ok {
		return []byte(text), nil
	}
	return f.hostFS.ReadFile(name)
}

// stdOverrides returns replacement file texts for any stdlib source files
// present in the overlay (keyed by std import path -> filename -> source).
func (f overlayFS) stdOverrides() map[string]map[string]string {
	var out map[string]map[string]string
	const marker = "/internal/sketch/std/"
	for p, text := range f.contents {
		slash := filepath.ToSlash(filepath.Clean(p))
		_, after, ok := strings.Cut(slash, marker)
		if !ok {
			continue
		}
		pkgPath := path.Dir(after)
		fileName := path.Base(after)
		srcs, isStd := stdlib.Package(pkgPath)
		if !isStd {
			continue
		}
		if _, exists := srcs[fileName]; !exists {
			continue
		}
		if out == nil {
			out = map[string]map[string]string{}
		}
		if out[pkgPath] == nil {
			out[pkgPath] = map[string]string{}
		}
		out[pkgPath][fileName] = text
	}
	return out
}

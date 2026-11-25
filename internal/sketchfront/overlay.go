package sketchfront

import (
	"path"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/stdlib"
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

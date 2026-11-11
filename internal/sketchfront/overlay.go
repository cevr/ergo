package sketchfront

import "path/filepath"

// CheckDirOverlay checks a package with replacement contents for existing
// files. Absolute, clean keys identify files. Editors and content mappers
// can check an unsaved buffer without writing it into the owner's project.
// The overlay is read only for this call; it adds no files or directories.
func CheckDirOverlay(dir string, contents map[string]string) (Analysis, error) {
	return checkDir(overlayFS{contents: contents}, dir)
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

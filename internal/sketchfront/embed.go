package sketchfront

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// EmbeddedPaths lists the absolute paths of the program's checked asset
// inputs. Source inventories use the loader's facts, not a second globber.
func (a Analysis) EmbeddedPaths() []string {
	if a.program == nil || a.program.root == nil {
		return nil
	}
	var out []string
	for _, p := range append([]*loadedPackage{a.program.root}, a.program.imports...) {
		for name := range p.assets {
			out = append(out, filepath.Join(p.dir, filepath.FromSlash(name)))
		}
	}
	slices.Sort(out)
	return out
}

// ResolveEmbed captures build inputs under the declaring package. It uses
// the loader's FS, so a check needs neither Go compilation nor a host file
// service. Links are refused, never followed out of the package or module.
func (l *loader) ResolveEmbed(file *syntax.File, pattern string, tree bool) ([]check.EmbedFile, error) {
	var pkg *loadedPackage
	for _, p := range l.order {
		for _, f := range p.files {
			if f.syntax == file {
				pkg = p
			}
		}
	}
	if pkg == nil {
		return nil, fmt.Errorf("the declaring package is not loaded")
	}
	for dir := pkg.dir; dir != l.root; dir = filepath.Dir(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("the package is outside the module")
		}
		entries, err := l.fsys.ReadDir(parent)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Name() == filepath.Base(dir) && e.Type()&fs.ModeSymlink != 0 {
				return nil, fmt.Errorf("a symbolic link in the package path may leave the module")
			}
		}
	}
	all := strings.HasPrefix(pattern, "all:")
	pattern = strings.TrimPrefix(pattern, "all:")
	if !fs.ValidPath(pattern) || pattern == "." || strings.ContainsAny(pattern, "\\:\n\r") {
		return nil, fmt.Errorf("pattern %q must stay under the package folder", pattern)
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %v", pattern, err)
	}
	selected := map[string]bool{}
	var add func(string) error
	var expand func(string, []string) error
	add = func(name string) error {
		info, err := l.fsys.Stat(filepath.Join(pkg.dir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if info.IsDir() {
			entries, err := l.fsys.ReadDir(filepath.Join(pkg.dir, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !all && strings.HasPrefix(e.Name(), ".") || !all && strings.HasPrefix(e.Name(), "_") {
					continue
				}
				if e.Type()&fs.ModeSymlink != 0 {
					return fmt.Errorf("symbolic link %q is not an embeddable file", path.Join(name, e.Name()))
				}
				if err := add(path.Join(name, e.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q is not an embeddable regular file name", name)
		}
		if !embedNameAllowed(name) {
			return fmt.Errorf("%q is not a portable embed path", name)
		}
		selected[name] = true
		return nil
	}
	expand = func(dir string, parts []string) error {
		entries, err := l.fsys.ReadDir(filepath.Join(pkg.dir, filepath.FromSlash(dir)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			match, _ := path.Match(parts[0], e.Name())
			if !match {
				continue
			}
			name := path.Join(dir, e.Name())
			if e.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symbolic link %q is not an embeddable file", name)
			}
			if len(parts) == 1 {
				if err := add(name); err != nil {
					return err
				}
			} else if e.IsDir() {
				if err := expand(name, parts[1:]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := expand(".", strings.Split(pattern, "/")); err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("pattern %q matches no files", pattern)
	}
	if !tree && len(selected) != 1 {
		return nil, fmt.Errorf("a string or []byte embeds exactly one file; pattern %q matches %d", pattern, len(selected))
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	if pkg.assets == nil {
		pkg.assets = map[string][]byte{}
	}
	var out []check.EmbedFile
	for _, name := range names {
		data, cached := pkg.assets[name]
		if !cached {
			var err error
			data, err = l.fsys.ReadFile(filepath.Join(pkg.dir, filepath.FromSlash(name)))
			if err != nil {
				return nil, err
			}
			pkg.assets[name] = data
		}
		rel, _ := filepath.Rel(l.root, filepath.Join(pkg.dir, filepath.FromSlash(name)))
		l.texts[filepath.ToSlash(rel)] = string(data)
		out = append(out, check.EmbedFile{Path: name, Data: slices.Clone(data)})
	}
	return out, nil
}

// embedNameAllowed is a portable subset of Go module.CheckFilePath, which
// cmd/go applies to every element of an embedded path. Brackets are also
// refused: emitted directives name exact files, not more glob patterns.
// Keep this at the source boundary, before the Go builder can reject it.
func embedNameAllowed(name string) bool {
	for _, elem := range strings.Split(name, "/") {
		if elem == "" || strings.HasSuffix(elem, ".") {
			return false
		}
		switch elem {
		case ".git", ".hg", ".svn", ".bzr", "go.mod":
			return false
		}
		short, _, _ := strings.Cut(strings.ToUpper(elem), ".")
		if short == "CON" || short == "PRN" || short == "AUX" || short == "NUL" ||
			len(short) == 4 && (strings.HasPrefix(short, "COM") || strings.HasPrefix(short, "LPT")) && short[3] >= '1' && short[3] <= '9' {
			return false
		}
		for _, r := range elem {
			if r < 128 {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&()+,-.=@^_{}~ ", r) {
					continue
				}
				return false
			}
			if !unicode.IsLetter(r) {
				return false
			}
		}
	}
	return true
}

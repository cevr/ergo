package build

import (
	"fmt"
	"path"
	"strings"
)

// goModFile is the part of a go.mod the direct build path reads: the
// module path, the go line, and the requires, each of which a replace must
// name as a local directory. parseGoMod refuses every other directive, so a
// module the direct path cannot reproduce falls back to go build.
type goModFile struct {
	Module   string
	Go       string
	Requires map[string]string // module path → version
	Replaces map[string]string // module path → local directory, as written
}

// parseGoMod reads the go.mod data. It refuses toolchain and godebug lines
// (they change the toolchain or the binary's default GODEBUG), every other
// directive it does not know, and a require that no replace maps to a local
// directory (its module comes from the module cache, with a checksum this
// path does not verify).
func parseGoMod(data string) (goModFile, error) {
	f := goModFile{Requires: map[string]string{}, Replaces: map[string]string{}}
	block := ""
	for n, line := range strings.Split(data, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if block != "" {
			if fields[0] == ")" && len(fields) == 1 {
				block = ""
				continue
			}
			if err := f.directive(block, fields); err != nil {
				return f, fmt.Errorf("go.mod:%d: %w", n+1, err)
			}
			continue
		}
		if len(fields) == 2 && fields[1] == "(" {
			block = fields[0]
			if block != "require" && block != "replace" {
				return f, fmt.Errorf("go.mod:%d: %s block", n+1, block)
			}
			continue
		}
		if err := f.directive(fields[0], fields[1:]); err != nil {
			return f, fmt.Errorf("go.mod:%d: %w", n+1, err)
		}
	}
	if f.Module == "" || f.Go == "" {
		return f, fmt.Errorf("go.mod has no module or no go line")
	}
	for mod := range f.Requires {
		if _, ok := f.Replaces[mod]; !ok {
			return f, fmt.Errorf("go.mod requires %s from the module cache", mod)
		}
	}
	return f, nil
}

func (f *goModFile) directive(verb string, args []string) error {
	unquote := func(s string) string { return strings.Trim(s, `"`) }
	switch {
	case verb == "module" && len(args) == 1:
		f.Module = unquote(args[0])
	case verb == "go" && len(args) == 1:
		f.Go = args[0]
	case verb == "require" && len(args) == 2:
		f.Requires[unquote(args[0])] = args[1]
	case verb == "replace" && len(args) == 3 && args[1] == "=>":
		dir := unquote(args[2])
		if !strings.HasPrefix(dir, "./") || strings.Contains(dir, "..") {
			return fmt.Errorf("replace %s is not a directory inside the module", args[0])
		}
		f.Replaces[unquote(args[0])] = dir
	default:
		return fmt.Errorf("directive %s %s", verb, strings.Join(args, " "))
	}
	return nil
}

// goLang is the language version of a go line: "1.27.3" and "1.27" give
// "1.27".
func goLang(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}

// within reports whether importPath is mod or a package under it.
func within(importPath, mod string) bool {
	return importPath == mod || strings.HasPrefix(importPath, mod+"/")
}

// standard reports whether an import path names a standard package: its
// first element has no dot, as cmd/go decides.
func standard(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".") && path.Clean(importPath) == importPath
}

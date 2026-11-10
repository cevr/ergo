package sketchfront

// Go modules (IG7, owner q37; docs/spec/interop.md, Go modules). A module
// that imports Go packages outside Go's standard library keeps a plain
// go.mod and go.sum beside ergo.mod, managed with the go command, and the
// committed binding of each package under ergo.bind/. The loader reads
// go.mod and go.sum as files: a check runs no Go process. go.mod gives the
// emitted module its require and replace lines (a local directory made
// absolute) and the version each binding must be stamped with; go.sum is
// copied as it is. go build is the authority on the rest.

import (
	"fmt"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/lower"
)

// GoModFile and GoSumFile are the Go module files beside ergo.mod.
const (
	GoModFile = "go.mod"
	GoSumFile = "go.sum"
)

// moduleStamp is the prefix of a module binding's stamp line, written by
// ergo bind go (internal/gobind): `// ergo:module <path> <version>`, with
// ` => <replacement> [<version>]` when go.mod replaces the module, or only
// `// ergo:module <path>` for a package of go.mod's own module.
const moduleStamp = "// ergo:module "

// goRequire is one require line of go.mod.
type goRequire struct{ path, version string }

// goReplace is one replace line: old [oldVersion] => new [newVersion].
type goReplace struct{ old, oldVersion, new, newVersion string }

// goMod is the part of a go.mod the loader reads.
type goMod struct {
	module  string
	require []goRequire
	replace []goReplace
}

// parseGoMod reads the module, require and replace directives of a go.mod,
// in single-line and block form; every other directive is go build's. An
// error names its line.
func parseGoMod(text string) (goMod, error) {
	var m goMod
	block := ""
	for i, raw := range strings.Split(text, "\n") {
		line := i + 1
		words, err := goModWords(raw)
		if err != nil {
			return goMod{}, fmt.Errorf("%s:%d: %v", GoModFile, line, err)
		}
		if len(words) == 0 {
			continue
		}
		verb := block
		if block == "" {
			verb, words = words[0], words[1:]
			if len(words) == 1 && words[0] == "(" {
				block = verb
				continue
			}
		} else if len(words) == 1 && words[0] == ")" {
			block = ""
			continue
		}
		switch verb {
		case "module":
			if len(words) != 1 {
				return goMod{}, fmt.Errorf("%s:%d: module takes one path", GoModFile, line)
			}
			m.module = words[0]
		case "require":
			if len(words) != 2 {
				return goMod{}, fmt.Errorf("%s:%d: a requirement is a module path and a version", GoModFile, line)
			}
			m.require = append(m.require, goRequire{words[0], words[1]})
		case "replace":
			arrow := -1
			for k, w := range words {
				if w == "=>" {
					arrow = k
				}
			}
			left, right := words[:max(arrow, 0)], words[arrow+1:]
			if arrow < 1 || len(left) > 2 || len(right) < 1 || len(right) > 2 {
				return goMod{}, fmt.Errorf("%s:%d: a replacement is old [version] => new [version]", GoModFile, line)
			}
			r := goReplace{old: left[0], new: right[0]}
			if len(left) == 2 {
				r.oldVersion = left[1]
			}
			if len(right) == 2 {
				r.newVersion = right[1]
			}
			m.replace = append(m.replace, r)
		}
	}
	if block != "" {
		return goMod{}, fmt.Errorf("%s: the %s block is not closed", GoModFile, block)
	}
	return m, nil
}

// goModWords splits a go.mod line into its words: a // comment ends it,
// and a word may be a Go string literal.
func goModWords(line string) ([]string, error) {
	var words []string
	for {
		line = strings.TrimLeft(line, " \t\r")
		if line == "" {
			return words, nil
		}
		if line[0] == '"' || line[0] == '`' {
			q, err := strconv.QuotedPrefix(line)
			if err != nil {
				return nil, fmt.Errorf("a quoted word does not end")
			}
			w, _ := strconv.Unquote(q)
			words, line = append(words, w), line[len(q):]
			continue
		}
		end := strings.IndexAny(line, " \t\r")
		if end < 0 {
			end = len(line)
		}
		if c := strings.Index(line[:end], "//"); c == 0 {
			return words, nil // a comment ends the line
		} else if c > 0 {
			end = c
		}
		words, line = append(words, line[:end]), line[end:]
	}
}

// localReplacement reports whether a replacement target is a directory, by
// Go's rule: it starts with ./ or ../, or it is absolute.
func localReplacement(target string) bool {
	return strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") || filepath.IsAbs(target) ||
		target == "." || target == ".."
}

// lowerModule is the emitted module's Go requirements: go.mod's require and
// replace lines with each local directory made absolute against root, the
// module of go.mod itself required and replaced by root, and go.sum.
func (m goMod) lowerModule(root, sum string) lower.GoModule {
	out := lower.GoModule{Sum: sum}
	for _, r := range m.require {
		out.Require = append(out.Require, goModWord(r.path)+" "+goModWord(r.version))
		out.Modules = append(out.Modules, r.path)
	}
	for _, r := range m.replace {
		target := r.new
		if localReplacement(target) {
			target = filepath.Join(root, filepath.FromSlash(target))
		}
		line := goModWord(r.old)
		if r.oldVersion != "" {
			line += " " + goModWord(r.oldVersion)
		}
		line += " => " + goModWord(target)
		if r.newVersion != "" {
			line += " " + goModWord(r.newVersion)
		}
		out.Replace = append(out.Replace, line)
	}
	if m.module != "" {
		out.Require = append(out.Require, goModWord(m.module)+" v0.0.0")
		out.Replace = append(out.Replace, goModWord(m.module)+" => "+goModWord(root))
		out.Modules = append(out.Modules, m.module)
	}
	return out
}

// goModWord is w as a go.mod word: quoted unless it is plain.
func goModWord(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~+/") == "" {
		return w
	}
	return strconv.Quote(w)
}

// moduleKey is the stamp a binding of a package of module mod must carry
// under m: what ergo bind go wrote from go list's Module for the same
// go.mod. ok is false when m does not require mod.
func (m goMod) moduleKey(mod string) (string, bool) {
	if mod == m.module {
		return mod, true
	}
	version := ""
	for _, r := range m.require {
		if r.path == mod {
			version = r.version
		}
	}
	if version == "" {
		return "", false
	}
	key := mod + " " + version
	var match *goReplace
	for i, r := range m.replace {
		if r.old == mod && (r.oldVersion == version || r.oldVersion == "" && match == nil) {
			match = &m.replace[i]
		}
	}
	if match != nil {
		key += " => " + match.new
		if match.newVersion != "" {
			key += " " + match.newVersion
		}
	}
	return key, true
}

// staleBinding is why the module binding src of the Go package path does
// not match go.mod, read from files only: "" when its stamp is the key
// go.mod gives its module now. m is nil when there is no go.mod beside
// ergo.mod.
func staleBinding(m *goMod, path, src string) string {
	stamp := ""
	for _, line := range strings.SplitN(src, "\n", 4) {
		if rest, ok := strings.CutPrefix(line, moduleStamp); ok {
			stamp = strings.TrimSpace(rest)
		}
	}
	mod, _, _ := strings.Cut(stamp, " ")
	switch {
	case stamp == "":
		return "its stamp names no Go module (no " + strings.TrimSpace(moduleStamp) + " line)"
	case path != mod && !strings.HasPrefix(path, mod+"/"):
		return fmt.Sprintf("its stamp names module %s, which does not hold %s", mod, path)
	case m == nil:
		return fmt.Sprintf("no %s beside %s requires %s", GoModFile, ModFile, mod)
	}
	key, ok := m.moduleKey(mod)
	switch {
	case !ok:
		return fmt.Sprintf("%s does not require %s", GoModFile, mod)
	case key != stamp:
		return fmt.Sprintf("it was made for %s, and %s now names %s", stamp, GoModFile, key)
	}
	return ""
}

// bindingPlatform checks the binding's provenance, not a substring of its
// source or the platforms supported by today's generator. Old bindings keep
// their stated set until ergo bind go regenerates them.
func bindingPlatform(path, src string, platform gobind.Platform) string {
	var platforms []string
	lines := strings.SplitN(src, "\n", 4)
	for _, line := range lines[:min(3, len(lines))] {
		if rest, ok := strings.CutPrefix(line, "// ergo:bind "); ok {
			fields := strings.Fields(rest)
			if len(fields) >= 2 && fields[0] == path {
				platforms = fields[2:]
			}
			break
		}
	}
	if slices.Contains(platforms, platform.String()) {
		return ""
	}
	stated := strings.Join(platforms, " ")
	if stated == "" {
		stated = "(none)"
	}
	return fmt.Sprintf("it states platforms %s; target is %s", stated, platform)
}

// readGoModule reads go.mod and go.sum beside ergo.mod, if go.mod exists.
// A malformed go.mod is a diagnostic in it.
func (l *loader) readGoModule() {
	data, err := l.fsys.ReadFile(filepath.Join(l.root, GoModFile))
	if err != nil {
		return
	}
	text := string(data)
	l.texts[GoModFile] = text
	m, err := parseGoMod(text)
	if err != nil {
		lines := lineStarts(text)
		line := 1
		if _, rest, ok := strings.Cut(err.Error(), GoModFile+":"); ok {
			if n, _, ok := strings.Cut(rest, ":"); ok {
				if v, convErr := strconv.Atoi(n); convErr == nil {
					line = v
				}
			}
		}
		d := modDiagnostic(text, lines, min(line, len(lines)), 1, err.Error())
		d.File = GoModFile
		l.diags = append(l.diags, *d)
		return
	}
	l.goMod = &m
	if sum, err := l.fsys.ReadFile(filepath.Join(l.root, GoSumFile)); err == nil {
		l.goSum = string(sum)
		l.texts[GoSumFile] = l.goSum
	}
}

// bindingFile is where the binding of a Go package lives under the module
// root.
func bindingFile(path string) string {
	return pathpkg.Join("ergo.bind", path, pathpkg.Base(path)+".ergo")
}

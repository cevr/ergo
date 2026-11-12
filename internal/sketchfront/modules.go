package sketchfront

// Module requirements (MD1; docs/spec/grammar.md, Source files 1). A
// package of another Ergo module is imported by its import path when the
// go.mod beside ergo.mod requires that module (owner question Q12, option
// a: Ergo rides Go's module files, as Gleam, Elixir and ReScript ride their
// hosts' package managers).
//
//   - The root's go.mod pins each module the program's packages import
//     from, as Go 1.17 asks of a main module, and go.sum holds the hashes.
//   - The build list is Go's pruned graph: each module the root requires,
//     at the highest version that the root's go.mod or the go.mod of one of
//     the root's requirements names (minimal version selection).
//   - A module's copy is read from Go's module cache, read-only, and only
//     after its h1 hash is the one go.sum holds; a go.mod read for the
//     graph is checked the same way. A replacement in the root's go.mod by
//     a local folder has no hash, as in Go.
//   - A module is an Ergo module when its root holds ergo.mod naming the
//     module's path.
//   - The loader runs no process and reaches no network: a module that is
//     not in the cache is a diagnostic at the import that names the fetch
//     command.
//
// Prior art: Go's go.mod, go.sum, module cache and minimal version
// selection (go.dev/ref/mod); Gleam's manifest.toml and Elixir's mix.lock
// pin hashes beside the project; ReScript resolves through npm.

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/std"
)

// depModule is one required module of the build list that a package of
// the program imports from.
type depModule struct {
	// path and version name the module and its selected version; name is
	// path@version, the prefix of its files' names in diagnostics.
	path, version, name string
	// dir is the module's root folder: its copy in the module cache, or
	// the local folder that replaces it.
	dir string
	// reason is why the module serves no package; "" when it serves them.
	reason string
}

// moduleGraph is the build list the root's go.mod gives.
type moduleGraph struct {
	// selected is each module the root requires, at its selected version.
	selected map[string]string
	// err is why the graph could not be read; "" when it was.
	err string
}

// FetchCommand is the command that puts module path at version into the
// module cache and its hashes into go.sum, run in the module root. Every
// diagnostic that asks for a fetch names it (MD2 brings ergo get).
func FetchCommand(path, version string) string {
	return "go get " + path + "@" + version
}

// moduleCache is the folder of Go's module cache, as the go command finds
// it: GOMODCACHE, else the first GOPATH element's pkg/mod, else
// $HOME/go/pkg/mod; each variable is read from the environment, then from
// the go env file. "" when none of them names a folder.
func moduleCache() string {
	env := goEnvFile()
	get := func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return env[key]
	}
	if v := get("GOMODCACHE"); filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	if v := get("GOPATH"); v != "" {
		if first := filepath.SplitList(v)[0]; filepath.IsAbs(first) {
			return filepath.Join(first, "pkg", "mod")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, "go", "pkg", "mod")
}

// goEnvFile reads the go env file (go env -w) as the go command does:
// GOENV names it, "off" turns it off, and the default is go/env in the
// user's configuration folder. Lines are KEY=VALUE.
func goEnvFile() map[string]string {
	name := os.Getenv("GOENV")
	if name == "off" {
		return nil
	}
	if name == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil
		}
		name = filepath.Join(dir, "go", "env")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if key, value, ok := strings.Cut(strings.TrimSpace(scan.Text()), "="); ok {
			out[key] = value
		}
	}
	return out
}

// escapeModulePath is a module path or version as the module cache spells
// it: each upper-case letter is ! and its lower case (Go's
// module.EscapePath), so the cache works on a file system that folds case.
func escapeModulePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// compareVersions orders two semantic versions (semver.org 2.0.0, with Go's
// leading v): the numbers, then a version with a pre-release below the same
// version without one, then the pre-release identifiers in order. Build
// metadata is ignored.
func compareVersions(a, b string) int {
	a, _, _ = strings.Cut(strings.TrimPrefix(a, "v"), "+")
	b, _, _ = strings.Cut(strings.TrimPrefix(b, "v"), "+")
	ac, apre, aHas := strings.Cut(a, "-")
	bc, bpre, bHas := strings.Cut(b, "-")
	an, bn := strings.Split(ac, "."), strings.Split(bc, ".")
	for i := 0; i < max(len(an), len(bn)); i++ {
		x, y := "0", "0"
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if c := compareNumbers(x, y); c != 0 {
			return c
		}
	}
	switch {
	case !aHas && !bHas:
		return 0
	case !aHas:
		return 1
	case !bHas:
		return -1
	}
	ap, bp := strings.Split(apre, "."), strings.Split(bpre, ".")
	for i := 0; i < min(len(ap), len(bp)); i++ {
		x, y := ap[i], bp[i]
		_, xErr := strconv.ParseUint(x, 10, 64)
		_, yErr := strconv.ParseUint(y, 10, 64)
		var c int
		switch {
		case xErr == nil && yErr == nil:
			c = compareNumbers(x, y)
		case xErr == nil:
			c = -1 // numeric identifiers sort below alphanumeric ones
		case yErr == nil:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return len(ap) - len(bp)
}

// compareNumbers orders two decimal numerals of any length.
func compareNumbers(x, y string) int {
	x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
	if len(x) != len(y) {
		return len(x) - len(y)
	}
	return strings.Compare(x, y)
}

// sums reads go.sum: the hash of each "path version" and of each
// "path version/go.mod".
func sums(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			out[f[0]+" "+f[1]] = f[2]
		}
	}
	return out
}

// hash1 is the go command's h1 hash of a file tree (dirhash.Hash1): the
// SHA-256 of the lines "<hex SHA-256 of the file>  <name>\n", names sorted.
func hash1(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	summary := sha256.New()
	for _, name := range names {
		fmt.Fprintf(summary, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(summary.Sum(nil))
}

// hashTree is the h1 hash of every file under dir, each named prefix/ and
// its slash path below dir, as go mod verify hashes a module's copy.
func (l *loader) hashTree(dir, prefix string) (string, error) {
	files := map[string][]byte{}
	var walk func(d, rel string) error
	walk = func(d, rel string) error {
		entries, err := l.fsys.ReadDir(d)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := rel + "/" + e.Name()
			full := filepath.Join(d, e.Name())
			if e.IsDir() {
				if err := walk(full, name); err != nil {
					return err
				}
				continue
			}
			data, err := l.fsys.ReadFile(full)
			if err != nil {
				return err
			}
			files[name] = data
		}
		return nil
	}
	if err := walk(dir, prefix); err != nil {
		return "", err
	}
	return hash1(files), nil
}

// replacement is the root go.mod's replacement of module path at version:
// a local folder (made absolute), or another module and version; ok is
// false when none applies. A replacement that names the version wins over
// one that does not, as in Go.
func (l *loader) replacement(path, version string) (dir, module, moduleVersion string, ok bool) {
	var match *goReplace
	for i, r := range l.goMod.replace {
		if r.old == path && (r.oldVersion == version || r.oldVersion == "" && (match == nil || match.oldVersion == "")) {
			match = &l.goMod.replace[i]
		}
	}
	if match == nil {
		return "", "", "", false
	}
	if localReplacement(match.new) {
		target := match.new
		if !filepath.IsAbs(target) {
			target = filepath.Join(l.root, filepath.FromSlash(target))
		}
		return filepath.Clean(target), "", "", true
	}
	return "", match.new, match.newVersion, true
}

// graph is the build list, read once, on the first import of another
// module's package.
func (l *loader) graph() *moduleGraph {
	if l.modules != nil {
		return l.modules
	}
	g := &moduleGraph{selected: map[string]string{}}
	l.modules = g
	for _, r := range l.goMod.require {
		if v, ok := g.selected[r.path]; !ok || compareVersions(r.version, v) > 0 {
			g.selected[r.path] = r.version
		}
	}
	paths := make([]string, 0, len(g.selected))
	for path := range g.selected {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	pinned := maps.Clone(g.selected)
	for _, path := range paths {
		reqs, why := l.requirements(path, pinned[path])
		if why != "" {
			g.err = why
			return g
		}
		for _, r := range reqs {
			if v, ok := g.selected[r.path]; ok && compareVersions(r.version, v) > 0 {
				g.selected[r.path] = r.version
			}
		}
	}
	return g
}

// requirements are the require lines of module path's go.mod at version:
// the replacing folder's go.mod, or the cache's copy of the go.mod, checked
// against go.sum's "path version/go.mod" hash first. why is the reason it
// cannot be read.
func (l *loader) requirements(path, version string) ([]goRequire, string) {
	name, text := "", ""
	if dir, other, otherVersion, ok := l.replacement(path, version); ok && dir != "" {
		data, err := l.fsys.ReadFile(filepath.Join(dir, GoModFile))
		if err != nil {
			return nil, fmt.Sprintf("%s, which replaces %s, holds no %s", l.relRoot(dir), path, GoModFile)
		}
		name, text = path+"@"+version+"/"+GoModFile, string(data)
	} else {
		if ok {
			path, version = other, otherVersion
		}
		key := path + " " + version + "/" + GoModFile
		want, ok := sums(l.goSum)[key]
		if !ok {
			return nil, fmt.Sprintf("%s has no hash of %s; run %s", GoSumFile, key, FetchCommand(path, version))
		}
		if l.modCache == "" {
			return nil, "no module cache: GOMODCACHE, GOPATH and the home folder are not set"
		}
		file := filepath.Join(l.modCache, "cache", "download", escapeModulePath(path), "@v", escapeModulePath(version)+".mod")
		data, err := l.fsys.ReadFile(file)
		if err != nil {
			return nil, fmt.Sprintf("%s %s is not in the module cache (no %s); run %s", path, version, file, FetchCommand(path, version))
		}
		if got := hash1(map[string][]byte{GoModFile: data}); got != want {
			return nil, fmt.Sprintf("the module cache's go.mod of %s %s does not match %s (%s holds %s, the copy hashes %s); go mod verify reports it", path, version, GoSumFile, GoSumFile, want, got)
		}
		name, text = path+"@"+version+"/"+GoModFile, string(data)
	}
	l.texts[name] = text
	m, err := parseGoMod(text)
	if err != nil {
		return nil, fmt.Sprintf("%s: %v", name, err)
	}
	return m.require, ""
}

// required is the module of the root's go.mod requirements that holds the
// import path: the longest required module path that is path or a prefix
// of it; "" when none is.
func (l *loader) required(path string) string {
	if l.goMod == nil {
		return ""
	}
	best := ""
	for _, r := range l.goMod.require {
		if (path == r.path || strings.HasPrefix(path, r.path+"/")) && len(r.path) > len(best) {
			best = r.path
		}
	}
	return best
}

// isStdPath reports whether path names std, which every module imports
// and no module may shadow.
func isStdPath(path string) bool {
	_, ok := std.Package(path)
	return ok || path == std.PreludePath
}

// locate is the folder of an imported package and the module that holds
// it (nil for the program's own module); reason is why no folder serves
// it. A required module whose path is longer than the program's module
// name holds its paths, as Go's longest module path does.
func (l *loader) locate(path string) (string, *depModule, string) {
	mod := l.required(path)
	if dir, ok := l.local(path); ok && len(mod) <= len(l.module) {
		return dir, nil, ""
	}
	switch {
	case l.module == "":
		return "", nil, "no " + ModFile + " above the program's folder declares a module, so the program imports std only"
	case l.localOnly:
		return "", nil, "it is not in module " + l.module
	case mod == "":
		if l.goMod == nil {
			return "", nil, fmt.Sprintf("it is not std and not in module %s, and no %s beside %s requires a module; a package of another module is imported through a requirement there (go mod init, then %s)", l.module, GoModFile, ModFile, FetchCommand("MODULE", "VERSION"))
		}
		return "", nil, fmt.Sprintf("it is not std, not in module %s, and in no module that %s requires; require the module that holds it: %s", l.module, GoModFile, FetchCommand("MODULE", "VERSION"))
	}
	g := l.graph()
	if g.err != "" {
		return "", nil, "the module graph of " + GoModFile + " cannot be read: " + g.err
	}
	m := l.dependency(mod, g.selected[mod])
	if m.reason != "" {
		return "", nil, m.reason
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(path, mod), "/")
	if rel != "" && slices.ContainsFunc(strings.Split(rel, "/"), func(e string) bool {
		return e == "" || e == "." || e == ".." || strings.ContainsAny(e, `\`)
	}) {
		return "", nil, fmt.Sprintf("it is not a package path of module %s", m.name)
	}
	return filepath.Join(m.dir, filepath.FromSlash(rel)), m, ""
}

// dependency is required module path at its selected version, read once:
// its folder, its hash checked against go.sum, and its ergo.mod.
func (l *loader) dependency(path, version string) *depModule {
	if m, ok := l.deps[path]; ok {
		return m
	}
	m := &depModule{path: path, version: version, name: path + "@" + version}
	l.deps[path] = m
	m.reason = l.fetchDependency(m)
	if m.reason != "" {
		return m
	}
	data, err := l.fsys.ReadFile(filepath.Join(m.dir, ModFile))
	if err != nil {
		m.reason = fmt.Sprintf("module %s is a Go module, not an Ergo module: its root holds no %s", m.name, ModFile)
		return m
	}
	l.texts[m.name+"/"+ModFile] = string(data)
	name, problem := parseModule(string(data))
	switch {
	case problem != nil:
		m.reason = fmt.Sprintf("the %s of module %s does not parse: %s", ModFile, m.name, problem.Message)
	case name != path:
		m.reason = fmt.Sprintf("the %s of module %s names module %s, not %s", ModFile, m.name, name, path)
	}
	return m
}

// fetchDependency finds m's folder: the local folder that replaces it, or
// its copy in the module cache, whose h1 hash must be go.sum's. It returns
// why the module cannot be read.
func (l *loader) fetchDependency(m *depModule) string {
	path, version := m.path, m.version
	if dir, other, otherVersion, ok := l.replacement(path, version); ok {
		if dir != "" {
			if info, err := l.fsys.Stat(dir); err != nil || !info.IsDir() {
				return fmt.Sprintf("the folder %s, which replaces %s, does not exist", l.relRoot(dir), m.name)
			}
			m.dir = dir
			return ""
		}
		path, version = other, otherVersion
	}
	key := path + " " + version
	want, ok := sums(l.goSum)[key]
	if !ok {
		return fmt.Sprintf("%s has no hash of %s; run %s", GoSumFile, key, FetchCommand(path, version))
	}
	if l.modCache == "" {
		return "no module cache: GOMODCACHE, GOPATH and the home folder are not set"
	}
	dir := filepath.Join(l.modCache, escapeModulePath(path)+"@"+escapeModulePath(version))
	info, err := l.fsys.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("%s %s is not in the module cache (no %s); run %s", path, version, dir, FetchCommand(path, version))
	}
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("the module cache's copy of %s %s is not a folder (%s)", path, version, dir)
	}
	got, err := l.hashTree(dir, path+"@"+version)
	if err != nil {
		return fmt.Sprintf("the module cache's copy of %s %s cannot be read: %v", path, version, err)
	}
	if got != want {
		return fmt.Sprintf("the module cache's copy of %s %s does not match %s (%s holds %s, the copy hashes %s); go mod verify reports it", path, version, GoSumFile, GoSumFile, want, got)
	}
	m.dir = dir
	return ""
}

// relRoot names a folder for a diagnostic: relative to the module root
// when it is below it, else as it is.
func (l *loader) relRoot(dir string) string {
	if rel, err := filepath.Rel(l.root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(dir)
}

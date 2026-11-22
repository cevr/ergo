package sketchfront

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gobind"
)

// WorkFile is the optional workspace file at a workspace root
// (docs/spec/build.md, Workspace).
const WorkFile = "ergo.work"

// workVersion is the newest ergo.work format this binary reads.
const workVersion = 2

// Workspace is the modules below a root (docs/spec/build.md, Workspace):
// every folder with an ergo.mod that source admission reaches, and the
// edges between them.
type Workspace struct {
	// Root is the absolute folder the workspace was discovered from.
	Root string
	// Members are the modules, by folder in byte order of the
	// slash-separated path relative to Root.
	Members []Member
	// Edges are the local replace lines of a member's go.mod that land
	// on another member, one per pair, sorted by From then To.
	Edges []ModuleEdge
	// Skip are the folders ergo.work leaves out, relative to Root.
	Skip []string
	// Divergences are the requirements of one member on another member's
	// Go module by version with no replace line: the member builds
	// against a copy from the module cache, not the one beside it.
	Divergences []Divergence
}

// Divergence is a member's go.mod line `require PATH VERSION` where PATH is
// another member's Go module and no replace line of the same go.mod
// replaces PATH. From and To index Members; Line is the require's line.
type Divergence struct {
	From, To      int
	Path, Version string
	Line          int
}

// Member is one module of a workspace.
type Member struct {
	// Dir is the module's absolute folder; Rel is it relative to the
	// workspace root, slash-separated ("." for the root).
	Dir, Rel string
	// Module is the ergo.mod module name; "" when the ergo.mod does not
	// read, and then Problem says why.
	Module  string
	Problem string
}

// ModuleEdge is a member that resolves another member locally: From's
// go.mod replaces a module with To's folder. From and To index Members.
type ModuleEdge struct {
	From, To int
}

// Name is m's module name, or its folder when its ergo.mod does not read.
func (m Member) Name() string {
	if m.Module != "" {
		return m.Module
	}
	return m.Rel
}

// IsWorkspaceRoot reports whether the command that names dir runs over a
// workspace: dir holds an ergo.work, or no module holds dir.
func IsWorkspaceRoot(fsys FS, dir string) (bool, error) {
	if fsys == nil {
		fsys = hostFS{}
	}
	if _, err := fsys.Stat(filepath.Join(dir, WorkFile)); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	l := newLoader(fsys, gobind.Platform{}, dir, "")
	l.localOnly = true
	found, err := l.findModule(dir)
	return !found, err
}

// DiscoverWorkspace finds the members and edges of the workspace at root.
// It walks by source admission: a folder whose name starts with `.` or
// `_`, testdata, node_modules and vendor are never entered, nor a folder
// ergo.work skips; directory links are not followed. A member's own
// subfolders are walked too, so a module nested in another is a member of
// its own. It reads ergo.mod and go.mod files and nothing else.
func DiscoverWorkspace(fsys FS, root string) (Workspace, error) {
	if fsys == nil {
		fsys = hostFS{}
	}
	ws := Workspace{Root: root}
	if data, err := fsys.ReadFile(filepath.Join(root, WorkFile)); err == nil {
		wf, err := parseWorkFile(string(data))
		if err != nil {
			return Workspace{}, err
		}
		for _, s := range wf.skips {
			info, err := fsys.Stat(filepath.Join(root, filepath.FromSlash(s.dir)))
			if err != nil || !info.IsDir() {
				return Workspace{}, fmt.Errorf("%s:%d: skip %s names no folder below the workspace root", WorkFile, s.line, s.dir)
			}
			ws.Skip = append(ws.Skip, s.dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Workspace{}, err
	}
	skipped := map[string]bool{}
	for _, s := range ws.Skip {
		skipped[s] = true
	}
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		if rel != "." {
			name := filepath.Base(dir)
			if !admitFolder(name) || skipped[rel] {
				return nil
			}
		}
		if info, err := fsys.Stat(filepath.Join(dir, ModFile)); err == nil && info.Mode().IsRegular() {
			m := Member{Dir: dir, Rel: rel}
			data, err := fsys.ReadFile(filepath.Join(dir, ModFile))
			if err != nil {
				return err
			}
			if mf, d := parseModule(string(data)); d != nil {
				m.Problem = filepath.Join(dir, ModFile) + ": " + d.Message
			} else {
				m.Module = mf.module
			}
			ws.Members = append(ws.Members, m)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := e.Name()
			if rel != "." {
				sub = rel + "/" + sub
			}
			if err := walk(filepath.Join(dir, e.Name()), sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, "."); err != nil {
		return Workspace{}, err
	}
	slices.SortFunc(ws.Members, func(a, b Member) int { return strings.Compare(a.Rel, b.Rel) })
	byDir := map[string]int{}
	for i, m := range ws.Members {
		byDir[m.Dir] = i
	}
	goMods := make([]*goMod, len(ws.Members))
	byGoModule := map[string]int{}
	for i, m := range ws.Members {
		data, err := fsys.ReadFile(filepath.Join(m.Dir, GoModFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Workspace{}, err
		}
		gm, err := parseGoMod(string(data))
		if err != nil {
			continue // the member's own load reports its go.mod
		}
		goMods[i] = &gm
		if gm.module != "" {
			byGoModule[gm.module] = i
		}
	}
	seen := map[ModuleEdge]bool{}
	for i, gm := range goMods {
		if gm == nil {
			continue
		}
		m := ws.Members[i]
		replaced := map[string]bool{}
		for _, r := range gm.replace {
			replaced[r.old] = true
		}
		for _, q := range gm.require {
			if j, ok := byGoModule[q.path]; ok && j != i && !replaced[q.path] {
				ws.Divergences = append(ws.Divergences, Divergence{From: i, To: j, Path: q.path, Version: q.version, Line: q.line})
			}
		}
		for _, r := range gm.replace {
			if !localReplacement(r.new) {
				continue
			}
			target := filepath.Clean(r.new)
			if !filepath.IsAbs(target) {
				target = filepath.Join(m.Dir, target)
			}
			if j, ok := byDir[target]; ok && j != i {
				e := ModuleEdge{From: i, To: j}
				if !seen[e] {
					seen[e] = true
					ws.Edges = append(ws.Edges, e)
				}
			}
		}
	}
	slices.SortFunc(ws.Edges, func(a, b ModuleEdge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	return ws, nil
}

// admitFolder reports whether discovery enters a folder of this name:
// not hidden, not underscored, and not testdata, node_modules or vendor,
// which hold fixtures and copies, never members.
func admitFolder(name string) bool {
	return !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") &&
		name != "testdata" && name != "node_modules" && name != "vendor"
}

// Order is the members dependency first: a member after every member it
// resolves locally (Edges), else by folder. Members of a cycle keep
// their folder order.
func (ws Workspace) Order() []int {
	deps := make([][]int, len(ws.Members))
	for _, e := range ws.Edges {
		deps[e.From] = append(deps[e.From], e.To)
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]int, len(ws.Members))
	var out []int
	var visit func(i int)
	visit = func(i int) {
		if state[i] != unvisited {
			return
		}
		state[i] = visiting
		for _, j := range deps[i] {
			visit(j)
		}
		state[i] = done
		out = append(out, i)
	}
	for i := range ws.Members {
		visit(i)
	}
	return out
}

// workSkip is one skip line of ergo.work.
type workSkip struct {
	dir  string
	line int
}

// WorkGen is a `gen` line of ergo.work (version 2; docs/spec/build.md,
// Generators): a command that writes files of the tree, which `ergo task
// gen` runs, and the paths it reads besides its Go build (its `reads`
// lines).
type WorkGen struct {
	// Name is the generator's name, unique in the file.
	Name string
	// Command is the command's words: `go run PKG ARGS...` or
	// `go test ARGS...` with one package.
	Command []string
	// Reads are the files and folders the command reads that its Go build
	// does not hold, clean slash-separated paths below the root, in the
	// order the file names them.
	Reads []string
	// Line is the gen line's line number.
	Line int
}

// workFile is what an ergo.work holds.
type workFile struct {
	skips []workSkip
	gens  []WorkGen
}

// ReadWorkFile reads the ergo.work at root: its generators, none when
// root holds no ergo.work.
func ReadWorkFile(root string) ([]WorkGen, error) {
	data, err := hostFS{}.ReadFile(filepath.Join(root, WorkFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wf, err := parseWorkFile(string(data))
	if err != nil {
		return nil, err
	}
	return wf.gens, nil
}

// genName reports whether s is a generator name: lower-case letters,
// digits and '-', starting with a letter.
func genName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// cleanBelow reports whether p is a clean, slash-separated path below
// the root: no "..", no "." element, not absolute.
func cleanBelow(p string) bool {
	return !strings.Contains(p, `\`) && filepath.IsLocal(filepath.FromSlash(p)) && pathClean(p) == p
}

// parseWorkFile reads an ergo.work (docs/spec/build.md, Workspace): blank
// lines and // comments; first the line `version N`; then lines of a key
// and its arguments. Version 1 knows one key, `skip DIR`, a folder below
// the root, slash-separated; version 2 adds `gen NAME COMMAND...` and
// `reads NAME PATH...`. A key the file's version does not know, or a
// newer version, is an error, never ignored: a later key changes what a
// run does, so a binary that cannot read it must not run as if it were
// absent.
func parseWorkFile(text string) (workFile, error) {
	var wf workFile
	gens := map[string]int{}
	version := 0
	for i, raw := range strings.Split(text, "\n") {
		line := i + 1
		if c := strings.Index(raw, "//"); c >= 0 {
			raw = raw[:c]
		}
		words := strings.Fields(raw)
		if len(words) == 0 {
			continue
		}
		if version == 0 {
			if words[0] != "version" || len(words) != 2 {
				return workFile{}, fmt.Errorf("%s:%d: the first line is version N", WorkFile, line)
			}
			v, err := strconv.Atoi(words[1])
			if err != nil || v < 1 {
				return workFile{}, fmt.Errorf("%s:%d: version %s is not a format version (1, 2, ...)", WorkFile, line, words[1])
			}
			if v > workVersion {
				return workFile{}, fmt.Errorf("%s:%d: version %d needs a newer ergo; this one reads version %d", WorkFile, line, v, workVersion)
			}
			version = v
			continue
		}
		switch {
		case words[0] == "skip":
			if len(words) != 2 {
				return workFile{}, fmt.Errorf("%s:%d: skip takes one folder", WorkFile, line)
			}
			dir := words[1]
			if !cleanBelow(dir) {
				return workFile{}, fmt.Errorf("%s:%d: skip %s is not a clean slash-separated folder below the workspace root", WorkFile, line, dir)
			}
			wf.skips = append(wf.skips, workSkip{dir: dir, line: line})
		case words[0] == "gen" && version >= 2:
			if len(words) < 3 || !genName(words[1]) {
				return workFile{}, fmt.Errorf("%s:%d: gen takes a name (a-z, 0-9, -) and a command", WorkFile, line)
			}
			if _, dup := gens[words[1]]; dup {
				return workFile{}, fmt.Errorf("%s:%d: gen %s is named twice", WorkFile, line, words[1])
			}
			gens[words[1]] = len(wf.gens)
			wf.gens = append(wf.gens, WorkGen{Name: words[1], Command: words[2:], Line: line})
		case words[0] == "reads" && version >= 2:
			if len(words) < 3 {
				return workFile{}, fmt.Errorf("%s:%d: reads takes a gen name and paths", WorkFile, line)
			}
			i, ok := gens[words[1]]
			if !ok {
				return workFile{}, fmt.Errorf("%s:%d: reads %s names no gen above it", WorkFile, line, words[1])
			}
			for _, p := range words[2:] {
				if !cleanBelow(p) {
					return workFile{}, fmt.Errorf("%s:%d: reads %s is not a clean slash-separated path below the workspace root", WorkFile, line, p)
				}
				wf.gens[i].Reads = append(wf.gens[i].Reads, p)
			}
		case version == 1:
			return workFile{}, fmt.Errorf("%s:%d: unknown key %s; version 1 knows skip", WorkFile, line, words[0])
		default:
			return workFile{}, fmt.Errorf("%s:%d: unknown key %s; version %d knows skip, gen and reads", WorkFile, line, words[0], version)
		}
	}
	if version == 0 {
		return workFile{}, fmt.Errorf("%s: the first line is version N", WorkFile)
	}
	return wf, nil
}

// pathClean is path.Clean of a slash-separated path.
func pathClean(p string) string { return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))) }

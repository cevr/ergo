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
const workVersion = 3

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

// WorkTask is a task of ergo.work (version 3; docs/spec/build.md,
// Tasks): its steps in order, the tasks it starts after, and the host
// variables its children get.
type WorkTask struct {
	Name string
	// Steps are the task's steps, one per task line, in order.
	Steps []WorkStep
	// After are the tasks it starts after, from its after lines.
	After []string
	// Env are the host variables its children get (its env lines); their
	// values are in every step's key.
	Env []string
	// Line is the line of its first task line; 0 for the default gate.
	Line int
}

// WorkStep is one step of a task: a built-in verb (Verb, with Words its
// words: check, fmt --check, lint, test, gen --check), a gen line's
// name (Gen) or another task's (Task).
type WorkStep struct {
	Words []string
	Verb  string
	Gen   string
	Task  string
	Line  int
}

// WorkDecl is what ergo.work declares for ergo task: its generators and its
// tasks, in the order the file names them, the default gate among them
// when the file writes no task gate line.
type WorkDecl struct {
	Gens  []WorkGen
	Tasks []WorkTask
}

// Task is the task named name, if the file declares it (or it is the
// default gate).
func (w WorkDecl) Task(name string) (WorkTask, bool) {
	for _, t := range w.Tasks {
		if t.Name == name {
			return t, true
		}
	}
	return WorkTask{}, false
}

// GateTask is the task every workspace has with no line written: check,
// fmt --check, lint, test and gen --check, in that order. Writing any
// task gate line replaces it whole.
const GateTask = "gate"

// GenTask is the task whose steps are the generators (ergo task gen); no
// task line may declare it.
const GenTask = "gen"

// builtinSteps are the built-in verbs a step may name, by their words.
var builtinSteps = map[string]string{
	"check":       "check",
	"fmt --check": "fmt",
	"lint":        "lint",
	"test":        "test",
	"gen --check": "gen",
}

// defaultGate is GateTask's steps when no task gate line is written.
func defaultGate() WorkTask {
	t := WorkTask{Name: GateTask}
	for _, words := range []string{"check", "fmt --check", "lint", "test", "gen --check"} {
		t.Steps = append(t.Steps, WorkStep{Words: strings.Fields(words), Verb: builtinSteps[words]})
	}
	return t
}

// workFile is what an ergo.work holds.
type workFile struct {
	skips []workSkip
	gens  []WorkGen
	tasks []WorkTask
}

// ReadWorkFile reads the ergo.work at root: its generators, none when
// root holds no ergo.work.
func ReadWorkFile(root string) ([]WorkGen, error) {
	w, err := ReadWork(root)
	return w.Gens, err
}

// ReadWork reads the ergo.work at root: its generators and tasks, the
// default gate among them; with no ergo.work, the default gate alone.
func ReadWork(root string) (WorkDecl, error) {
	data, err := hostFS{}.ReadFile(filepath.Join(root, WorkFile))
	if errors.Is(err, fs.ErrNotExist) {
		return WorkDecl{Tasks: []WorkTask{defaultGate()}}, nil
	}
	if err != nil {
		return WorkDecl{}, err
	}
	wf, err := parseWorkFile(string(data))
	if err != nil {
		return WorkDecl{}, err
	}
	return WorkDecl{Gens: wf.gens, Tasks: wf.tasks}, nil
}

// envName reports whether s is a host variable's name: upper-case
// letters, digits and '_', not starting with a digit.
func envName(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
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
// `reads NAME PATH...`; version 3 adds `task NAME STEP...`, `after NAME
// TASK...` and `env NAME VAR...` (resolveTasks). A key the file's
// version does not know, or a
// newer version, is an error, never ignored: a later key changes what a
// run does, so a binary that cannot read it must not run as if it were
// absent.
func parseWorkFile(text string) (workFile, error) {
	var wf workFile
	gens := map[string]int{}
	var lines []taskLine
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
		case (words[0] == "task" || words[0] == "after" || words[0] == "env") && version >= 3:
			if len(words) < 3 || !genName(words[1]) {
				return workFile{}, fmt.Errorf("%s:%d: %s takes a task name (a-z, 0-9, -) and %s", WorkFile, line, words[0], map[string]string{"task": "a step", "after": "tasks", "env": "variables"}[words[0]])
			}
			lines = append(lines, taskLine{key: words[0], name: words[1], args: words[2:], line: line})
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
		case version == 2:
			return workFile{}, fmt.Errorf("%s:%d: unknown key %s; version 2 knows skip, gen and reads", WorkFile, line, words[0])
		default:
			return workFile{}, fmt.Errorf("%s:%d: unknown key %s; version %d knows skip, gen, reads, task, after and env", WorkFile, line, words[0], version)
		}
	}
	if version == 0 {
		return workFile{}, fmt.Errorf("%s: the first line is version N", WorkFile)
	}
	tasks, err := resolveTasks(lines, gens)
	if err != nil {
		return workFile{}, err
	}
	wf.tasks = tasks
	return wf, nil
}

// taskLine is a task, after or env line, read before the tasks resolve
// (a step may name a task or gen declared further down).
type taskLine struct {
	key, name string
	args      []string
	line      int
}

// resolveTasks makes the tasks of the file's task, after and env lines,
// in the order their first task lines come, the default gate first when
// no task gate line is written. A step is a built-in verb's words, or one
// word naming a gen line or a task (GenTask included: the generators).
// After and env lines name a task the file declares or GateTask. A name
// declared as both a gen and a task, a task named GenTask, an unknown
// step, a repeated variable, and a cycle of tasks (through steps or after
// lines) are errors naming the line.
func resolveTasks(lines []taskLine, gens map[string]int) ([]WorkTask, error) {
	var tasks []WorkTask
	index := map[string]int{}
	for _, l := range lines {
		if l.key != "task" {
			continue
		}
		if l.name == GenTask {
			return nil, fmt.Errorf("%s:%d: task gen is the generators' task (ergo task gen); name this task otherwise", WorkFile, l.line)
		}
		if _, ok := gens[l.name]; ok {
			return nil, fmt.Errorf("%s:%d: task %s: a gen line has that name", WorkFile, l.line, l.name)
		}
		if _, ok := index[l.name]; !ok {
			index[l.name] = len(tasks)
			tasks = append(tasks, WorkTask{Name: l.name, Line: l.line})
		}
	}
	if _, ok := index[GateTask]; !ok {
		tasks = append([]WorkTask{defaultGate()}, tasks...)
		for n := range index {
			index[n]++
		}
		index[GateTask] = 0
	}
	// edges[i] are the tasks task i waits for, with the line saying so.
	type edge struct{ to, line int }
	edges := make([][]edge, len(tasks))
	for _, l := range lines {
		i, ok := index[l.name]
		if !ok {
			return nil, fmt.Errorf("%s:%d: %s %s names no task (a task line declares one; gate is always one)", WorkFile, l.line, l.key, l.name)
		}
		t := &tasks[i]
		switch l.key {
		case "task":
			words := strings.Join(l.args, " ")
			step := WorkStep{Words: l.args, Line: l.line}
			if verb, ok := builtinSteps[words]; ok {
				step.Verb = verb
			} else if j, ok := index[words]; ok && len(l.args) == 1 {
				step.Task = words
				edges[i] = append(edges[i], edge{j, l.line})
			} else if _, ok := gens[words]; ok && len(l.args) == 1 {
				step.Gen = words
			} else if words == GenTask {
				step.Task = GenTask
			} else {
				return nil, fmt.Errorf("%s:%d: task %s: step %q is no built-in verb (check, fmt --check, lint, test, gen --check), gen line or task", WorkFile, l.line, l.name, words)
			}
			t.Steps = append(t.Steps, step)
		case "after":
			for _, a := range l.args {
				j, ok := index[a]
				if !ok && a != GenTask {
					return nil, fmt.Errorf("%s:%d: after %s: %s names no task", WorkFile, l.line, l.name, a)
				}
				if slices.Contains(t.After, a) {
					return nil, fmt.Errorf("%s:%d: after %s: %s is named twice", WorkFile, l.line, l.name, a)
				}
				t.After = append(t.After, a)
				if ok {
					edges[i] = append(edges[i], edge{j, l.line})
				}
			}
		case "env":
			for _, v := range l.args {
				if !envName(v) {
					return nil, fmt.Errorf("%s:%d: env %s: %s is not a variable name (A-Z, 0-9, _)", WorkFile, l.line, l.name, v)
				}
				if slices.Contains(t.Env, v) {
					return nil, fmt.Errorf("%s:%d: env %s: %s is named twice", WorkFile, l.line, l.name, v)
				}
				t.Env = append(t.Env, v)
			}
		}
	}
	// A cycle of tasks never starts: the line that closes it is named.
	state := make([]int, len(tasks))
	var visit func(i int) error
	visit = func(i int) error {
		state[i] = 1
		for _, e := range edges[i] {
			switch state[e.to] {
			case 1:
				return fmt.Errorf("%s:%d: task %s waits for %s, which waits for it (a cycle of tasks)", WorkFile, e.line, tasks[i].Name, tasks[e.to].Name)
			case 0:
				if err := visit(e.to); err != nil {
					return err
				}
			}
		}
		state[i] = 2
		return nil
	}
	for i := range tasks {
		if state[i] == 0 {
			if err := visit(i); err != nil {
				return nil, err
			}
		}
	}
	return tasks, nil
}

// pathClean is path.Clean of a slash-separated path.
func pathClean(p string) string { return filepath.ToSlash(filepath.Clean(filepath.FromSlash(p))) }

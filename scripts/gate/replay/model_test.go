package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWholeGateModel runs generated command sequences against a reference
// model and against the real key, store, record, replay, withdrawal and
// merge comparison, and requires that they agree after every command.
//
// The model holds the key inputs as it understands them (projection), the
// store (records by projection, objects by content and their state) and so
// the last recorded run per key. The real side keeps a checkout in a
// temporary directory, collects Facts with Walk and Links as package main
// does (package main only adds Git's classes, the tool queries and the host
// facts), and runs the protocol of this package. The oracle:
//
//	(a) soundness: a replay happens only when the model has a record at the
//	    current inputs, made by a passed run whose start and end inputs were
//	    these, and every object it names is intact; it prints the recorded
//	    outputs;
//	(b) minimality: with equal inputs, an intact store and a recorded pass,
//	    the real gate replays; an unkeyed change (group or other bits, a
//	    variable no step receives, the mtime of a tracked file) still replays;
//	(c) a replay holds every step, or the gate runs;
//	(d) a failure is never recorded, and a failed run withdraws its key;
//	(e) merge: the rows are equal exactly when the verdicts are, a
//	    difference exits 1 and withdraws, and with no record the inner tier
//	    runs first.
//
// Ignored files are keyed by stat, so the model gives each one a generation
// that every write, chmod, rename or touch increases, and the test gives
// each such change a new mtime, so the stat tuple changes with it. The
// RecentWindow rule is tested by TestRecentChangesAreNotRecorded; here the
// collection time is an hour ahead, so no path is recent.
func TestWholeGateModel(t *testing.T) {
	const sequences = 1000
	const maxCommands = 50
	workers := max(2, runtime.NumCPU())
	seeds := make(chan uint64)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string
	for range workers {
		wg.Go(func() {
			for seed := range seeds {
				w := newWorld(t, seed)
				summary, err := w.sequence(maxCommands)
				w.close()
				mu.Lock()
				if err != nil {
					failed = append(failed, fmt.Sprintf("seed %d: %v\ncommands:\n%s", seed, err, strings.Join(w.log, "\n")))
				} else {
					t.Logf("seed %d: %s", seed, summary)
				}
				mu.Unlock()
			}
		})
	}
	for seed := uint64(1); seed <= sequences; seed++ {
		seeds <- seed
	}
	close(seeds)
	wg.Wait()
	slices.Sort(failed)
	for i, failure := range failed {
		if i == 3 {
			t.Errorf("... and %d more failing seeds", len(failed)-3)
			break
		}
		t.Error(failure)
	}
}

var (
	filePool  = []string{"a", "b", "c", "d/e", "d/f", "g/h/i"}
	emptyDirs = []string{"x", "y", "y/z"}
	linkNames = []string{"l1", "l2"}
	contents  = []string{"", "one\n", "two\n", "three three\n"}
	outputs   = []string{"", "ok\n", "line one\nline two\n", "warning: x\n"}
	steps     = []string{"s1", "s2", "s3"}
	// keyedBits and unkeyedBits are the permission bits a chmod toggles.
	keyedBits   = []fs.FileMode{0o400, 0o100, 0o010, 0o001}
	unkeyedBits = []fs.FileMode{0o040, 0o020, 0o004, 0o002}
	classes     = []Class{Tracked, Untracked, Ignored}
)

type node struct {
	kind    Kind
	class   Class
	content string
	perm    fs.FileMode
	gen     int
	target  string
}

// mrecord is the model's W(K): each step's output.
type mrecord struct{ outputs []string }

type world struct {
	t                      *testing.T
	rng                    *rand.Rand
	base, root             string
	outside, keyed, spools string
	store                  Store
	log                    []string
	mtime                  int64

	nodes map[string]*node
	index map[string]string
	head  int
	exe   string
	env   string
	host  string
	tool  string

	// The model store.
	records map[string]*mrecord
	objects map[string]string // content -> "ok" or "damaged"; absent: missing
	keys    map[string]string // real key -> projection, for the sweeper

	replays, recorded int

	// last is the last collection, still current while no edit came after
	// it (dirty is false); check uses it to save a collection.
	last  *Facts
	dirty bool
}

func newWorld(t *testing.T, seed uint64) *world {
	base, err := os.MkdirTemp("", "gate-model-")
	if err == nil {
		// Link targets name the resolved paths, as a collector's keyed
		// roots are (darwin's temporary directory is behind a link).
		base, err = filepath.EvalSymlinks(base)
	}
	if err != nil {
		panic(err)
	}
	w := &world{
		t: t, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), base: base,
		root: filepath.Join(base, "checkout"), outside: filepath.Join(base, "outside"),
		keyed: filepath.Join(base, "goroot"), spools: filepath.Join(base, "spools"),
		nodes: map[string]*node{}, index: map[string]string{},
		exe: "exe-1", env: "flags-1", host: "kernel-1",
		records: map[string]*mrecord{}, objects: map[string]string{}, keys: map[string]string{},
		mtime: 1_000_000_000,
	}
	for _, dir := range []string{w.root, w.outside, w.keyed, w.spools} {
		w.must(os.MkdirAll(dir, 0o755))
	}
	w.must(os.WriteFile(filepath.Join(w.outside, "f"), []byte("outside\n"), 0o644))
	w.must(os.WriteFile(filepath.Join(w.keyed, "f"), []byte("keyed\n"), 0o644))
	w.tool = sum("keyed\n")
	w.store = Open(filepath.Join(base, "cache"))
	// Start with a few files of each class.
	for i := 0; i < 3; i++ {
		w.addFile()
	}
	return w
}

// close removes the world. Only files lose permission bits, never
// directories, so everything can be removed.
func (w *world) close() {
	_ = os.RemoveAll(w.base)
}

// must records a setup failure; the sequence then fails at its next check.
func (w *world) must(err error) {
	if err != nil {
		panic(err)
	}
}

func sum(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func (w *world) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

func pick[T any](rng *rand.Rand, items []T) T { return items[rng.IntN(len(items))] }

func (w *world) abs(name string) string { return filepath.Join(w.root, filepath.FromSlash(name)) }

// touch gives a path a new mtime, so its stat tuple changes with the
// generation the model gives it.
func (w *world) touch(name string) {
	w.mtime++
	when := time.Unix(w.mtime, 0)
	w.must(os.Chtimes(w.abs(name), when, when))
	// Unique across the sequence: a file deleted and made again is another
	// stat tuple.
	w.nodes[name].gen = int(w.mtime)
}

func (w *world) files() []string {
	var names []string
	for name, n := range w.nodes {
		if n.kind == File {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (w *world) ensureDirs(name string) {
	for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
		if _, ok := w.nodes[dir]; !ok {
			w.must(os.MkdirAll(w.abs(dir), 0o755))
			w.nodes[dir] = &node{kind: Directory}
		}
	}
}

// ---- edits ----

func (w *world) addFile() {
	var free []string
	for _, name := range filePool {
		if _, ok := w.nodes[name]; !ok && !w.blocked(name) {
			free = append(free, name)
		}
	}
	if len(free) == 0 {
		return
	}
	name := pick(w.rng, free)
	class := pick(w.rng, classes)
	content := pick(w.rng, contents)
	w.ensureDirs(name)
	w.must(os.WriteFile(w.abs(name), []byte(content), 0o644))
	w.must(os.Chmod(w.abs(name), 0o644))
	w.nodes[name] = &node{kind: File, class: class, content: content, perm: 0o644}
	w.touch(name)
	if class == Tracked {
		w.index[name] = sum(content)
	}
	w.note("add %s (%s) %q", name, class, content)
}

// blocked reports whether a pool path cannot be created because a parent
// is a file (never, in this pool) or the path is a directory.
func (w *world) blocked(name string) bool {
	n, ok := w.nodes[name]
	return ok && n.kind == Directory
}

func (w *world) editFile() {
	names := w.files()
	if len(names) == 0 {
		w.addFile()
		return
	}
	name := pick(w.rng, names)
	n := w.nodes[name]
	content := pick(w.rng, contents)
	if n.perm&0o200 == 0 {
		return
	}
	w.must(os.WriteFile(w.abs(name), []byte(content), 0o644))
	n.content = content
	w.touch(name)
	w.note("edit %s %q", name, content)
}

func (w *world) deleteFile() {
	names := w.files()
	if len(names) == 0 {
		return
	}
	name := pick(w.rng, names)
	w.must(os.Remove(w.abs(name)))
	delete(w.nodes, name)
	w.note("delete %s", name)
}

func (w *world) renameFile() {
	names := w.files()
	var free []string
	for _, name := range filePool {
		if _, ok := w.nodes[name]; !ok {
			free = append(free, name)
		}
	}
	if len(names) == 0 || len(free) == 0 {
		return
	}
	from, to := pick(w.rng, names), pick(w.rng, free)
	w.ensureDirs(to)
	w.must(os.Rename(w.abs(from), w.abs(to)))
	w.nodes[to] = w.nodes[from]
	delete(w.nodes, from)
	w.touch(to)
	w.note("rename %s %s", from, to)
}

func (w *world) chmodFile() {
	names := w.files()
	if len(names) == 0 {
		return
	}
	name := pick(w.rng, names)
	n := w.nodes[name]
	bits := keyedBits
	if w.rng.IntN(2) == 0 {
		bits = unkeyedBits
	}
	bit := pick(w.rng, bits)
	if bit == 0o400 && os.Geteuid() == 0 {
		return
	}
	n.perm ^= bit
	w.must(os.Chmod(w.abs(name), n.perm))
	w.touch(name)
	w.note("chmod %s %o", name, n.perm)
}

// touchFile changes only a file's times; rewriteFile rewrites the same
// content. Neither changes a tracked or untracked file's key.
func (w *world) touchFile() {
	names := w.files()
	if len(names) == 0 {
		return
	}
	name := pick(w.rng, names)
	if w.rng.IntN(2) == 0 && w.nodes[name].perm&0o200 != 0 {
		w.must(os.WriteFile(w.abs(name), []byte(w.nodes[name].content), 0o644))
		w.note("rewrite %s", name)
	} else {
		w.note("touch %s", name)
	}
	w.touch(name)
}

// moveClass is git add, git rm --cached or a new ignore rule.
func (w *world) moveClass() {
	names := w.files()
	if len(names) == 0 {
		return
	}
	name := pick(w.rng, names)
	n := w.nodes[name]
	var others []Class
	for _, c := range classes {
		if c != n.class {
			others = append(others, c)
		}
	}
	n.class = pick(w.rng, others)
	if n.class == Tracked {
		w.index[name] = sum(n.content)
	} else {
		delete(w.index, name)
	}
	w.note("class %s %s", name, n.class)
}

// stage is git add of a tracked file's current content.
func (w *world) stage() {
	for _, name := range w.files() {
		if n := w.nodes[name]; n.class == Tracked {
			w.index[name] = sum(n.content)
		}
	}
	w.note("stage")
}

func (w *world) emptyDir() {
	name := pick(w.rng, append(slices.Clone(emptyDirs), "d", "g/h"))
	if _, ok := w.nodes[name]; ok {
		for other := range w.nodes {
			if strings.HasPrefix(other, name+"/") {
				return
			}
		}
		w.must(os.Remove(w.abs(name)))
		delete(w.nodes, name)
		w.note("rmdir %s", name)
		return
	}
	if strings.Contains(name, "/") {
		w.ensureDirs(name)
	}
	if _, file := w.nodes[name]; file {
		return
	}
	w.must(os.Mkdir(w.abs(name), 0o755))
	w.nodes[name] = &node{kind: Directory}
	w.note("mkdir %s", name)
}

// linkTargets are the targets a link takes: inside the checkout (a file, a
// missing name, a directory, another link), into the keyed root, outside
// (a file, a missing name, by a relative path that climbs out).
func (w *world) linkTargets() []string {
	return []string{"a", "nope", "d", "l1", "l2", filepath.Join(w.keyed, "f"), filepath.Join(w.outside, "f"), filepath.Join(w.outside, "missing"), "../outside/f", "./a"}
}

func (w *world) retargetLink() {
	name := pick(w.rng, linkNames)
	if _, ok := w.nodes[name]; ok {
		w.must(os.Remove(w.abs(name)))
		delete(w.nodes, name)
	}
	if w.rng.IntN(5) == 0 {
		w.note("unlink %s", name)
		return
	}
	target := pick(w.rng, w.linkTargets())
	w.must(os.Symlink(target, w.abs(name)))
	class := pick(w.rng, []Class{Untracked, Ignored})
	w.nodes[name] = &node{kind: Symlink, class: class, target: target}
	w.note("link %s -> %s (%s)", name, target, class)
}

// linkOut reports whether the link leads outside every keyed root.
func (w *world) linkOut(name string, hops int) bool {
	if hops > maxLinkHops {
		return true
	}
	target := w.nodes[name].target
	switch {
	case target == filepath.Join(w.keyed, "f"):
		return false
	case filepath.IsAbs(target) || strings.HasPrefix(target, "../"):
		return true
	}
	if next, ok := w.nodes[target]; ok && next.kind == Symlink {
		return w.linkOut(target, hops+1)
	}
	return false
}

func (w *world) hostChange() {
	switch w.rng.IntN(5) {
	case 0:
		content := fmt.Sprintf("keyed %d\n", w.rng.IntN(3))
		w.must(os.WriteFile(filepath.Join(w.keyed, "f"), []byte(content), 0o644))
		w.tool = sum(content)
		w.note("tool %q", content)
	case 1:
		w.env = fmt.Sprintf("flags-%d", w.rng.IntN(3))
		w.note("received variable %s", w.env)
	case 2:
		// A variable no step receives is not a fact.
		w.note("unreceived variable")
	case 3:
		w.host = fmt.Sprintf("kernel-%d", w.rng.IntN(3))
		w.note("host %s", w.host)
	case 4:
		w.exe = fmt.Sprintf("exe-%d", w.rng.IntN(3))
		w.note("executable %s", w.exe)
	}
}

func (w *world) commit() {
	w.head++
	w.note("commit (same tree)")
}

// edit is any change to the checkout or the host.
func (w *world) edit() {
	w.dirty = true
	switch w.rng.IntN(14) {
	case 0, 1:
		w.editFile()
	case 2:
		w.addFile()
	case 3:
		w.deleteFile()
	case 4:
		w.renameFile()
	case 5:
		w.chmodFile()
	case 6:
		w.touchFile()
	case 7:
		w.moveClass()
	case 8:
		w.stage()
	case 9:
		w.emptyDir()
	case 10:
		w.retargetLink()
	case 11:
		w.hostChange()
	case 12:
		w.commit()
	case 13:
		w.touchFile()
	}
}

// ---- the model's key inputs ----

func (w *world) projection() string {
	var b strings.Builder
	fmt.Fprintf(&b, "head %d\nexe %s\nenv %s\nhost %s\ntool %s\n", w.head, w.exe, w.env, w.host, w.tool)
	var index []string
	for name, blob := range w.index {
		index = append(index, name+" "+blob)
	}
	slices.Sort(index)
	fmt.Fprintf(&b, "index %s\n", strings.Join(index, ","))
	var names []string
	for name := range w.nodes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		n := w.nodes[name]
		switch {
		case n.kind == Directory:
			fmt.Fprintf(&b, "%s dir\n", name)
		case n.kind == Symlink:
			fmt.Fprintf(&b, "%s link %s %s\n", name, n.class, n.target)
		case n.class == Ignored:
			fmt.Fprintf(&b, "%s ignored gen %d\n", name, n.gen)
		default:
			fmt.Fprintf(&b, "%s %s %o %q\n", name, n.class, n.perm&KeyedPermissions, n.content)
		}
	}
	return b.String()
}

// replayable reports whether the model's inputs can key a replay: no link
// leads out, and every content-keyed file is readable.
func (w *world) replayable() bool {
	for name, n := range w.nodes {
		if n.kind == Symlink && w.linkOut(name, 1) {
			return false
		}
		if n.kind == File && n.class != Ignored && n.perm&0o400 == 0 && os.Geteuid() != 0 {
			return false
		}
	}
	return true
}

// ---- the real side ----

func (w *world) classify(name string) Class {
	if n, ok := w.nodes[name]; ok {
		return n.class
	}
	return Ignored
}

func (w *world) collect() (*Facts, error) {
	entries, recent, unreplayable, err := Walk(w.root, w.classify, time.Now().Add(time.Hour))
	if err != nil {
		return nil, err
	}
	links, err := NewLinks(w.root, []string{w.keyed})
	if err != nil {
		return nil, err
	}
	unreplayable = append(unreplayable, links.FollowLinks(entries)...)
	var index []string
	for name, blob := range w.index {
		index = append(index, fmt.Sprintf("100644 %s 0\t%s", blob, name))
	}
	facts := &Facts{
		Executable: w.exe, Root: w.root, Head: strconv.Itoa(w.head), Index: index, Entries: entries,
		Environment:  []Variable{{"GOFLAGS", true, w.env}, {"PATH", true, "/bin"}},
		Settings:     []Variable{{"ERGO_GATE_CACHE", true, w.store.Dir}},
		Host:         []Fact{{"kernel", w.host}, {"program go", w.tool}},
		Unreplayable: unreplayable, Recent: recent,
	}
	w.keys[facts.Key()] = w.projection()
	w.last, w.dirty = facts, false
	return facts, nil
}

func (w *world) spool() (*os.File, error) {
	file, err := os.CreateTemp(w.spools, "spool-")
	if err != nil {
		return nil, err
	}
	return file, os.Remove(file.Name())
}

// ---- gates ----

// expectReplay is the model's answer: a record at the current inputs whose
// every object is intact.
func (w *world) expectReplay() *mrecord {
	if !w.replayable() {
		return nil
	}
	record := w.records[w.projection()]
	if record == nil {
		return nil
	}
	for _, output := range record.outputs {
		if w.objects[output] != "ok" {
			return nil
		}
	}
	return record
}

// innerGate runs the inner tier (replay allowed unless noCache), with an
// edit between the start and the end key when editDuring.
func (w *world) innerGate(noCache, editDuring bool) error {
	start, err := w.collect()
	if err != nil {
		return err
	}
	if !noCache {
		want := w.expectReplay()
		replay, reason := w.store.TryReplay(start, w.spool)
		if (replay != nil) != (want != nil) {
			return fmt.Errorf("replay %t (%s), model says %t", replay != nil, reason, want != nil)
		}
		if replay != nil {
			defer replay.Close()
			w.replays++
			return w.checkReplay(replay, want)
		}
	}
	return w.runGate(start, w.verdicts(), editDuring)
}

func (w *world) checkReplay(replay *Replay, want *mrecord) error {
	if len(replay.Record.Steps) != len(steps) || len(replay.Outputs) != len(steps) {
		return fmt.Errorf("replay holds %d steps, want %d", len(replay.Record.Steps), len(steps))
	}
	var printed bytes.Buffer
	if err := replay.Print(&printed); err != nil {
		return err
	}
	var expected strings.Builder
	for i, name := range steps {
		if replay.Record.Steps[i].Name != name {
			return fmt.Errorf("replay step %d is %q, want %q", i, replay.Record.Steps[i].Name, name)
		}
		expected.WriteString("== " + name + ": ok, replayed (passed in ")
		expected.WriteString(want.outputs[i])
	}
	// Strip the times from the printed header lines.
	var got strings.Builder
	for _, line := range strings.SplitAfter(printed.String(), "\n") {
		if strings.HasPrefix(line, "== ") {
			line = line[:strings.Index(line, "(passed in ")+len("(passed in ")]
		}
		if strings.HasPrefix(line, "gate: replayed ") {
			if !strings.HasPrefix(line, fmt.Sprintf("gate: replayed %d steps (key ", len(steps))) {
				return fmt.Errorf("replay closing line %q", line)
			}
			continue
		}
		got.WriteString(line)
	}
	if got.String() != expected.String() {
		return fmt.Errorf("replay printed %q, want %q", got.String(), expected.String())
	}
	return nil
}

type verdict struct {
	pass   bool
	output string
}

func (w *world) verdicts() []verdict {
	v := make([]verdict, len(steps))
	for i := range v {
		v[i] = verdict{pass: w.rng.IntN(8) != 0, output: pick(w.rng, outputs)}
	}
	return v
}

func allPass(v []verdict) bool {
	return !slices.ContainsFunc(v, func(x verdict) bool { return !x.pass })
}

// runGate runs the steps with the given verdicts from the start facts,
// then finishes the run, and applies the same run to the model.
func (w *world) runGate(start *Facts, verdicts []verdict, editDuring bool) error {
	run, finish, err := w.begin(start, verdicts)
	if err != nil {
		return err
	}
	if editDuring {
		w.edit()
		w.note("  (edit during the run)")
	}
	return finish(run)
}

// begin starts a run; the returned finish ends it and applies it to the
// model at the start inputs.
func (w *world) begin(start *Facts, verdicts []verdict) (*Run, func(*Run) error, error) {
	startProjection := w.keys[start.Key()]
	startReplayable := start.Problem() == ""
	run := w.store.Begin(start)
	for i, name := range steps {
		object, err := run.Output()
		if err != nil {
			return nil, nil, err
		}
		_, _ = object.Write([]byte(verdicts[i].output))
		if verdicts[i].pass {
			run.Passed(name, object, "ran", 1.5, time.Now())
		}
	}
	finish := func(run *Run) error {
		passed := allPass(verdicts)
		line := run.Finish(passed, w.collect, Meta{Head: "head", Tree: "tree", Clean: true, Now: time.Now()})
		w.note("  run %v -> %s", passedNames(verdicts), line)
		if !passed {
			delete(w.records, startProjection)
			return nil
		}
		if !startReplayable {
			return nil
		}
		for _, v := range verdicts {
			w.objects[v.output] = "ok"
		}
		if w.projection() == startProjection && w.replayable() {
			record := &mrecord{}
			for _, v := range verdicts {
				record.outputs = append(record.outputs, v.output)
			}
			w.records[startProjection] = record
			w.recorded++
		}
		return nil
	}
	return run, finish, nil
}

func passedNames(v []verdict) string {
	var b strings.Builder
	for i, x := range v {
		if x.pass {
			b.WriteString(steps[i] + ":pass ")
		} else {
			b.WriteString(steps[i] + ":FAIL ")
		}
	}
	return strings.TrimSpace(b.String())
}

// twoGates starts two runs at one key and finishes them in either order.
func (w *world) twoGates() error {
	startA, err := w.collect()
	if err != nil {
		return err
	}
	startB, err := w.collect()
	if err != nil {
		return err
	}
	if startA.Key() != startB.Key() {
		return fmt.Errorf("two collections of one state differ: %v", Diff(startA, startB, 5))
	}
	runA, finishA, err := w.begin(startA, w.verdicts())
	if err != nil {
		return err
	}
	runB, finishB, err := w.begin(startB, w.verdicts())
	if err != nil {
		return err
	}
	if w.rng.IntN(2) == 0 {
		w.note("two gates, A finishes first")
		if err := finishA(runA); err != nil {
			return err
		}
		return finishB(runB)
	}
	w.note("two gates, B finishes first")
	if err := finishB(runB); err != nil {
		return err
	}
	return finishA(runA)
}

// mergeGate runs the merge tier with generated uncached verdicts.
func (w *world) mergeGate() error {
	projection := w.projection()
	_, hadRecord := w.records[projection]
	if !w.replayable() {
		hadRecord = false
	}
	innerVerdicts := w.verdicts()
	uncached := map[string]Verdict{}
	all := w.rng.IntN(3) != 0
	for _, name := range steps {
		uncached[name] = Pass
		if !all {
			uncached[name] = pick(w.rng, []Verdict{Pass, Pass, Failed, NotRun})
		}
	}
	// What happens during the uncached run: nothing, a keyed edit (a
	// source, an ignored file or the host), or an end collection that
	// fails. A proven mismatch withdraws W(K) in each case.
	during := pick(w.rng, []string{"nothing", "nothing", "edit", "collection error"})
	failCollect := false
	var startKey string
	collect := func() (*Facts, error) {
		if failCollect {
			failCollect = false
			return nil, fmt.Errorf("the end collection failed")
		}
		facts, err := w.collect()
		if err == nil && startKey == "" {
			startKey = facts.Key()
		}
		return facts, err
	}
	innerRan := false
	var innerErr error
	var record *mrecord
	tier := MergeTier{
		Store:   w.store,
		Collect: collect,
		Inner: func() bool {
			innerRan = true
			start, err := w.collect()
			if err != nil {
				innerErr = err
				return false
			}
			innerErr = w.runGate(start, innerVerdicts, false)
			return allPass(innerVerdicts)
		},
		Uncached: func() ([]string, map[string]Verdict) {
			// The record the uncached verdicts answer for: W(K) at the
			// start, after the inner tier, before any edit below.
			record = w.records[projection]
			if !w.replayable() {
				record = nil
			}
			switch during {
			case "edit":
				w.edit()
				w.note("  (edit during the uncached run)")
			case "collection error":
				failCollect = true
			}
			return steps, uncached
		},
	}
	var out bytes.Buffer
	code := tier.Run(&out)
	if innerErr != nil {
		return innerErr
	}
	w.note("merge (record %t, uncached %v, during: %s) -> %d", hadRecord, uncached, during, code)
	if innerRan == hadRecord {
		return fmt.Errorf("merge: inner tier ran %t with a record %t", innerRan, hadRecord)
	}
	if innerRan && !allPass(innerVerdicts) {
		if code != 1 || !strings.Contains(out.String(), "merge: FAILED: the inner tier failed\n") || strings.Contains(out.String(), "uncached") {
			return fmt.Errorf("merge after a failed inner tier exited %d:\n%s", code, out.String())
		}
		return nil
	}
	var want []string
	var differ []string
	for _, name := range steps {
		in := None
		if record != nil {
			in = Pass
		}
		want = append(want, fmt.Sprintf("merge: %s: inner %s, uncached %s", name, in, uncached[name]))
		if in != Pass || uncached[name] != Pass {
			differ = append(differ, name)
		}
	}
	// With equal verdicts, a change or a failed collection at the end may
	// fail the merge before its summary; a difference always prints it.
	endUnknown := len(differ) == 0 && during != "nothing"
	if len(differ) > 0 {
		want = append(want, "merge: FAILED: verdicts differ: "+strings.Join(differ, ", "))
	} else if !endUnknown {
		want = append(want, fmt.Sprintf("merge: verdicts equal (%d steps)", len(steps)))
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.HasPrefix(line, "merge: s") || strings.HasPrefix(line, "merge: FAILED: verdicts") || (!endUnknown && strings.HasPrefix(line, "merge: verdicts")) {
			got = append(got, line)
		}
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("merge printed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(differ) > 0 {
		// (d), (e): a proven mismatch withdraws W(K), whatever the end
		// collection found.
		delete(w.records, projection)
		if _, err := w.store.Load(startKey); err == nil {
			return fmt.Errorf("merge: verdicts differ (during: %s) and W(K) %s is still there", during, startKey[:12])
		}
		if code != 1 {
			return fmt.Errorf("merge exited %d with differing verdicts, want 1", code)
		}
		return nil
	}
	switch {
	case during == "collection error" && code != 1:
		return fmt.Errorf("merge exited %d after a failed end collection, want 1", code)
	case during == "edit" && code != 0 && code != 1:
		return fmt.Errorf("merge exited %d after an edit, want 0 or 1", code)
	case during == "nothing" && code != 0:
		return fmt.Errorf("merge exited %d, want 0", code)
	}
	return nil
}

// ---- the sweeper ----

func (w *world) storeFiles(kind string) []string {
	matches, _ := filepath.Glob(filepath.Join(w.store.Dir, kind, "*", "*"))
	var files []string
	for _, match := range matches {
		if !strings.HasPrefix(filepath.Base(match), ".") {
			files = append(files, match)
		}
	}
	slices.Sort(files)
	return files
}

func (w *world) sweep() error {
	switch w.rng.IntN(4) {
	case 0, 1:
		objects := w.storeFiles("objects")
		if len(objects) == 0 {
			return nil
		}
		name := pick(w.rng, objects)
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if w.rng.IntN(2) == 0 {
			w.must(os.Remove(name))
			delete(w.objects, string(content))
			w.note("sweep object %q", content)
		} else {
			// Damage in place, as a disk error or an editor would: the
			// same file, other bytes.
			damaged := pick(w.rng, []string{"garbage", string(content) + "x", ""})
			if damaged == string(content) {
				damaged = "garbage"
			}
			w.must(os.WriteFile(name, []byte(damaged), 0o644))
			w.objects[string(content)] = "damaged"
			w.note("damage object %q", content)
		}
	default:
		records := w.storeFiles("whole")
		if len(records) == 0 {
			return nil
		}
		name := pick(w.rng, records)
		key := strings.TrimSuffix(filepath.Base(name), ".json")
		projection, ok := w.keys[key]
		if !ok {
			return fmt.Errorf("a record under key %s that no collection made", key[:12])
		}
		if w.rng.IntN(2) == 0 {
			w.must(os.Remove(name))
			w.note("sweep record")
		} else {
			w.must(os.WriteFile(name, []byte("{not json"), 0o644))
			w.note("damage record")
		}
		delete(w.records, projection)
	}
	return nil
}

// ---- the state check after every command ----

// check compares the real store at the current key with the model: a
// record exists exactly when the model has one, and it names the model's
// outputs (d).
func (w *world) check() error {
	facts := w.last
	if w.dirty || facts == nil {
		var err error
		if facts, err = w.collect(); err != nil {
			return err
		}
	}
	key := facts.Key()
	if (facts.Problem() == "") != w.replayable() {
		return fmt.Errorf("facts replayable %t (%q), model says %t", facts.Problem() == "", facts.Problem(), w.replayable())
	}
	record, err := w.store.Load(key)
	want := w.records[w.projection()]
	if (record != nil) != (want != nil) {
		return fmt.Errorf("a record at the current key %t (%v), model says %t", record != nil, err, want != nil)
	}
	if record != nil {
		for i, step := range record.Steps {
			if step.Object != sum(want.outputs[i]) {
				return fmt.Errorf("record step %s names %s, model says %q", step.Name, step.Object[:12], want.outputs[i])
			}
		}
	}
	return nil
}

func (w *world) sequence(maxCommands int) (summary string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	commands := 1 + w.rng.IntN(maxCommands)
	for i := 0; i < commands; i++ {
		switch roll := w.rng.IntN(20); {
		case roll < 9:
			w.edit()
		case roll < 13:
			w.note("gate")
			err = w.innerGate(false, w.rng.IntN(4) == 0)
		case roll < 14:
			w.note("gate --no-cache")
			err = w.innerGate(true, false)
		case roll < 16:
			err = w.mergeGate()
		case roll < 17:
			err = w.twoGates()
		default:
			err = w.sweep()
		}
		if err == nil {
			err = w.check()
		}
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d commands, %d replays, %d records", commands, w.replays, w.recorded), nil
}

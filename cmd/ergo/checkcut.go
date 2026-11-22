package main

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
)

// The early cutoff of `ergo check DIR/...` (MONOREPO WS0b; docs/spec/
// build.md, Warm answer by tokens 5). When the warm answer by tokens
// misses, the shared load checks every package again, but a package's
// evaluation is taken from the store when nothing it can observe
// changed:
//
//   - Its own source fingerprint (token text and `// ergo:` markers, the
//     path it is checked as and the environment; check.FingerprintOf),
//     and its folder: a comment or layout edit of its own changes none.
//   - The interface fingerprint of each local import: the static half of
//     its answer reads no more of an import (CS5a).
//   - When its answer holds an obligation, the implementation fingerprint
//     of each local import as well: evaluation runs imported bodies, so a
//     verdict is keyed by implementation, never by interface (CS5a). A
//     body edit re-runs the edited package and its importers that hold
//     obligations; a package that imports nothing edited, or that has no
//     obligation, is taken as it was.
//   - The inputs the run read that are not the load's sources (ergo.mod,
//     go.mod, go.sum, the law ledger, assets), by bytes.
//
// The answer is printed again as the warm answer prints one (renderTree):
// the revision of the files now and each site where its token now is. It
// is kept when it would be kept there and its evaluation stored no law
// verdict. A package whose check now has a diagnostic is evaluated.
//
// Prior art: Bazel's Skyframe prunes a node whose recomputed value is
// unchanged; OCaml, ReScript and Go stop at an unchanged interface; Salsa
// verifies a memoized query's inputs before reusing it. The eval read set
// (WS0c) narrows the implementation half to the packages an evaluation
// entered.

// treeCut is one package's kept evaluation for the cutoff.
type treeCut struct {
	Entry treeEntry `json:"entry"`
	// Inputs are the run's inputs that are not the load's sources.
	Inputs sketchfront.Inputs `json:"inputs"`
	// Impls are the implementation fingerprints of the local imports,
	// checked when Obligations.
	Impls       map[string]string `json:"impls"`
	Obligations bool              `json:"obligations"`
}

// treeRerun, when not nil, hears each package the cutoff evaluated (a
// test's account of the work it saved).
var treeRerun func(path string)

// cutKey is the cutoff's key of a's root package and the implementation
// fingerprints of its local imports; ok is false when the root has no
// source fingerprint, an import has no interface, or a package is of a
// required module.
func (t *checkTree) cutKey(a sketchfront.Analysis) (string, map[string]string, bool) {
	for _, p := range a.Packages() {
		if p.Module != "" {
			return "", nil, false
		}
	}
	fps := a.Fingerprints()
	if len(fps) == 0 {
		return "", nil, false
	}
	byPath := map[string]fingerprint.Package{}
	for _, fp := range fps {
		byPath[fp.Path] = fp
	}
	root := fps[len(fps)-1]
	if root.Source == "" {
		return "", nil, false
	}
	parts := []string{treeSchema, "cut", t.base, root.Path, a.Root().Dir, root.Source}
	impls := map[string]string{}
	for _, imp := range root.Imports {
		q := byPath[imp]
		if q.Interface == "" || q.Implementation == "" {
			return "", nil, false
		}
		parts = append(parts, imp, q.Interface)
		impls[imp] = q.Implementation
	}
	return lawcache.Key(parts...), impls, true
}

// programSources are the source files of a's program, by absolute name.
func programSources(a sketchfront.Analysis) (string, map[string]string, bool) {
	root := a.ModuleRoot()
	if root == "" {
		return "", nil, false
	}
	srcs := map[string]string{}
	for _, p := range a.Packages() {
		for _, f := range p.Files {
			if f.Syntax == nil {
				return "", nil, false
			}
			srcs[filepath.Join(root, filepath.FromSlash(f.Rel))] = f.Syntax.Src
		}
	}
	return root, srcs, true
}

// replay is the printed answer of a's root package taken from the
// cutoff's store, or false.
func (t *checkTree) replay(a sketchfront.Analysis) ([]byte, bool) {
	// A package with a diagnostic needs no guard here: an entry is kept
	// only from a clean check (keepCut), and its key holds the package's
	// source and each local import's interface, of which the check is a
	// function (CS5a; TestIncrementalCheckDecides: a package whose source
	// and imports' interfaces are unchanged checks to the same view, its
	// diagnostics included). So a check with a diagnostic never has a
	// kept entry's key.
	if t == nil {
		return nil, false
	}
	key, impls, ok := t.cutKey(a)
	if !ok {
		return nil, false
	}
	text, ok := t.store.Get(key)
	var c treeCut
	if !ok || json.Unmarshal([]byte(text), &c) != nil {
		return nil, false
	}
	if c.Obligations {
		if len(c.Impls) != len(impls) {
			return nil, false
		}
		for imp, want := range c.Impls {
			if impls[imp] != want {
				return nil, false
			}
		}
	}
	if !t.fresh(c.Inputs) {
		return nil, false
	}
	root, srcs, ok := programSources(a)
	if !ok {
		return nil, false
	}
	return renderTree(c.Entry, root, srcs)
}

// storePuts is the store's Puts so far (0 with no tree).
func (t *checkTree) storePuts() int {
	if t == nil {
		return 0
	}
	return t.store.Puts()
}

// fresh is Inputs.Fresh, once for each set of inputs in a run.
func (t *checkTree) fresh(in sketchfront.Inputs) bool {
	data, err := json.Marshal(in)
	if err != nil {
		return false
	}
	k := string(data)
	if v, ok := t.freshness[k]; ok {
		return v
	}
	if t.freshness == nil {
		t.freshness = map[string]bool{}
	}
	v := in.Fresh()
	t.freshness[k] = v
	return v
}

// keepCut keeps the evaluation of a's root package, which printed out,
// for the cutoff; puts is the store's Puts before it was evaluated.
func (t *checkTree) keepCut(a sketchfront.Analysis, sources map[string]bool, out []byte, exit error, puts int) {
	if t == nil || t.rec == nil || exit != nil || t.store.Puts() != puts || len(a.Diagnostics) > 0 {
		return
	}
	key, impls, ok := t.cutKey(a)
	if !ok {
		return
	}
	root, srcs, ok := programSources(a)
	if !ok {
		return
	}
	e, why := treeEntryOf(a, out, root, srcs)
	if why != "" {
		return
	}
	var env report.CheckEnvelope
	if json.Unmarshal(out, &env) != nil || env.Evaluation == nil {
		return
	}
	inputs, ok := t.rec.Inputs()
	if !ok {
		return
	}
	// The load's sources are in the fingerprints, and its packages'
	// folder listings in their file lists: a sibling's edit or new file
	// does not touch this answer.
	for name := range inputs.Files {
		if sources[name] {
			delete(inputs.Files, name)
		}
	}
	for dir := range inputs.Dirs {
		if sources[strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator)] {
			delete(inputs.Dirs, dir)
		}
	}
	c := treeCut{Entry: e, Inputs: inputs, Impls: impls, Obligations: len(env.Evaluation.Obligations) > 0}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	t.store.Put(key, string(data))
	t.cutPuts++
	if treeCutKept != nil {
		treeCutKept(a.Root().Path, c.Obligations)
	}
}

// treeCutKept, when not nil, hears each package whose evaluation the
// cutoff kept, and whether it holds an obligation.
var treeCutKept func(path string, obligations bool)

// loadSources are the source files of every package of loads, by
// absolute name, and each package folder with a trailing separator.
func loadSources(loads []loaded) map[string]bool {
	out := map[string]bool{}
	for _, l := range loads {
		_, srcs, ok := programSources(l.analysis)
		if !ok {
			continue
		}
		for name := range srcs {
			out[name] = true
		}
		for _, p := range l.analysis.Packages() {
			out[strings.TrimSuffix(p.Dir, string(filepath.Separator))+string(filepath.Separator)] = true
		}
	}
	return out
}

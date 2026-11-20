package main

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
)

// The early cutoff of `ergo check DIR/...` (MONOREPO WS0b; docs/spec/
// build.md, Warm answer by tokens 6). When the warm answer by tokens
// misses, the shared load checks every package again, but a package's
// evaluation is taken from the store when nothing it can observe
// changed:
//
//   - Its own source fingerprint (token text and `// ergo:` markers, the
//     path it is checked as and the environment; check.FingerprintOf),
//     and its folder: a comment or layout edit of its own changes none.
//   - The interface fingerprint of every package of the program: the
//     static half of its answer reads no more of an import (CS5a), and
//     what evaluation reads of a package it never entered (a constant
//     check folded, a type, a method set) is in its interface.
//   - When its answer holds an obligation, the source fingerprint of each
//     package its verdicts read (GB-WS0C; eval/reads.go): a verdict is
//     valid for what it read, and a cached verdict carries its reads
//     from the law store, so the read set is the union over every
//     verdict, cached or evaluated alike. A body edit re-runs the
//     packages whose verdicts entered the edited one; a package whose
//     laws never reach it is taken as it was. A verdict whose store did
//     not keep its reads (a test binary's) keys the answer by each local
//     import's implementation fingerprint instead (WS0b's rule), and the
//     run counts it (cutFallbacks).
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
	// Reads are the source fingerprints of the packages the verdicts read,
	// by path ("" for one with none, a std package), checked when
	// Obligations; Impls are the implementation fingerprints of the local
	// imports, checked instead when a verdict's reads are not known.
	Reads       map[string]string `json:"reads"`
	Impls       map[string]string `json:"impls,omitempty"`
	Obligations bool              `json:"obligations"`
}

// cutState is what the cutoff checks of one analysis: its key, the
// source and implementation fingerprints of its packages by path, and
// its root's local imports.
type cutState struct {
	key     string
	sources map[string]string
	impls   map[string]string
	imports []string
}

// readsOf are the source fingerprints of reads now.
func (s cutState) readsOf(reads []string) map[string]string {
	out := make(map[string]string, len(reads))
	for _, p := range reads {
		out[p] = s.sources[p]
	}
	return out
}

// importImpls are the implementation fingerprints of the root's imports.
func (s cutState) importImpls() map[string]string {
	out := make(map[string]string, len(s.imports))
	for _, p := range s.imports {
		out[p] = s.impls[p]
	}
	return out
}

// holds says c's evaluation is still the answer under s.
func (s cutState) holds(c treeCut) bool {
	if !c.Obligations {
		return true
	}
	if c.Reads != nil {
		for p, want := range c.Reads {
			if s.sources[p] != want {
				return false
			}
		}
		return true
	}
	now := s.importImpls()
	if len(c.Impls) != len(now) {
		return false
	}
	for p, want := range c.Impls {
		if now[p] != want {
			return false
		}
	}
	return true
}

// treeRerun, when not nil, hears each package the cutoff evaluated (a
// test's account of the work it saved).
var treeRerun func(path string)

// cutStateOf is the cutoff's state of a's root package: its key holds the
// root's source fingerprint and every package's interface; ok is false
// when the root has no source fingerprint, a package has no interface,
// or a package is of a required module.
func (t *checkTree) cutStateOf(a sketchfront.Analysis) (cutState, bool) {
	for _, p := range a.Packages() {
		if p.Module != "" {
			return cutState{}, false
		}
	}
	fps := a.Fingerprints()
	if len(fps) == 0 {
		return cutState{}, false
	}
	root := fps[len(fps)-1]
	if root.Source == "" {
		return cutState{}, false
	}
	s := cutState{sources: map[string]string{}, impls: map[string]string{}, imports: root.Imports}
	parts := []string{treeSchema, "cut", t.base, root.Path, a.Root().Dir, root.Source}
	for _, fp := range fps[:len(fps)-1] {
		if fp.Interface == "" || fp.Implementation == "" {
			return cutState{}, false
		}
		parts = append(parts, fp.Path, fp.Interface)
		s.sources[fp.Path], s.impls[fp.Path] = fp.Source, fp.Implementation
	}
	s.sources[root.Path] = root.Source
	s.key = lawcache.Key(parts...)
	return s, true
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
	if t == nil || len(a.Diagnostics) > 0 {
		return nil, false
	}
	s, ok := t.cutStateOf(a)
	if !ok {
		return nil, false
	}
	text, ok := t.store.Get(s.key)
	var c treeCut
	if !ok || json.Unmarshal([]byte(text), &c) != nil {
		return nil, false
	}
	if !s.holds(c) {
		return nil, false
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
// for the cutoff; puts is the store's Puts before it was evaluated, and
// ev the evaluation it printed (nil when none ran).
func (t *checkTree) keepCut(a sketchfront.Analysis, sources map[string]bool, out []byte, exit error, puts int, ev *report.Evaluation) {
	if t == nil || t.rec == nil || exit != nil || t.store.Puts() != puts || len(a.Diagnostics) > 0 {
		return
	}
	s, ok := t.cutStateOf(a)
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
	c := treeCut{Entry: e, Inputs: inputs, Obligations: len(env.Evaluation.Obligations) > 0}
	if c.Obligations {
		switch {
		case ev == nil:
			return
		case ev.ReadsUnknown > 0:
			c.Impls = s.importImpls()
			t.cutFallbacks += ev.ReadsUnknown
		default:
			c.Reads = s.readsOf(ev.Reads)
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	t.store.Put(s.key, string(data))
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

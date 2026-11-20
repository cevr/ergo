package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// The warm answer of `ergo check DIR/...` by tokens (MONOREPO WS0;
// findings §5.3, §7.1). A request whose every package checked and passed
// is kept in the law-verdict store (laws/v1), so the next identical
// request in which no package's check text changed prints the same
// envelopes with no parse, no check and no evaluation.
//
//   - Each package is keyed by its folder (a replay command names it),
//     its files' check text (syntax.CheckText: tokens and comments, no
//     layout) and the keys of the local packages it imports, composed in
//     dependency order (treeKeys): an edit anywhere a package reaches
//     changes its key. The key names no request, so another request that
//     reaches the package (a workspace root's, WS2) finds it.
//   - The request's manifest holds every other input the shared load read
//     (ergo.mod, go.mod, go.sum, the law ledger, assets, folder listings),
//     compared by bytes, and each package's with work: the with budget is
//     the one part of a check that reads a file's size
//     (check.WithLimit), so a hit needs the work to fit the size now.
//   - An answer is printed again with what a layout edit moves: the
//     revision (report.Revision of the program's text) and each
//     obligation's site, kept as the ordinal of the token it starts at
//     (syntax.TokenSites). An answer this cannot print exactly is not
//     kept: one with a diagnostic (a span), a reason that names a site
//     (treeSiteText), advice (it may quote the source), a site at no one
//     token, or one whose printing from its own files differs. A package
//     whose obligation fails does not pass, so its request is not kept.
//   - It is kept only from a run that stored no verdict: a law run whose
//     verdict this run stored prints "cached" in the next run, so this
//     run's envelope is not what the next one prints (lawcache Puts).
//
// The hit is all or nothing per request; each package's answer is kept
// under its own key so a partial reuse (WS0b) changes the lookup, not the
// store. ERGO_BUILD=full or ERGO_LAWCACHE=off takes the normal path.
//
// Prior art: go test's cache replays a package's result by its inputs;
// Gleam and ReScript reuse a module whose source digest and dependency
// digests are unchanged; Zig's manifests list a request's inputs. Bazel
// and Turborepo key by bytes, so a comment edit misses there.

// treeSchema versions the manifest, the entries and their keys.
const treeSchema = "check tree/1"

// treeManifest is a kept request.
type treeManifest struct {
	Schema string `json:"schema"`
	// Root is the module's folder, which every text's Rel is relative to.
	Root string `json:"root"`
	// Inputs are what the load read but the packages' source files.
	Inputs sketchfront.Inputs `json:"inputs"`
	// Packages are every local package the checks reached, each after
	// its imports.
	Packages []treePackage `json:"packages"`
	// Selected are the import paths the request printed, in order.
	Selected []string `json:"selected"`
}

// treePackage is one local package of a kept request.
type treePackage struct {
	Path string `json:"path"`
	// Dir is the package's folder, absolute: an obligation's replay
	// command names it.
	Dir      string   `json:"dir"`
	Files    []string `json:"files"`
	Imports  []string `json:"imports"`
	WithWork int      `json:"withWork"`
}

// treeEntry is one printed package's kept answer: the envelope it
// printed, the program's text files (Rel) its revision hashes, and the
// sites a layout edit moves.
type treeEntry struct {
	Out   []byte     `json:"out"`
	Texts []string   `json:"texts"`
	Sites []treeSite `json:"sites"`
}

// treeSite is an obligation's site (or its declaration's) as the token
// it starts at, in a file named by its path from the module's root.
type treeSite struct {
	Obligation int    `json:"obligation"`
	Decl       bool   `json:"decl,omitempty"`
	File       string `json:"file"`
	Token      int    `json:"token"`
}

// checkTree is one `ergo check DIR/...` request's warm answer by tokens.
type checkTree struct {
	store  *lawcache.Store
	base   string // the request's options, environment and compiler: a part of every key
	folder string // the folder the request walks: a part of the manifest's key
	rec    *sketchfront.Recorder
	puts   int // the store's Puts when the normal path started
	// kept are the selected packages' answers so far; complete is false
	// once one cannot be kept.
	kept     []treeKept
	complete bool
}

type treeKept struct {
	a   sketchfront.Analysis
	out []byte
}

// startTree is the request's warm answer: nil when it does not apply.
func startTree(opts fileOptions) *checkTree {
	if !sketchfront.WarmEnabled() || opts.work || opts.report != "" || opts.goText != "" {
		return nil
	}
	source, err := receipt.LocateSource(filepath.Dir(filepath.Clean(opts.file)))
	if err != nil {
		return nil
	}
	compiler, err := build.IdentityOf(producer.Current())
	if err != nil {
		return nil
	}
	store := lawStoreOf(compiler)
	// The packages' keys name no folder, so a package's answer is found
	// again from another request that reaches it (a module of a
	// workspace, WS2); the manifest is the request's.
	base := sketchfront.WarmKey(treeSchema, warmOptions(opts))
	if store == nil || base == "" {
		return nil
	}
	return &checkTree{store: store, base: base, folder: source.Admitted(), complete: true}
}

// manifestKey is the key of the request's manifest.
func (t *checkTree) manifestKey() string {
	return lawcache.Key(treeSchema, "manifest", t.base, t.folder)
}

// lookup is the printed answer of every selected package, in order, when
// the request is kept and nothing it read changed but layout.
func (t *checkTree) lookup() ([][]byte, bool) {
	if t == nil {
		return nil, false
	}
	text, ok := t.store.Get(t.manifestKey())
	var m treeManifest
	if !ok || json.Unmarshal([]byte(text), &m) != nil || m.Schema != treeSchema || !m.Inputs.Fresh() {
		return nil, false
	}
	srcs := map[string]string{}
	for _, p := range m.Packages {
		size := 0
		for _, name := range p.Files {
			data, err := os.ReadFile(name)
			if err != nil {
				return nil, false
			}
			srcs[name] = string(data)
			size += len(data)
		}
		if p.WithWork > check.WithLimit(size) {
			return nil, false
		}
	}
	keys, ok := treeKeys(t.base, m.Packages, srcs)
	if !ok {
		return nil, false
	}
	outs := make([][]byte, 0, len(m.Selected))
	for _, path := range m.Selected {
		text, ok := t.store.Get(keys[path])
		var e treeEntry
		if !ok || json.Unmarshal([]byte(text), &e) != nil {
			return nil, false
		}
		out, ok := renderTree(e, m.Root, srcs)
		if !ok {
			return nil, false
		}
		outs = append(outs, out)
	}
	return outs, true
}

// treeKeys is each package's key: its import path, whether the load
// checked it as main (no package of the load imports it) or as its path,
// its files' names and check text, and the keys of its imports. ok is
// false when an import is not among packages, which each come after
// their imports.
func treeKeys(base string, packages []treePackage, srcs map[string]string) (map[string]string, bool) {
	imported := map[string]bool{}
	for _, p := range packages {
		for _, q := range p.Imports {
			imported[q] = true
		}
	}
	keys := map[string]string{}
	for _, p := range packages {
		as := "main"
		if imported[p.Path] {
			as = "imported"
		}
		parts := []string{treeSchema, "package", base, p.Path, p.Dir, as}
		for _, name := range p.Files {
			parts = append(parts, filepath.Base(name), syntax.CheckText(srcs[name]))
		}
		imports := slices.Clone(p.Imports)
		slices.Sort(imports)
		for _, q := range imports {
			k, ok := keys[q]
			if !ok {
				return nil, false
			}
			parts = append(parts, q, k)
		}
		keys[p.Path] = lawcache.Key(parts...)
	}
	return keys, true
}

// renderTree prints a kept answer against the files as they are now:
// srcs holds the packages' files by absolute path; a text not among them
// is read from root.
func renderTree(e treeEntry, root string, srcs map[string]string) ([]byte, bool) {
	var env report.CheckEnvelope
	if json.Unmarshal(e.Out, &env) != nil {
		return nil, false
	}
	texts := map[string]string{}
	for _, rel := range e.Texts {
		name := filepath.Join(root, filepath.FromSlash(rel))
		src, ok := srcs[name]
		if !ok {
			data, err := os.ReadFile(name)
			if err != nil {
				return nil, false
			}
			src = string(data)
		}
		texts[rel] = src
	}
	revision := report.Revision(sketchfront.ProgramText(texts))
	env.Revision = revision
	if env.Snapshot.Revision != "" {
		env.Snapshot.Revision = revision
	}
	if env.Evaluation == nil {
		return nil, false
	}
	env.Evaluation.Revision = revision
	sites := map[string][][2]int{}
	for _, s := range e.Sites {
		name := filepath.Join(root, filepath.FromSlash(s.File))
		src, ok := srcs[name]
		if !ok || s.Obligation >= len(env.Evaluation.Obligations) {
			return nil, false
		}
		at, ok := sites[name]
		if !ok {
			at = syntax.TokenSites(src)
			sites[name] = at
		}
		if s.Token >= len(at) {
			return nil, false
		}
		o := &env.Evaluation.Obligations[s.Obligation]
		site := &o.Site
		if s.Decl {
			if o.Declaration == nil {
				return nil, false
			}
			site = o.Declaration
		}
		site.Line, site.Column = at[s.Token][0], at[s.Token][1]
	}
	var buf bytes.Buffer
	if json.NewEncoder(&buf).Encode(env) != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// fsys is the file system the normal path reads through.
func (t *checkTree) fsys() sketchfront.FS {
	if t == nil {
		return nil
	}
	if t.rec == nil {
		t.rec = sketchfront.NewRecorder()
		t.puts = t.store.Puts()
	}
	return t.rec
}

// add keeps one selected package's printed answer; exit is its check's
// exit status.
func (t *checkTree) add(a sketchfront.Analysis, out []byte, exit error) {
	if t == nil {
		return
	}
	if exit != nil {
		t.complete = false
		return
	}
	t.kept = append(t.kept, treeKept{a: a, out: slices.Clone(out)})
}

// declarationFiles are, by an obligation site's "FILE:LINE:COLUMN", the
// files (absolute) that declare a test or a law the root package runs at
// that site: the evaluator names a site by the declaring file's name and
// the declaration's position (eval's site).
func declarationFiles(a sketchfront.Analysis, root string) map[string][]string {
	abs := map[*syntax.File]string{}
	for _, p := range a.Packages() {
		for _, f := range p.Files {
			if f.Syntax != nil {
				abs[f.Syntax] = filepath.Join(root, filepath.FromSlash(f.Rel))
			}
		}
	}
	out := map[string][]string{}
	add := func(pkg *check.Package, decl syntax.Decl) {
		if pkg == nil || decl == nil {
			return
		}
		for _, f := range pkg.Files() {
			found := false
			for _, d := range f.Decls {
				syntax.Inspect(d, func(n syntax.Node) bool {
					found = found || n == decl
					return !found
				})
				if found {
					break
				}
			}
			if name, ok := abs[f]; found && ok {
				line, col := f.Position(decl.Pos())
				key := fmt.Sprintf("%s:%d:%d", f.Name, line, col)
				if !slices.Contains(out[key], name) {
					out[key] = append(out[key], name)
				}
				return
			}
		}
	}
	pkg := a.Root().Package
	if pkg == nil {
		return out
	}
	for _, t := range pkg.Tests {
		add(pkg, t.Decl)
	}
	for _, run := range pkg.LawRuns {
		add(run.Law.Pkg, run.Law.Decl)
	}
	return out
}

// treeSiteText finds a site in an obligation's reason, as the evaluator
// writes one ("at FILE.ergo:LINE", eval's siteText): a layout edit moves
// it, and a kept answer would print where it was. The other texts a reason
// quotes from the source are a failed assert's and a refuted licence's,
// whose package does not pass, so it is not kept.
var treeSiteText = regexp.MustCompile(`\.ergo:[0-9]|(^|\s):[0-9]`)

// treeUnkept, when not nil, hears why a request was not kept (a test's
// account of what the store leaves out).
var treeUnkept func(why string)

// record keeps the request when every selected package can be kept.
func (t *checkTree) record() {
	if t == nil || t.rec == nil {
		return
	}
	if why := t.keep(); why != "" && treeUnkept != nil {
		treeUnkept(why)
	}
}

// keep keeps the request, or says why it cannot.
func (t *checkTree) keep() (why string) {
	switch {
	case !t.complete:
		return "a package did not pass"
	case len(t.kept) == 0:
		return "no package"
	case t.store.Puts() != t.puts:
		return "the run stored verdicts"
	}
	inputs, ok := t.rec.Inputs()
	if !ok {
		return "a read failed"
	}
	root := t.kept[0].a.ModuleRoot()
	if root == "" {
		return "no module"
	}
	m := treeManifest{Schema: treeSchema, Root: root}
	seen := map[string]bool{}
	srcs := map[string]string{}
	for _, k := range t.kept {
		for _, p := range k.a.Packages() {
			if p.Module != "" {
				return "a package of a required module"
			}
			if p.Package == nil {
				return "a package not checked"
			}
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			work, ok := p.Package.WithWork()
			if !ok {
				return "a package this process did not check"
			}
			tp := treePackage{Path: p.Path, Dir: p.Dir, Imports: p.Imports, WithWork: work}
			for _, f := range p.Files {
				if f.Syntax == nil {
					return "a file that did not parse"
				}
				name := filepath.Join(root, filepath.FromSlash(f.Rel))
				srcs[name] = f.Syntax.Src
				tp.Files = append(tp.Files, name)
			}
			slices.Sort(tp.Files)
			m.Packages = append(m.Packages, tp)
		}
		m.Selected = append(m.Selected, k.a.Root().Path)
	}
	// The packages' files are keyed by their check text, not their bytes.
	for name := range srcs {
		delete(inputs.Files, name)
	}
	m.Inputs = inputs
	keys, ok := treeKeys(t.base, m.Packages, srcs)
	if !ok {
		return "an import outside the load"
	}
	entries := make([]string, len(t.kept))
	for i, k := range t.kept {
		e, why := treeEntryOf(k.a, k.out, root, srcs)
		if why != "" {
			return m.Selected[i] + ": " + why
		}
		data, err := json.Marshal(e)
		if err != nil {
			return err.Error()
		}
		entries[i] = string(data)
	}
	for i, path := range m.Selected {
		t.store.Put(keys[path], entries[i])
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err.Error()
	}
	t.store.Put(t.manifestKey(), string(data))
	return ""
}

// treeEntryOf is the kept answer of a package whose check printed out, or
// why it cannot be printed again exactly (see the top of this file).
func treeEntryOf(a sketchfront.Analysis, out []byte, root string, srcs map[string]string) (treeEntry, string) {
	var env report.CheckEnvelope
	switch {
	case json.Unmarshal(out, &env) != nil || !env.Checked || env.Evaluation == nil:
		return treeEntry{}, "no evaluated envelope"
	case len(env.Diagnostics) > 0:
		return treeEntry{}, "a diagnostic"
	}
	e := treeEntry{Out: out}
	for rel, text := range a.Files() {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(data) != text {
			return treeEntry{}, "the text of " + rel + " is not the file's"
		}
		e.Texts = append(e.Texts, rel)
	}
	slices.Sort(e.Texts)
	// The files a site may name: the file of each test and law the
	// package runs whose declaration is at the site. A site names a file
	// only by its name in its package's folder, which another package of
	// the program may share.
	byName := declarationFiles(a, root)
	sites := map[string][][2]int{}
	at := func(site report.Site) (string, int, bool) {
		found, file, token := 0, "", 0
		for _, name := range byName[fmt.Sprintf("%s:%d:%d", site.File, site.Line, site.Column)] {
			ts, ok := sites[name]
			if !ok {
				ts = syntax.TokenSites(srcs[name])
				sites[name] = ts
			}
			for k, s := range ts {
				if s[0] == site.Line && s[1] == site.Column {
					found, file, token = found+1, name, k
				}
			}
		}
		rel, err := filepath.Rel(root, file)
		return filepath.ToSlash(rel), token, found == 1 && err == nil
	}
	for i, o := range env.Evaluation.Obligations {
		switch {
		case treeSiteText.MatchString(o.Reason):
			return treeEntry{}, "a reason that names a site"
		case o.Advice != "":
			return treeEntry{}, "advice"
		}
		if o.Site != (report.Site{}) {
			file, token, ok := at(o.Site)
			if !ok {
				return treeEntry{}, fmt.Sprintf("a site at no one token (%s:%d:%d)", o.Site.File, o.Site.Line, o.Site.Column)
			}
			e.Sites = append(e.Sites, treeSite{Obligation: i, File: file, Token: token})
		}
		if o.Declaration != nil {
			file, token, ok := at(*o.Declaration)
			if !ok {
				return treeEntry{}, "a declaration site at no one token"
			}
			e.Sites = append(e.Sites, treeSite{Obligation: i, Decl: true, File: file, Token: token})
		}
	}
	// It prints again, from the files it was checked from, as it printed.
	again, ok := renderTree(e, root, srcs)
	if !ok || !bytes.Equal(again, out) {
		return treeEntry{}, "its printing again differs:\n" + string(again) + "\n" + string(out)
	}
	return e, ""
}

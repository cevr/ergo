package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

// `ergo test [PKG|DIR/...] --affected [BASE] [--list] [--why] [--json]`
// (CI-SELECT §7.1, CS-AFFECTED; docs/testing.md, Affected tests). An
// obligation, a test block or a law run, is affected when its reach
// digest (lower.TestDigest; lower.RunTarget.Digest) in the working tree
// differs from its digest at BASE, or BASE has no obligation of its
// name. BASE's sources are read from Git objects (sketchfront.GitTree),
// through the loader that reads the working tree, so both sides admit
// their sources by one rule. A comment, a layout edit or a moved
// declaration changes no digest. It fails closed: a changed ergo.mod,
// go.mod, go.sum, law ledger or case corpus affects every obligation of
// the module, a changed file other than an .ergo source in a package's
// folder (a binding, an asset) every obligation of the package, and a
// package that does not check at BASE every obligation of it. The
// compiler is not compared: --affected assumes one compiler on both
// sides.
//
// --list prints the affected set and runs nothing; --why prints a reason
// for each obligation of the selected packages; --json prints the set as
// JSON and runs nothing. Without them, the packages with an affected
// obligation run their tests as `ergo test` runs them, the verdict store
// replaying the law runs it holds, and the others are left out.

// affectedState is an obligation's state against BASE.
type affectedState string

const (
	stateAffected  affectedState = "affected"
	stateNew       affectedState = "new"
	stateUnchanged affectedState = "unchanged"
	stateGone      affectedState = "gone"
)

// affectedObligation is one obligation of a selected package and its state.
type affectedObligation struct {
	Package string        `json:"package"`
	Label   string        `json:"label"`
	State   affectedState `json:"state"`
	Reason  string        `json:"reason"`
}

// affectedReport is the comparison of the selected packages with BASE.
type affectedReport struct {
	Base        string               `json:"base"`
	Obligations []affectedObligation `json:"obligations"`
	// dirs are the selected packages' folders by import path, in run
	// order; runs are those with an affected or new obligation.
	dirs  []sketchfront.PackageTarget
	runs  map[string]bool
	heads map[string]loaded
}

// Affected is the obligations that run: affected or new.
func (r affectedReport) affected() []affectedObligation {
	var out []affectedObligation
	for _, o := range r.Obligations {
		if o.State == stateAffected || o.State == stateNew {
			out = append(out, o)
		}
	}
	return out
}

// obligation is a package's obligation: its label, digest and reach.
type obligation struct {
	label  string
	digest string
	reach  func() []lower.ReachedDecl
}

// obligationsOf are the test blocks and the law runs of pkg, by label.
func obligationsOf(pkg *check.Package) map[string]obligation {
	out := map[string]obligation{}
	for _, t := range pkg.Tests {
		out[lower.TestLabel(t)] = obligation{label: lower.TestLabel(t), digest: lower.TestDigest(pkg, t), reach: func() []lower.ReachedDecl { return lower.TestReach(pkg, t) }}
	}
	for _, t := range lower.RunTargets(pkg) {
		out[t.Label] = obligation{label: t.Label, digest: t.Digest(pkg), reach: func() []lower.ReachedDecl { return t.Reach(pkg) }}
	}
	return out
}

// moduleFiles are the files beside ergo.mod whose change affects every
// obligation of the module.
var moduleFiles = []string{sketchfront.ModFile, sketchfront.GoModFile, sketchfront.GoSumFile, lower.LedgerFile, sketchfront.CorpusFile}

// affectedOf compares the packages opts selects in the working tree with
// those at base ("" for the merge base with the default branch).
func affectedOf(opts fileOptions, base string, platform gobind.Platform) (affectedReport, error) {
	dir := opts.file
	if recursivePattern(dir) {
		dir = filepath.Dir(filepath.Clean(dir))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return affectedReport{}, err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return affectedReport{}, err
	}
	if base == "" {
		if base, err = sketchfront.DefaultBase(abs); err != nil {
			return affectedReport{}, err
		}
	}
	tree, err := sketchfront.OpenGitTree(abs, base)
	if err != nil {
		return affectedReport{}, err
	}
	defer tree.Close()
	var heads []sketchfront.PackageTarget
	if recursivePattern(opts.file) {
		if heads, err = sketchfront.DiscoverPackagesThrough(nil, abs); err != nil {
			return affectedReport{}, err
		}
	} else {
		heads = []sketchfront.PackageTarget{{Dir: abs}}
	}
	rep := affectedReport{Base: tree.Commit(), dirs: heads, runs: map[string]bool{}, heads: map[string]loaded{}}
	// The module's own files: a change of one affects every obligation.
	everything := ""
	root, hasRoot := moduleRoot(abs)
	if !hasRoot {
		root = abs
	}
	if hasRoot {
		for _, name := range moduleFiles {
			if fileChanged(tree, filepath.Join(root, name)) {
				everything = "every obligation: " + name + " changed"
				break
			}
		}
	}
	for i, target := range heads {
		l, err := loadThrough(nil, target.Dir, opts.target, platform)
		if err != nil {
			return affectedReport{}, err
		}
		if !l.result.Checked {
			return affectedReport{}, refuseUnchecked(l, opts)
		}
		pkg := l.analysis.Root().Package
		if target.Path == "" {
			heads[i].Path = l.analysis.Root().Path
			target = heads[i]
		}
		rep.heads[target.Path] = l
		head := obligationsOf(pkg)
		why := everything
		if why == "" {
			if name, changed := folderFilesChanged(tree, target.Dir); changed {
				why = "every obligation: " + name + " changed"
			}
		}
		var at map[string]obligation
		if why == "" && sourcesSame(tree, l.analysis) {
			// Every source the package reads is BASE's, byte for byte:
			// BASE checks to the same package, so nothing changed and
			// BASE need not be checked.
			at = head
		} else if why == "" {
			b, err := sketchfront.CheckDirFS(tree, target.Dir, platform)
			switch {
			case err != nil:
				why = "every obligation: the package is not at BASE"
			case b.Root().Package == nil || len(b.Diagnostics) > 0:
				why = "every obligation: the package does not check at BASE"
			default:
				at = obligationsOf(b.Root().Package)
			}
		}
		labels := make([]string, 0, len(head))
		for label := range head {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			o := affectedObligation{Package: target.Path, Label: label}
			then, had := at[label]
			switch {
			case why != "":
				o.State, o.Reason = stateAffected, why
			case !had:
				o.State, o.Reason = stateNew, "new"
			case then.digest != head[label].digest:
				o.State, o.Reason = stateAffected, reachChange(then.reach(), head[label].reach(), declNamer{pkgPath: target.Path, root: root, dirs: packageDirs(l)})
			default:
				o.State, o.Reason = stateUnchanged, "none: nothing it reaches changed"
			}
			if o.State != stateUnchanged {
				rep.runs[target.Path] = true
			}
			rep.Obligations = append(rep.Obligations, o)
		}
		gone := []string{}
		for label := range at {
			if _, ok := head[label]; !ok {
				gone = append(gone, label)
			}
		}
		sort.Strings(gone)
		for _, label := range gone {
			rep.Obligations = append(rep.Obligations, affectedObligation{Package: target.Path, Label: label, State: stateGone, Reason: "gone: not in the working tree"})
		}
	}
	stage.Add(stage.ObligationsAffected, int64(len(rep.affected())))
	return rep, nil
}

// reachChange is the reason an obligation's digest changed: the first
// declaration it reaches whose tokens changed or that it newly reaches,
// with its place in the working tree, else one it no longer reaches.
func reachChange(then, now []lower.ReachedDecl, n declNamer) string {
	before := map[string]string{}
	for _, d := range then {
		before[d.Key] = d.Text
	}
	for _, d := range now {
		text, had := before[d.Key]
		switch {
		case !had:
			return "reaches " + n.name(d) + ", new"
		case text != d.Text:
			return "reaches " + n.name(d) + ", changed"
		}
	}
	after := map[string]bool{}
	for _, d := range now {
		after[d.Key] = true
	}
	for _, d := range then {
		if !after[d.Key] {
			return "no longer reaches " + shortKey(d.Key, n.pkgPath)
		}
	}
	return "its providers, its instance or the generators of its types changed"
}

// declNamer names reached declarations for an obligation of the package
// pkgPath: dirs are the folders of the packages its load read, by import
// path, and root the module root.
type declNamer struct {
	pkgPath, root string
	dirs          map[string]string
}

// packageDirs are the folders of the packages l read, by import path.
func packageDirs(l loaded) map[string]string {
	dirs := map[string]string{}
	for _, p := range l.analysis.Packages() {
		dirs[p.Path] = p.Dir
	}
	return dirs
}

// name is a reached declaration as a reason names it: `func tally
// (lib/tally.ergo:14)`, its file relative to the module root when it is
// below it; a std declaration has no place.
func (n declNamer) name(d lower.ReachedDecl) string {
	name := shortKey(d.Key, n.pkgPath)
	file, line := d.Position()
	if file == "" || d.Pkg == nil || n.dirs[d.Pkg.Path] == "" {
		return name
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(n.dirs[d.Pkg.Path], file)
	}
	if rel, err := filepath.Rel(n.root, file); err == nil && !strings.HasPrefix(rel, "..") {
		file = rel
	}
	return fmt.Sprintf("%s (%s:%d)", name, filepath.ToSlash(file), line)
}

// shortKey is a reach key with the obligation's own package path cut,
// "func tally", and another package's cut to its last element, "func
// lib.tally".
func shortKey(key, pkgPath string) string {
	kind, name, ok := strings.Cut(key, " ")
	if !ok {
		return key
	}
	if own, ok := strings.CutPrefix(name, pkgPath+"."); ok {
		return kind + " " + own
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return kind + " " + name
}

// moduleRoot is the folder of the nearest ergo.mod at or above dir.
func moduleRoot(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Stat(filepath.Join(d, sketchfront.ModFile)); err == nil && info.Mode().IsRegular() {
			return d, true
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// sourcesSame reports whether every package of the module that the
// analysis checked, the root and each package it imports at any depth,
// has the same .ergo files at the commit, byte for byte. The imports
// decide what BASE checks too, and they are in those files, so equal
// files are an equal program; a difference is only a reason to compare
// digests.
func sourcesSame(tree *sketchfront.GitTree, a sketchfront.Analysis) bool {
	for _, p := range a.Packages() {
		if p.Module != "" {
			continue // a required module: ergo.mod pins it
		}
		if !ergoFilesSame(tree, p.Dir) {
			return false
		}
	}
	return true
}

// ergoFilesSame reports whether the folder dir holds the same .ergo files
// in the working tree and at the commit.
func ergoFilesSame(tree *sketchfront.GitTree, dir string) bool {
	names := map[string]int{}
	for _, list := range [][]os.DirEntry{readDirQuiet(os.ReadDir, dir), readDirQuiet(tree.ReadDir, dir)} {
		for _, e := range list {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".ergo" {
				names[e.Name()]++
			}
		}
	}
	for n, seen := range names {
		if seen != 2 || fileChanged(tree, filepath.Join(dir, n)) {
			return false
		}
	}
	return true
}

// fileChanged reports whether the file name differs between the working
// tree and the commit: its bytes, or one side has it and the other not.
func fileChanged(tree *sketchfront.GitTree, name string) bool {
	now, errNow := os.ReadFile(name)
	then, errThen := tree.ReadFile(name)
	if errNow != nil || errThen != nil {
		return (errNow == nil) != (errThen == nil)
	}
	return !bytes.Equal(now, then)
}

// folderFilesChanged reports the first file of the package folder dir,
// other than an .ergo source and a hidden file, that differs between the
// working tree and the commit: a binding or an asset the package reads.
func folderFilesChanged(tree *sketchfront.GitTree, dir string) (string, bool) {
	names := map[string]bool{}
	for _, list := range [][]os.DirEntry{readDirQuiet(os.ReadDir, dir), readDirQuiet(tree.ReadDir, dir)} {
		for _, e := range list {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || filepath.Ext(e.Name()) == ".ergo" {
				continue
			}
			names[e.Name()] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if fileChanged(tree, filepath.Join(dir, n)) {
			return n, true
		}
	}
	return "", false
}

func readDirQuiet(read func(string) ([]os.DirEntry, error), dir string) []os.DirEntry {
	list, _ := read(dir)
	return list
}

// affectedOptions are test's --affected, --list and --why.
type affectedOptions struct {
	on        bool
	base      string
	list, why bool
}

// affectedTestCommand is `ergo test … --affected`; run tests one package
// (testLoadedPackage).
func affectedTestCommand(opts fileOptions, stdout io.Writer, run func(fileOptions, loaded) error) error {
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return err
	}
	rep, err := affectedOf(opts, opts.affected.base, platform)
	if err != nil {
		return err
	}
	if opts.json {
		if rep.Obligations == nil {
			rep.Obligations = []affectedObligation{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if opts.affected.why {
		for _, o := range rep.Obligations {
			fmt.Fprintf(stdout, "%s: %s: %s\n", o.Package, o.Label, o.Reason)
		}
	} else if opts.affected.list {
		for _, o := range rep.affected() {
			fmt.Fprintf(stdout, "%s: %s\n", o.Package, o.Label)
		}
	}
	if opts.affected.list {
		return nil
	}
	fmt.Fprintf(os.Stderr, "affected: %d obligations in %d of %d packages since %.12s\n", len(rep.affected()), len(rep.runs), len(rep.dirs), rep.Base)
	failed := false
	for _, target := range rep.dirs {
		if !rep.runs[target.Path] {
			continue
		}
		one := opts
		one.file = target.Dir
		fmt.Fprintf(stdout, "package %s\n", target.Path)
		stop, fail := packageOutcome(target.Path, run(one, rep.heads[target.Path]))
		if stop != nil {
			return stop
		}
		failed = failed || fail
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

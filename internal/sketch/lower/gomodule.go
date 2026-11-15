package lower

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// GoModule is what the emitted module needs from the go.mod and go.sum
// beside ergo.mod (IG7, owner q37; docs/spec/interop.md, Go modules), and
// from Ergo itself: the requirements and replacements of each source,
// merged by MergeGoModules, and the Go modules a program's import go
// packages come from. Its zero value is the runtime's alone
// (RuntimeGoModule).
type GoModule struct {
	// Require are the require directives, in source order.
	Require []GoRequire
	// Replace are the replace directives, a local directory made
	// absolute, since the emitted module lives elsewhere.
	Replace []GoReplace
	// Sum is go.sum's text, copied as it is.
	Sum string
	// Modules are the module paths a Go import of the program may name
	// outside Go's standard library: each required module, and the module
	// of go.mod itself, which Require and Replace then hold.
	Modules []string
	// Main is the path of the main module a source emits: no source may
	// require or replace it.
	Main string
}

// GoRequire is one require directive and the source that states it.
type GoRequire struct {
	Path, Version string
	// From names the source in a conflict: "go.mod:7", or the runtime.
	From string
	// Line is the directive's go.mod line, 0 for a source that is no file.
	Line int
}

// GoReplace is one replace directive, old [version] => new [version], and
// the source that states it.
type GoReplace struct {
	Old, OldVersion, New, NewVersion string
	From                             string
	Line                             int
	// Holds are the files of a directory the emitted module writes for
	// this replacement, relative to it, its go.mod aside: a replacement of
	// the same module by a directory that holds the same bytes serves it.
	Holds []File
}

// GoConflict is two directives of the sources that no one emitted go.mod
// can hold. Line is the go.mod line to report it at, 0 when neither
// directive comes from go.mod.
type GoConflict struct {
	Message string
	Line    int
}

// runtimeSource names the runtime's directives in a conflict.
const runtimeSource = "the Ergo runtime (" + runtimeDir + "/)"

// RuntimeGoModule is Ergo's own source of the emitted module of a program:
// the runtime's module, required and replaced by the copied tree, and the
// main module's path.
func RuntimeGoModule() GoModule {
	var held []File
	for _, f := range Runtime() {
		rel := strings.TrimPrefix(f.Path, runtimeDir+"/")
		if rel != "go.mod" {
			held = append(held, File{Path: rel, Data: f.Data})
		}
	}
	return GoModule{
		Main:    programModule,
		Require: []GoRequire{{Path: runtimeModule, Version: "v0.0.0", From: runtimeSource}},
		Replace: []GoReplace{{Old: runtimeModule, New: "./" + runtimeDir, From: runtimeSource, Holds: held}},
	}
}

// MergeGoModules merges the directives of the sources into one emitted
// go.mod by one rule for every module path:
//   - a module is required at one version: equal requirements are one, and
//     two versions conflict;
//   - a module has one replacement for each version: equal replacements
//     are one, and for each required module the replacement each source
//     applies to the required version (its own version's, else its
//     version-less one, as go build chooses) is the same in every source;
//   - two replacements that differ conflict, unless one is a directory the
//     emitted module writes (Holds) and the other a directory that holds
//     the same bytes for each of its files: then that directory serves
//     both, and the written one's directive is dropped;
//   - no source requires or replaces a source's main module.
//
// holds reports why the directory dir does not hold files, "" when it
// does. Each conflict names both directives' sources.
func MergeGoModules(holds func(dir string, files []File) string, sources ...GoModule) (GoModule, []GoConflict) {
	var out GoModule
	var conflicts []GoConflict
	report := func(a, b int, format string, args ...any) {
		conflicts = append(conflicts, GoConflict{Message: fmt.Sprintf(format, args...), Line: max(a, b)})
	}
	for _, s := range sources {
		if out.Main == "" {
			out.Main = s.Main
		}
		for _, mod := range s.Modules {
			if !slices.Contains(out.Modules, mod) {
				out.Modules = append(out.Modules, mod)
			}
		}
		if s.Sum != "" {
			out.Sum += s.Sum
		}
	}
	version := map[string]GoRequire{}
	mainRequired := false // the main module's replacement is then reported with it
	for _, s := range sources {
		for _, r := range s.Require {
			prev, ok := version[r.Path]
			switch {
			case r.Path == out.Main:
				mainRequired = true
				report(r.Line, 0, "%s requires %s, the path of the emitted main module", r.From, r.Path)
			case !ok:
				version[r.Path] = r
				out.Require = append(out.Require, r)
			case prev.Version != r.Version:
				report(prev.Line, r.Line, "%s requires %s %s, and %s requires %s %s: the emitted module requires one version", prev.From, r.Path, prev.Version, r.From, r.Path, r.Version)
			}
		}
	}
	type entry struct {
		GoReplace
		source int
		alive  bool
	}
	var reps []*entry
	for i, s := range sources {
		for _, r := range s.Replace {
			if r.Old == out.Main {
				if !mainRequired {
					report(r.Line, 0, "%s replaces %s, the path of the emitted main module", r.From, r.Old)
				}
				continue
			}
			reps = append(reps, &entry{GoReplace: r, source: i, alive: true})
		}
	}
	met := map[[2]*entry]bool{}
	reconcile := func(a, b *entry) {
		if !a.alive || !b.alive || met[[2]*entry{a, b}] || met[[2]*entry{b, a}] {
			return
		}
		met[[2]*entry{a, b}] = true
		if a.New == b.New && a.NewVersion == b.NewVersion {
			b.alive = false
			return
		}
		why := ""
		for _, p := range [][2]*entry{{a, b}, {b, a}} {
			written, other := p[0], p[1]
			if written.Holds == nil || other.NewVersion != "" {
				continue
			}
			if why = holds(other.New, written.Holds); why == "" {
				written.alive = false
				return
			}
		}
		msg := fmt.Sprintf("%s replaces %s with %s, and %s replaces it with %s", a.From, a.Old, replacementTarget(a.GoReplace), b.From, replacementTarget(b.GoReplace))
		if why != "" {
			msg += ": " + why
		}
		report(a.Line, b.Line, "%s", msg)
	}
	// The replacements that apply come first: a written one that an
	// applied one serves is dropped before it can meet another source's
	// replacement that no required version uses.
	for _, r := range out.Require {
		applied := map[int]*entry{}
		for _, e := range reps {
			if !e.alive || e.Old != r.Path || e.OldVersion != r.Version && e.OldVersion != "" {
				continue
			}
			if prev := applied[e.source]; prev == nil || prev.OldVersion == "" {
				applied[e.source] = e
			}
		}
		var each []*entry
		for i := range sources {
			if e := applied[i]; e != nil {
				each = append(each, e)
			}
		}
		for i, a := range each {
			for _, b := range each[i+1:] {
				reconcile(a, b)
			}
		}
	}
	for i, a := range reps {
		for _, b := range reps[i+1:] {
			if a.Old == b.Old && a.OldVersion == b.OldVersion {
				reconcile(a, b)
			}
		}
	}
	for _, e := range reps {
		if e.alive {
			out.Replace = append(out.Replace, e.GoReplace)
		}
	}
	return out, conflicts
}

// replacementTarget is a replacement's new path and version.
func replacementTarget(r GoReplace) string {
	if r.NewVersion != "" {
		return r.New + " " + r.NewVersion
	}
	return r.New
}

// goModFile is the emitted module's go.mod: the merged directives.
func goModFile(m GoModule) []byte { return goModFileAt(programModule, m) }

// goModFileAt is goModFile for the module path module.
func goModFileAt(module string, m GoModule) []byte {
	if len(m.Require) == 0 && len(m.Replace) == 0 {
		m = RuntimeGoModule()
	}
	var b strings.Builder
	b.WriteString("module " + module + "\n\ngo " + goVersion + "\n")
	for _, r := range m.Require {
		b.WriteString("\nrequire " + GoModWord(r.Path) + " " + GoModWord(r.Version) + "\n")
	}
	for _, r := range m.Replace {
		line := GoModWord(r.Old)
		if r.OldVersion != "" {
			line += " " + GoModWord(r.OldVersion)
		}
		line += " => " + GoModWord(r.New)
		if r.NewVersion != "" {
			line += " " + GoModWord(r.NewVersion)
		}
		b.WriteString("\nreplace " + line + "\n")
	}
	return []byte(b.String())
}

// GoModFile is the go.mod of the emitted program module that m gives.
func (m GoModule) GoModFile() []byte { return goModFile(m) }

// GoModWord is w as a go.mod word: quoted unless it is plain.
func GoModWord(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~+/") == "" {
		return w
	}
	return strconv.Quote(w)
}

// covers reports whether a Go import path is a package of one of m's
// modules.
func (m GoModule) covers(path string) bool {
	for _, mod := range m.Modules {
		if path == mod || strings.HasPrefix(path, mod+"/") {
			return true
		}
	}
	return false
}

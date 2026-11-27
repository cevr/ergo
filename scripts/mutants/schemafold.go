package main

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/mutants/schemata"
)

// foldAttempts bounds the folds of one run: each attempt after the first
// leaves out the mutants the last one's compile errors rest on.
const foldAttempts = 4

// foldCompiled folds variants into the tree's copy and compiles each
// package it changes there. A compile error the folded file blames on
// some mutants (a row that does not compile on its own, met before its
// source check) leaves those out, with the error, and folds the rest
// again; an error it blames on none leaves every mutant out. The tree's
// copy then holds the last fold.
func (e *engine) foldCompiled(ctx context.Context, w *worker, base []schemata.File, variants []schemata.Variant, names map[int]string, tree *schemaTree) (schemata.Result, error) {
	originals := map[string][]byte{}
	for _, f := range base {
		originals[f.Path] = f.Data
	}
	active := slices.Clone(variants)
	var written []string
	for attempt := 1; ; attempt++ {
		for _, p := range written {
			if err := writeKeep(filepath.Join(w.root, filepath.FromSlash(p)), originals[p]); err != nil {
				return schemata.Result{}, err
			}
		}
		written = nil
		folded := schemata.Fold(base, active)
		for id, why := range folded.Isolated {
			tree.isolated[names[id]] = why
		}
		for _, f := range folded.Files {
			if err := writeKeep(filepath.Join(w.root, filepath.FromSlash(f.Path)), f.Data); err != nil {
				return schemata.Result{}, err
			}
			written = append(written, f.Path)
		}
		if len(folded.Folded) == 0 {
			return folded, nil
		}
		blamed, detail := e.compileFolded(ctx, w, folded)
		if detail == "" {
			return folded, nil
		}
		if len(blamed) == 0 || attempt == foldAttempts {
			for id := range folded.Folded {
				tree.isolated[names[id]] = "the schemata tree does not compile: " + firstCompileError(detail)
			}
			for _, p := range written {
				if err := writeKeep(filepath.Join(w.root, filepath.FromSlash(p)), originals[p]); err != nil {
					return schemata.Result{}, err
				}
			}
			e.logf("schemata: the folded tree does not compile: every folded row takes the isolated path\n%s", lastLines(detail, 12))
			return schemata.Result{}, ctx.Err()
		}
		active = slices.DeleteFunc(active, func(v schemata.Variant) bool {
			if why, ok := blamed[v.ID]; ok {
				tree.isolated[v.Name] = "does not compile inside the schemata tree: " + why
				e.logf("schemata: %s left out: %s", v.Name, why)
				return true
			}
			return false
		})
	}
}

func writeKeep(p string, data []byte) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, info.Mode().Perm())
}

var compileAt = regexp.MustCompile(`(?m)^(\S+\.go):(\d+)(?::\d+)?: .*$`)

// compileFolded compiles each package the fold changes in the tree's
// copy. It returns the compile errors' output ("" when every package
// compiles) and the mutants they rest on, each with its first error.
func (e *engine) compileFolded(ctx context.Context, w *worker, folded schemata.Result) (map[int]string, string) {
	dirs := map[string]bool{}
	for _, f := range folded.Files {
		dirs[path.Dir(f.Path)] = true
	}
	var out []string
	for d := range dirs {
		mod := nestedModule(w.root, d)
		pkg := "./" + strings.TrimPrefix(strings.TrimPrefix(d, mod), "/")
		if d == mod || d == "." {
			pkg = "."
		}
		o := e.exec(ctx, filepath.Join(w.root, filepath.FromSlash(mod)), []string{"go", "build", "-o", os.DevNull, pkg}, e.env(), buildFloor)
		if o.reason != "" {
			out = append(out, o.tail)
		}
	}
	if len(out) == 0 {
		return nil, ""
	}
	detail := strings.Join(out, "\n")
	blamed := map[int]string{}
	for _, m := range compileAt.FindAllStringSubmatch(detail, -1) {
		line, _ := strconv.Atoi(m[2])
		at := filepath.ToSlash(filepath.Clean(m[1]))
		for _, f := range folded.Files {
			if at != f.Path && !strings.HasSuffix(at, "/"+f.Path) && !strings.HasSuffix(f.Path, "/"+at) {
				continue
			}
			for _, id := range folded.Blame(f.Path, line) {
				if _, ok := blamed[id]; !ok {
					blamed[id] = m[0]
				}
			}
		}
	}
	return blamed, detail
}

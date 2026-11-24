package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

const stdDir = "internal/sketch/std"

// inventory reads the worktree, including untracked files that would enter
// the commit, but not ignored build output. It checks a real package once,
// rather than treating each of its source files as an independent program.
func inventory(root string) ([]site, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.ergo", "*.txtar", "*.go")
	cmd.Dir = root
	index, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Ergo source: %w", err)
	}
	dirs := map[string]map[string]string{}
	var all []site
	var broken []error
	for _, file := range strings.Split(string(index), "\x00") {
		if file == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if os.IsNotExist(err) { // tracked deletion not yet staged
			continue
		}
		if err != nil {
			return nil, err
		}
		if path.Ext(file) == ".go" {
			// Decode before looking for a name: adjacent literals, escape
			// sequences and raw strings can all carry fixture source.
			sites, err := embeddedSites(file, string(data))
			if err != nil {
				broken = append(broken, err)
				continue
			}
			all = append(all, sites...)
			continue
		}
		if !bytes.Contains(data, []byte(legacyName)) {
			continue
		}
		if path.Ext(file) == ".txtar" {
			sites, err := archiveSites(file, string(data))
			if err != nil {
				return nil, err
			}
			all = append(all, sites...)
			continue
		}
		dir := path.Dir(file)
		if dirs[dir] == nil {
			dirs[dir] = map[string]string{}
		}
		dirs[dir][file] = string(data)
	}
	cache := sketchfront.NewCheckCache()
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		sources := dirs[dir]
		files, pkg, err := checkedFiles(root, dir, cache)
		if err != nil {
			return nil, err
		}
		for _, file := range slices.Sorted(maps.Keys(sources)) {
			src := sources[file]
			f := files[path.Base(file)]
			if f == nil || pkg == nil {
				// A refused fixture or a package with a refused import has
				// no checked names. Keep its candidates in a distinct
				// inventory; missing semantic information is not a pass.
				f, err = syntax.Parse(src)
				if err != nil {
					return nil, fmt.Errorf("%s: cannot inventory legacy occurrences: %w", file, err)
				}
				all = append(all, provisionalSites(file, src, f)...)
			} else {
				all = append(all, boundSites(file, src, f, pkg)...)
			}
		}
	}
	return all, errors.Join(broken...)
}

func checkedFiles(root, dir string, cache *sketchfront.CheckCache) (map[string]*syntax.File, *check.Package, error) {
	files := map[string]*syntax.File{}
	var pkg *check.Package
	switch {
	case dir == stdDir:
		pkg = check.PreludePackage()
		for _, f := range pkg.Files() {
			files[path.Base(f.Name)] = f
		}
	case strings.HasPrefix(dir, stdDir+"/"):
		// Check the on-disk declarations, not a prebuilt snapshot of
		// their previous texts. Imported std is supplied by the compiler.
		full := filepath.Join(root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(full)
		if err != nil {
			return nil, nil, err
		}
		var parsed []*syntax.File
		for _, entry := range entries {
			if entry.IsDir() || path.Ext(entry.Name()) != ".ergo" {
				continue
			}
			src, err := os.ReadFile(filepath.Join(full, entry.Name()))
			if err != nil {
				return nil, nil, err
			}
			f, err := syntax.Parse(string(src))
			if err != nil {
				return nil, nil, fmt.Errorf("%s/%s: %w", dir, entry.Name(), err)
			}
			f.Name = entry.Name()
			parsed = append(parsed, f)
			files[f.Name] = f
		}
		pkg, _ = check.StdFixture(strings.TrimPrefix(dir, stdDir+"/"), parsed)
	default:
		a, err := cache.CheckDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return nil, nil, err
		}
		checked := a.Root()
		for _, f := range checked.Files {
			files[path.Base(f.Rel)] = f.Syntax
		}
		pkg = checked.Partial
	}
	return files, pkg, nil
}

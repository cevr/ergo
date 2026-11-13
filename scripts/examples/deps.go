package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// localModules are the folders that the go.mod of a program of packages
// replaces a required Ergo module with (`replace M => ../../modules/xml`,
// grammar.md Source files 1, Required modules (c)), each holding ergo.mod.
// `ergo test` of the program never runs a dependency's tests, as go test
// never does; a module the repository holds has no runner of its own, so
// the test leg runs its packages too (testsOfModule), and an example that
// installs a module this way carries the module's laws and tests into the
// examples suite.
func localModules(p *program) []string {
	data, err := os.ReadFile(filepath.Join(p.dir, "go.mod"))
	if err != nil {
		return nil
	}
	var dirs []string
	block := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "replace (":
			block = true
			continue
		case block && line == ")":
			block = false
			continue
		case strings.HasPrefix(line, "replace "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "replace "))
		case !block:
			continue
		}
		_, to, ok := strings.Cut(line, "=>")
		if !ok {
			continue
		}
		fields := strings.Fields(to)
		if len(fields) != 1 {
			continue // a module version, not a folder
		}
		dir := fields[0]
		if !filepath.IsAbs(dir) && !strings.HasPrefix(dir, "./") && !strings.HasPrefix(dir, "../") {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(p.dir, dir)
		}
		if info, err := os.Stat(filepath.Join(dir, "ergo.mod")); err == nil && info.Mode().IsRegular() {
			dirs = append(dirs, filepath.Clean(dir))
		}
	}
	return dirs
}

// testsOfModule is each package folder of the module rooted at dir: a
// folder that holds a .ergo file, below a folder that holds no other
// ergo.mod.
func testsOfModule(dir string) []string {
	var out []string
	var walk func(d string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		pkg := false
		for _, e := range entries {
			switch {
			case e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "_") && e.Name() != "testdata":
				sub := filepath.Join(d, e.Name())
				if _, err := os.Stat(filepath.Join(sub, "ergo.mod")); err == nil {
					continue
				}
				walk(sub)
			case !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo"):
				pkg = true
			}
		}
		if pkg {
			out = append(out, d)
		}
	}
	walk(dir)
	slices.Sort(out)
	return out
}

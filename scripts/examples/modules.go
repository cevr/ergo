package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A module of modules/ (MK0) is an Ergo library of this repository: its
// own ergo.mod and go.mod, which an example requires and points at by a
// replace to the local folder (grammar.md, Source files 1, Required
// modules). It is a program of packages with no binary and no cases: the
// runner formats its .ergo files, checks its generated files, and runs
// `ergo test` on every package folder, so its tests and laws run in the
// test leg as an example's do. Its layout: ergo.mod and go.mod naming one
// module path, README.md, package folders and top-level .ergo files, and
// optionally go.sum, expected-skips and testdata/.

// modulesDir is the folder of the repository's modules.
const modulesDir = "modules"

// loadModules reads and checks the layout of every folder of modules/; no
// folder is no module. Each problem names its folder.
func loadModules(root string) ([]*program, []string) {
	dir := filepath.Join(root, modulesDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", modulesDir, err)}
	}
	var modules []*program
	var problems []string
	for _, entry := range entries {
		label := modulesDir + "/" + entry.Name()
		if !entry.IsDir() {
			if entry.Name() == "README.md" && entry.Type().IsRegular() {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s: not a module folder; %s/ holds module folders and README.md", label, modulesDir))
			continue
		}
		p, found := loadModule(filepath.Join(dir, entry.Name()), entry.Name())
		for _, problem := range found {
			problems = append(problems, label+": "+problem)
		}
		if len(found) == 0 {
			modules = append(modules, p)
		}
	}
	return modules, problems
}

// loadModule reads the layout of the module folder dir.
func loadModule(dir, name string) (*program, []string) {
	p := &program{name: modulesDir + "/" + name, dir: dir, module: true, library: true}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	present := map[string]bool{}
	for _, entry := range entries {
		file := entry.Name()
		present[file] = true
		switch {
		case file == "testdata":
			if !entry.IsDir() {
				problem("%s is not a folder", file)
			}
		case file == "ergo.mod" || file == "go.mod" || file == "go.sum" || file == "README.md" || file == expectedSkipsFile || strings.HasSuffix(file, ".ergo"):
			if !entry.Type().IsRegular() {
				problem("%s is not a regular file", file)
				continue
			}
			if strings.HasSuffix(file, ".ergo") {
				p.sources = append(p.sources, filepath.Join(dir, file))
			}
		case entry.IsDir() && (file == "cmd" || file == "cases"):
			problem("a module is a library: it has no %s/; an example under examples/ runs it", file)
		case entry.IsDir():
			problems = append(problems, packageSources(p, filepath.Join(dir, file), file)...)
		default:
			problem("unexpected %s; a module holds ergo.mod, go.mod, README.md and package folders, and may hold go.sum, expected-skips and testdata/", file)
		}
	}
	for _, required := range []string{"ergo.mod", "go.mod", "README.md"} {
		if !present[required] {
			problem("no %s", required)
		}
	}
	if present["ergo.mod"] && present["go.mod"] {
		ergoName, err := moduleName(filepath.Join(dir, "ergo.mod"))
		if err != nil {
			problem("%v", err)
		}
		goName, err := moduleName(filepath.Join(dir, "go.mod"))
		if err != nil {
			problem("%v", err)
		}
		if ergoName != "" && goName != "" && ergoName != goName {
			problem("ergo.mod names the module %s and go.mod %s; a required module's ergo.mod names its path", ergoName, goName)
		}
	}
	if len(p.sources) == 0 {
		problem("no .ergo file")
	}
	slices.Sort(p.sources)
	problems = append(problems, moduleLawProblems(p)...)
	for _, source := range p.sources {
		if target := filepath.Dir(source); !slices.Contains(p.tests, target) {
			p.tests = append(p.tests, target)
		}
	}
	slices.Sort(p.tests)
	return p, problems
}

// runModule formats a module, checks its generated files and runs the test
// leg on every package folder: a module has nothing to build and no case.
func (r *runner) runModule(p *program) {
	if o, err := r.tool(0, append([]string{"fmt", "--check"}, p.sources...)...); err != nil || !o.passed() {
		r.toolFailed(p, "fmt", o, err)
		return
	}
	if !r.genProgram(p) {
		return
	}
	r.testProgram(p)
}

// selectModules is the modules a run takes: those named, or every one.
func selectModules(names []string, modules []*program) ([]*program, error) {
	if len(names) == 0 {
		return modules, nil
	}
	var selected []*program
	for _, name := range names {
		at := slices.IndexFunc(modules, func(p *program) bool { return p.name == modulesDir+"/"+name || p.name == name })
		if at < 0 {
			return nil, usageError(fmt.Sprintf("examples: unknown module %q", name))
		}
		if !slices.Contains(selected, modules[at]) {
			selected = append(selected, modules[at])
		}
	}
	return selected, nil
}

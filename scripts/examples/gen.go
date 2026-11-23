package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/genmark"
)

// generated is the kind and argument of `ergo gen KIND --check` for one
// generated package of the program.
type generated struct {
	kind   string
	target string
}

// generatedContracts are the contracts of the program's generated files,
// each once, sorted. A folder ergo gen wrote holds its marker (genmark),
// which names the kind and the contract IMPORT.OPTYPE, whose import path
// is the module's name and the package's folder under the program.
func generatedContracts(p *program) ([]generated, error) {
	module, err := moduleName(filepath.Join(p.dir, "ergo.mod"))
	if err != nil {
		return nil, err
	}
	var out []generated
	seen := map[string]bool{}
	for _, source := range p.sources {
		folder := filepath.Dir(source)
		if seen[folder] {
			continue
		}
		seen[folder] = true
		mark, ok, err := genmark.Read(folder)
		if err != nil {
			return nil, err
		}
		if !ok || mark.Kind == "bind" {
			continue
		}
		if len(mark.Subjects) == 0 || (mark.Kind != "schema" && len(mark.Subjects) != 1) {
			return nil, fmt.Errorf("%s: %s has the wrong number of subjects for %s", folder, genmark.Name, mark.Kind)
		}
		path, opType, _ := genmark.SplitSubject(mark.Subjects[0])
		var dir string
		switch {
		case path == module:
			dir = p.dir
		case strings.HasPrefix(path, module+"/"):
			dir = filepath.Join(p.dir, filepath.FromSlash(strings.TrimPrefix(path, module+"/")))
		default:
			return nil, fmt.Errorf("%s: the contract %q is not a package of the module %s", folder, mark.Subjects[0], module)
		}
		g := generated{kind: mark.Kind, target: dir + "." + opType}
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b generated) int { return strings.Compare(a.kind+" "+a.target, b.kind+" "+b.target) })
	return out, nil
}

// moduleName is the name an ergo.mod declares.
func moduleName(file string) (string, error) {
	text, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(text), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(name), nil
		}
	}
	return "", fmt.Errorf("%s declares no module", file)
}

// genProgram runs `ergo gen KIND --check` on each contract of the
// program's generated files: a file that differs from what the contract
// generates now (a stale file) fails the program.
func (r *runner) genProgram(p *program) bool {
	if !p.module {
		return true
	}
	contracts, err := generatedContracts(p)
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s: gen: %v\n", p.name, err)
		r.fail(p.name + ": gen")
		return false
	}
	passed := true
	for _, g := range contracts {
		leg := "gen " + g.kind + " " + strings.TrimPrefix(g.target, p.dir+string(filepath.Separator))
		if o, err := r.tool(0, "gen", g.kind, "--check", g.target); err != nil || !o.Passed() {
			r.toolFailed(p, leg, o, err)
			passed = false
		}
	}
	return passed
}

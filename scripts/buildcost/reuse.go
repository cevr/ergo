package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// reuse counts, in process, what the incremental check would reuse (CS5a;
// docs/spec/build.md, Interface fingerprint): the program is checked, then
// edited and checked again for each edit in turn (a comment in the lowest
// package, a body there, an export added there, a declaration added to
// main), and each line says how many packages the cutoff would check
// again (check.Reusable) out of the packages a check from scratch checks.
// Counts only: the same program and edits give the same lines on any host.
func (r runner) reuse(label, dir, main, leaf string) {
	pkg := filepath.Join(dir, main)
	path := filepath.Join(dir, leaf)
	appendTo := func(file, text string) {
		data, _ := os.ReadFile(file)
		os.WriteFile(file, append(data, text...), 0o644)
	}
	prev, ok := reuseCheck(label, "first", pkg, nil)
	if !ok {
		return
	}
	edits := []struct {
		name string
		do   func() bool
	}{
		{"comment-edit", func() bool { appendTo(path, "\n// a comment edit\n"); return true }},
		{"leaf-body-edit", func() bool { return bodyEdit(path) }},
		{"leaf-export-added", func() bool {
			appendTo(path, "\n// BuildcostAdded is an export added.\nfunc BuildcostAdded(n int) int {\n\tn + 1\n}\n")
			return true
		}},
		{"main-decl-added", func() bool {
			appendTo(filepath.Join(pkg, "main.ergo"), "\nfunc buildcostMain() int {\n\t2\n}\n")
			return true
		}},
	}
	for _, e := range edits {
		if !e.do() {
			fmt.Printf("%s %s: no edit (no integer or string literal in a body of a function of %s that is not generic)\n", label, e.name, leaf)
			continue
		}
		cur, ok := reuseCheck(label, e.name, pkg, prev)
		if !ok {
			return
		}
		prev = cur
	}
}

// reuseCheck checks the program at pkg and prints its counted line
// against the fingerprints of the check before, prev.
func reuseCheck(label, step, pkg string, prev []check.Fingerprint) ([]check.Fingerprint, bool) {
	a, err := sketchfront.CheckDir(pkg)
	if err != nil {
		fmt.Printf("%s %s: %v\n", label, step, err)
		return nil, false
	}
	fps := a.Fingerprints()
	if len(a.Diagnostics) > 0 {
		fmt.Printf("%s %s: %d diagnostics, first %s %s\n", label, step, len(a.Diagnostics), a.Diagnostics[0].File, a.Diagnostics[0].Message)
		return fps, false
	}
	if prev == nil {
		fmt.Printf("%s %s: packages %d, checked %d\n", label, step, len(fps), len(fps))
		return fps, true
	}
	reused := check.Reusable(prev, fps)
	var again []string
	for _, fp := range fps {
		if !slices.Contains(reused, fp.Path) {
			again = append(again, fp.Path)
		}
	}
	list := ""
	if len(again) <= 4 {
		list = " (" + strings.Join(again, ", ") + ")"
	}
	fmt.Printf("%s %s: packages %d, checked again %d%s, reusable %d\n", label, step, len(fps), len(again), list, len(reused))
	return fps, true
}

// bodyEdit adds one to the first integer literal in the body of a function
// of the file at path that is not generic, a body edit that keeps the
// file's declarations. It reports false when there is none.
func bodyEdit(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	f, err := syntax.Parse(string(data))
	if err != nil {
		return false
	}
	for _, d := range f.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil || fd.TypeParams != nil {
			continue
		}
		var lit, str *syntax.BasicLit
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			if b, ok := n.(*syntax.BasicLit); ok && lit == nil {
				if _, err := strconv.Atoi(b.Value); err == nil && b.Kind == syntax.INT {
					lit = b
				} else if b.Kind == syntax.STRING && str == nil && strings.HasPrefix(b.Value, `"`) {
					str = b
				}
			}
			return lit == nil
		})
		var out string
		switch {
		case lit != nil:
			v, _ := strconv.Atoi(lit.Value)
			out = string(data[:lit.ValuePos]) + strconv.Itoa(v+1) + string(data[lit.ValueEnd:])
		case str != nil:
			// A string's body edit: one more letter after its opening quote.
			out = string(data[:str.ValuePos+1]) + "x" + string(data[str.ValuePos+1:])
		default:
			continue
		}
		return os.WriteFile(path, []byte(out), 0o644) == nil
	}
	return false
}

package std_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// One Go body has one Ergo owner (std-strengths SS1). A std package may
// import another (docs/spec/stdlib.md, io 9), so a package that needs a
// function of another std package imports it; it never binds that
// package's runtime/v1 body a second time under a private name. The one
// exception is a body that takes the hidden reading package
// (v1.ReadingPackage): the lowering passes each caller's own package, so
// each package's extern reads only its own bindings (stdlib.md, Hidden
// arguments 5).

// staleImportRule are the phrases of the old rule "a std package imports
// no other std package", which no std comment may hold.
var staleImportRule = []string{"imports no other std package", "does not import another", "deadlocks the checker"}

// stdBinding is one extern of a std package and the runtime/v1 body it
// names.
type stdBinding struct {
	pkg, name, body string
}

// TestStdBindsNoForeignBody: no runtime/v1 body is bound by externs of two
// std packages, and no std source states the stale import rule. The
// externs come from the checked packages (std.Packages() and the prelude),
// never from a written list.
func TestStdBindsNoForeignBody(t *testing.T) {
	var bindings []stdBinding
	sources := map[string]string{}
	for _, p := range stdLawPackages(t) {
		for _, f := range p.pkg.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil {
				continue
			}
			target, body := f.Decl.Extern.Split()
			bindings = append(bindings, stdBinding{pkg: p.path, name: f.Name, body: target + "." + body})
		}
		files := std.Prelude()
		if p.path != std.PreludePath {
			files, _ = std.Package(p.path)
		}
		for name, text := range files {
			sources[p.path+"/"+name] = text
		}
	}
	if len(bindings) < 50 {
		t.Fatalf("the walk found %d std externs; want 50 or more", len(bindings))
	}
	for _, problem := range foreignBindingProblems(bindings, sources, readsOwnPackage) {
		t.Error(problem)
	}
}

// readsOwnPackage reports whether the runtime/v1 body takes the hidden
// reading package, so that each package that binds it reads its own.
func readsOwnPackage(body string) bool {
	name, ok := strings.CutPrefix(body, "ergo.local/prototype/runtime/v1.")
	if !ok {
		return false
	}
	b, err := v1.BodyOf(name)
	return err == nil && slices.Contains(b.Hidden, v1.ReadingPackage)
}

// TestForeignBindingFixtures: the rule names both externs of a body bound
// in two packages, ignores two externs of one package and a body that
// reads its own package, and names a stale comment.
func TestForeignBindingFixtures(t *testing.T) {
	bindings := []stdBinding{
		{"slices", "WithCapacity", "v1.WithCapacity"},
		{"rand", "withCapacity", "v1.WithCapacity"},
		{"strings", "HasSuffix", "v1.StringsHasSuffix"},
		{"strings", "hasSuffix", "v1.StringsHasSuffix"},
		{"log", "contextValue", "v1.ContextValue"},
		{"trace", "contextValue", "v1.ContextValue"},
	}
	sources := map[string]string{
		"rand/rand.ergo":  "// withCapacity: a std package does not import another yet.\nfunc f() {}\n",
		"io/io.ergo":      "// A std package may import another.\n",
		"filepath/f.ergo": "func g() {}\n\n// hasSuffix is for the laws: a std package imports no\n//   other std package, so it binds the body.\n",
	}
	got := foreignBindingProblems(bindings, sources, func(body string) bool { return body == "v1.ContextValue" })
	want := []string{
		"filepath/f.ergo:3: the comment states the old rule (\"imports no other std package\"); a std package may import another (stdlib.md, io 9)",
		"rand/rand.ergo:1: the comment states the old rule (\"does not import another\"); a std package may import another (stdlib.md, io 9)",
		"v1.WithCapacity is bound by rand.withCapacity and slices.WithCapacity; one body has one Ergo owner: import slices and call its function",
	}
	if !slices.Equal(got, want) {
		t.Errorf("the problems are\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// foreignBindingProblems are the bodies bound by externs of two or more
// std packages (except those that read their own package) and the source
// lines that state the stale import rule, sorted.
func foreignBindingProblems(bindings []stdBinding, sources map[string]string, perPackage func(body string) bool) []string {
	var out []string
	byBody := map[string][]stdBinding{}
	for _, b := range bindings {
		byBody[b.body] = append(byBody[b.body], b)
	}
	for body, bs := range byBody {
		pkgs := map[string]bool{}
		for _, b := range bs {
			pkgs[b.pkg] = true
		}
		if len(pkgs) < 2 || perPackage(body) {
			continue
		}
		var names []string
		owner := ""
		for _, b := range bs {
			names = append(names, b.pkg+"."+b.name)
			if b.name != "" && b.name[0] >= 'A' && b.name[0] <= 'Z' {
				owner = b.pkg
			}
		}
		slices.Sort(names)
		fix := "import the package that exports it and call its function"
		if owner != "" {
			fix = "import " + owner + " and call its function"
		}
		out = append(out, fmt.Sprintf("%s is bound by %s; one body has one Ergo owner: %s", body, strings.Join(names, " and "), fix))
	}
	for file, text := range sources {
		// The lines joined by one space each, with their comment markers
		// and indentation removed, so a phrase that a comment wraps across
		// two lines is found; starts[i] is where line i+1 begins.
		var joined strings.Builder
		var starts []int
		for _, line := range strings.Split(text, "\n") {
			starts = append(starts, joined.Len())
			line = strings.TrimSpace(line)
			line = strings.TrimSpace(strings.TrimPrefix(line, "//"))
			joined.WriteString(strings.ToLower(line) + " ")
		}
		all := joined.String()
		for _, phrase := range staleImportRule {
			for at := 0; ; {
				i := strings.Index(all[at:], phrase)
				if i < 0 {
					break
				}
				line, _ := slices.BinarySearch(starts, at+i+1)
				out = append(out, fmt.Sprintf("%s:%d: the comment states the old rule (%q); a std package may import another (stdlib.md, io 9)", file, line, phrase))
				at += i + len(phrase)
			}
		}
	}
	slices.Sort(out)
	return out
}

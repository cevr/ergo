package provenance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

const (
	module       = "ergo.local/prototype"
	checkPath    = module + "/internal/sketch/check"
	mutantPath   = module + "/internal/sketch/mutant/check"
	manifestFile = "internal/sketch/check/testdata/provenance-manifest.txt"
)

// listed is the part of go list -json the copy needs.
type listed struct {
	Dir, ImportPath, Name string
	GoFiles, EmbedFiles   []string
	Module                *struct{ Path string }
}

// TestControls runs the analyzer on a module the test builds from the
// repository's checker: a pristine copy of internal/sketch/check, which must
// stay clean (the negative control), and a mutant copy of it at
// internal/sketch/mutant/check that adds the controls (testdata/controls,
// each a forbidden flow with its // want lines), the accepted shapes
// (testdata/accepts, no wants), and the patches below to the checker's own
// files. Both copies bind to the repository's one manifest; the mutant's
// is that manifest with its package line moved and the controls' sites
// added.
func TestControls(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module "+module+"\n\ngo 1.27\n")

	cmd := exec.Command("go", "list", "-deps", "-json", "./internal/sketch/check")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var check listed
	for dec := json.NewDecoder(strings.NewReader(string(out))); dec.More(); {
		var p listed
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p.Module == nil || p.Module.Path != module {
			continue
		}
		rel, _ := filepath.Rel(repo, p.Dir)
		copyPackage(t, p, filepath.Join(dir, rel), nil)
		if p.ImportPath == checkPath {
			check = p
		}
	}
	if check.Dir == "" {
		t.Fatalf("go list did not list %s", checkPath)
	}

	// The mutant: the checker's files with the patches, the controls and
	// the accepted shapes, and the fixture packages and syntax method the
	// controls use.
	mutantDir := filepath.Join(dir, "internal", "sketch", "mutant", "check")
	copyPackage(t, check, mutantDir, patches)
	for _, sub := range []string{"controls", "accepts"} {
		copyDir(t, filepath.Join("testdata", sub), mutantDir)
	}
	copyDir(t, filepath.Join("testdata", "fixtures", "syntax"), filepath.Join(dir, "internal", "sketch", "syntax"))
	for _, fixture := range []string{"e2bdup", "e2bmaker"} {
		copyDir(t, filepath.Join("testdata", "fixtures", fixture), filepath.Join(dir, "internal", "sketch", fixture))
	}

	manifest, err := os.ReadFile(filepath.Join(repo, manifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(string(manifest)); err != nil {
		t.Fatalf("the repository's manifest: %v", err)
	}
	pristine := filepath.Join(dir, "pristine-manifest.txt")
	write(t, pristine, string(manifest))
	mutant := string(manifest)
	for _, edit := range mutantManifest {
		if !strings.Contains(mutant, edit.old) {
			t.Fatalf("the manifest has no %q to edit", edit.old)
		}
		mutant = strings.Replace(mutant, edit.old, edit.new, 1)
	}
	mutantFile := filepath.Join(dir, "mutant-manifest.txt")
	write(t, mutantFile, mutant)

	a := New()
	for _, path := range []string{pristine, mutantFile} {
		if err := a.Flags.Set("manifest", path); err != nil {
			t.Fatal(err)
		}
	}
	analysistest.Run(t, dir, a, "./internal/sketch/check", "./internal/sketch/mutant/check")
}

// mutantManifest moves the manifest to the mutant and lists the sites the
// controls need: a parser site that holds a forbidden flow must be listed,
// or the control fails for the wrong reason.
var mutantManifest = []struct{ old, new string }{
	{"package " + checkPath + "\n", "package " + mutantPath + "\n"},
	{"callee Parse CheckSource prelude$1 (*stdImporter).Import\n", "callee Parse CheckSource prelude$1 (*stdImporter).Import parseValueAtSite closureAtSite closureReturnedAtSite closureNestedAtSite$1 (*checker).closureInMethodAtSite acceptParenParse\n"},
	{"site (*checker).assignStmt BinaryExpr compoundFixture\n", "site (*checker).assignStmt BinaryExpr compoundFixture\nsite acceptExplicitCalls Ident none\nsite acceptClosureCalls$1 Ident none\nsite acceptParenCalls Ident none\n"},
	// Stale entries: a site that makes nothing, and a parser site and an
	// entry site that never reference their callee (reported at made).
	{"entry CheckSource\n", "entry CheckSource staleEntrySite\nsite staleSite Ident none\n"},
	{" acceptParenParse\n", " acceptParenParse staleCalleeSite\n"},
}

// patches are the controls that edit the checker's own files: each anchor
// must occur once, and its replacement carries its // want lines.
var patches = map[string][]struct{ old, new string }{
	// E2c round 1 review M1 (mutant closureHook): a closure at a listed
	// parser site carries the parser to any pass.
	"check.go": {
		{"func CheckSource(src string) (*Package, []Diagnostic) {\n\tfile, err := syntax.Parse(src)\n",
			"func CheckSource(src string) (*Package, []Diagnostic) {\n" +
				"\tsnippetParse = func(s string) (*syntax.File, error) { return syntax.Parse(s) } // want `syntax callee Parse is called in CheckSource\\$1, which the manifest does not list for it` `CheckSource\\$1 makes a node outside the manifest and becomes a function value in CheckSource`\n" +
				"\tfile, err := syntax.Parse(src)\n"},
	},
	// The stale manifest entries are reported at made.
	"owner.go": {
		{"func made[N syntax.Node](c *checker, n N, origin syntax.Node) N {\n",
			"func made[N syntax.Node](c *checker, n N, origin syntax.Node) N { // want `the manifest lists construction site staleSite Ident, which the package no longer makes` `the manifest lists Parse in staleCalleeSite, which no longer calls it` `the manifest lists CheckSource in staleEntrySite, which no longer calls it`\n"},
	},
}

func copyPackage(t *testing.T, p listed, dst string, edits map[string][]struct{ old, new string }) {
	t.Helper()
	files := append(append([]string(nil), p.GoFiles...), p.EmbedFiles...)
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(p.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		// analysistest reads a comment that starts with "want" as an
		// expectation; the checker's prose has one (tails.go).
		src := strings.ReplaceAll(string(data), "// want ", "// prose: want ")
		for _, edit := range edits[name] {
			if strings.Count(src, edit.old) != 1 {
				t.Fatalf("patch anchor in %s occurs %d times: %q", name, strings.Count(src, edit.old), edit.old)
			}
			src = strings.Replace(src, edit.old, edit.new, 1)
		}
		write(t, filepath.Join(dst, name), src)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dst, e.Name()), string(data))
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

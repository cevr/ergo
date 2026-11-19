package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestModuleOwnGoLayout (reds #46; orchestrator ruling on FK2 q7): a
// module's root holds its law ledger, ergo.laws (runtime.md Tests 10),
// and a package folder holds the module's own Go (interop.md Trusted Go 3;
// LF2m), Go, assembly and test files, where the module's go.mod owns the
// folder. A Go file the go.mod does not own (no module line, or another
// module's go.mod between) and a file of no kind stay refused.
//
// Mutant (scripts/examples mutants.json): own-go-unowned.
func TestModuleOwnGoLayout(t *testing.T) {
	t.Parallel()
	const goMod = "module example.com/simd\n\ngo 1.27\n"
	for _, c := range []struct {
		name    string
		goMod   string
		files   []string
		refused []string
	}{
		{"the ledger beside ergo.mod", goMod, []string{"ergo.laws"}, nil},
		{"own Go, assembly and tests", goMod, []string{"kernel/k.go", "kernel/k_amd64.s", "kernel/k_test.go"}, nil},
		{"a go.mod that names no module", "go 1.27\n", []string{"kernel/k.go"}, []string{"kernel/k.go"}},
		{"another module's folder", goMod, []string{"inner/go.mod", "inner/k.go"}, []string{"inner/go.mod", "inner/k.go"}},
		{"a stray file", goMod, []string{"kernel/k.go", "kernel/notes.txt"}, []string{"kernel/notes.txt"}},
	} {
		dir := filepath.Join(t.TempDir(), "simd")
		write := func(name, text string) {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("ergo.mod", "module example.com/simd\n")
		write("go.mod", c.goMod)
		write("README.md", "# simd\n")
		for _, f := range c.files {
			write(f, "package kernel\n")
		}
		_, problems := loadModule(dir, "simd")
		var refused []string
		for _, p := range problems {
			if _, rest, ok := strings.Cut(p, "unexpected "); ok {
				name, _, _ := strings.Cut(rest, ";")
				refused = append(refused, name)
			}
		}
		slices.Sort(refused)
		if !slices.Equal(refused, c.refused) {
			t.Errorf("%s: the layout refuses %q, want %q\n%s", c.name, refused, c.refused, strings.Join(problems, "\n"))
		}
	}
}

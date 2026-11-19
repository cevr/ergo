package lower

import (
	"strings"
	"testing"
)

// TestReachLibraryExportsReached is the smallest repro of red #28 (M10:
// TestSelfhostFormatAgrees' driver did not build, "undefined: Project_1"):
// under the reach lowering a library has no main, so nothing reached its
// exported functions and their wrappers called Go functions no body
// declared. A library's exported functions are reach's roots: the
// library type-checks, the helper an export calls is lowered, and a
// function nothing exported reaches is not. It catches the ledger's
// mutant red28-lib-exports-unreached.
func TestReachLibraryExportsReached(t *testing.T) {
	const src = `func Hello(name string) string {
	greet(name)
}

func greet(name string) string {
	"hi " + name
}

func unused(n int) int {
	n + 1
}
`
	u := checkPackage(t, "greet", []pkgSource{{"/ergo-test/greet/greet.ergo", src}}, nil)
	saved := reachLowering
	reachLowering = ""
	defer func() { reachLowering = saved }()
	files, err := LowerLibrary(u, "example.com/greet", "greet")
	if err != nil {
		t.Fatal(err)
	}
	var lib string
	for _, f := range files {
		if f.Path == "greet.go" {
			lib = string(f.Data)
		}
	}
	if strings.Contains(lib, "func unused(") {
		t.Errorf("the library lowers unused, which no export reaches:\n%s", lib)
	}
	// The host shares the package only to type-check it in one program.
	host := strings.Replace(lib, "package greet", "package main", 1) + "\nfunc main() {}\n"
	typecheckGo(t, []*lowered{{name: "greetlib", src: src, files: []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: []byte(host)}}}})
}

package sketchfront

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestLawEntryNamesItsPlatform (LAW-BUILD-KEY, platforms; runtime.md,
// Tests 10): a ledger entry whose Go code another supported platform
// compiles differently (a kern_linux.go and kern_darwin.go pair) names
// the platform it was given on in its key, so a verdict given on linux is
// never read on darwin; an entry whose Go code every platform compiles
// alike keeps one key. The key is read from the files each build selects,
// so it needs no Go built.
//
// Mutant (sketchfront mutants.json): lbk-platform-unkeyed.
func TestLawEntryNamesItsPlatform(t *testing.T) {
	t.Setenv("GOEXPERIMENT", "")
	const body = "package kern\n\nfunc Index(s []byte, c byte) int {\n\tfor i := 0; i < len(s); i++ {\n\t\tif s[i] == c {\n\t\t\treturn i %s\n\t\t}\n\t}\n\treturn -1\n}\n"
	dir := writeTree(t, map[string]string{
		"go.mod":              "module example.com/app\n\ngo 1.27\n",
		"ergo.mod":            "module app\n",
		"kern/kern_linux.go":  fmt.Sprintf(body, ""),
		"kern/kern_darwin.go": fmt.Sprintf(body, "+ 0"),
		"plain/plain.go":      "package plain\n\nfunc Twice(n int) int { return n + n }\n",
		"main.ergo": `import strconv

extern go example.com/app/kern.Index func index(s []byte, c byte) int
extern go example.com/app/plain.Twice func twice(n int) int

func indexRef(s []byte, c byte) int {
	for i, b in s {
		if b == c {
			return i
		}
	}
	-1
}

func twiceRef(n int) int { n * 2 }

law IndexIsRef(s []byte, c byte) { indexRef(s, c) } == { index(s, c) }

law TwiceIsRef(n int) { twiceRef(n) } == { twice(n) }

func main() needs Console {
	Console.Println(strconv.Itoa(indexRef([]byte{1, 2}, 2) + twiceRef(1)))
}
`,
	})
	keys := func(p gobind.Platform) map[string]string {
		t.Helper()
		a, err := CheckDirForPlatform(dir, p)
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); got != "" {
			t.Fatalf("%s: %s", p, got)
		}
		pkg := a.rootPackage()
		out := map[string]string{}
		for _, law := range pkg.Laws {
			out[law.Name] = lower.FuncLawEntry(law, ledgerNames(a), "validated", foreignKeys(a, nil)).Key()
		}
		return out
	}
	for _, p := range []gobind.Platform{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "darwin", GOARCH: "arm64"}} {
		got := keys(p)
		if want := "app.IndexIsRef under " + p.String(); got["IndexIsRef"] != want {
			t.Errorf("%s: law IndexIsRef is keyed %q; want %q: kern's Index is another file on another platform", p, got["IndexIsRef"], want)
		}
		if want := "app.TwiceIsRef"; got["TwiceIsRef"] != want {
			t.Errorf("%s: law TwiceIsRef is keyed %q; want %q: plain's Twice is one file on every platform", p, got["TwiceIsRef"], want)
		}
	}
}

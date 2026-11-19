package lower

import (
	"testing"

	runtimefiles "ergo.local/prototype/runtime"
)

// TestForeignKeysNameTheirBuild (LAW-BUILD-KEY r3; runtime.md, Tests 10):
// the build part of a key outside the program's own folders.
//   - The runtime is the compiler's own Go: a function whose body is one
//     file on linux and another on darwin (exchangePaths) names the
//     target's platform; one every platform compiles alike
//     (ConfigFlagName) names none.
//   - A required module from go.sum is not read, and third-party Go has
//     no contract that every build runs one body: its key names every
//     variant's tag.
//   - Go's standard library names none: a verdict over it holds on every
//     platform of one Go version.
//
// Mutants (lower mutants.json): lbk-runtime-every-file,
// lbk-sum-shared, lbk-pull-linkname-widens (ConfigFlagName: darwin's
// exchange_darwin.go pulls syscall.syscall, and a pull widening the
// package would key every runtime/v1 function by platform).
func TestForeignKeysNameTheirBuild(t *testing.T) {
	linux := GoTarget{GOOS: "linux", GOARCH: "amd64"}
	src := ForeignKeySource{
		Toolchain: "go1.27.4",
		Module: GoModule{
			Modules: []string{"example.com/lib"},
			Require: []GoRequire{{Path: "example.com/lib", Version: "v1.0.0"}},
			Sum:     "example.com/lib v1.0.0 h1:lib=\n",
		},
		Target: &linux,
		Variants: []BuildVariant{
			{Tag: "linux/amd64", Target: GoTarget{GOOS: "darwin", GOARCH: "arm64"}},
			{Tag: "linux/amd64", Target: GoTarget{GOOS: "linux", GOARCH: "arm64"}},
		},
	}
	keys := src.Keys()
	v1 := runtimefiles.Module + "/runtime/v1."
	for _, c := range []struct{ target, want, why string }{
		{v1 + "exchangePaths", "linux/amd64", "its body is exchange_linux.go here and exchange_darwin.go on darwin"},
		{v1 + "ConfigFlagName", "", "every platform compiles the declarations it reaches"},
		{"example.com/lib/text.Twice", "linux/amd64", "a go.sum module is not read"},
		{"strings.Count", "", "Go's standard library holds on every platform of one Go version"},
	} {
		if got := keys(c.target).Build; got != c.want {
			t.Errorf("%s: the key's build is %q; want %q: %s", c.target, got, c.want, c.why)
		}
	}
}

// TestLinknameWidens (LAW-BUILD-KEY r3; orchestrator ruling on q6): a
// //go:linkname that pulls a symbol onto a function of its file with no
// body widens the key only for a walk that reaches that function; a
// linkname on anything else (a push of a declaration with a body, or a
// directive the walk cannot classify) widens the whole package.
//
// Mutants (lower mutants.json): lbk-pull-linkname-nothing (a pull widens
// nothing: the reached pull's key misses the package), lbk-push-linkname-
// narrow (every linkname is taken as a pull).
func TestLinknameWidens(t *testing.T) {
	const pull = "package a\n\nimport _ \"unsafe\"\n\n//go:linkname raw runtime.nanotime\nfunc raw() int64\n\nfunc Reach() int64 { return raw() }\n\nfunc Plain() int { return 1 }\n"
	const push = "package a\n\nimport _ \"unsafe\"\n\n//go:linkname out example.com/x.out\nfunc out() int { return 2 }\n\nfunc Plain() int { return 1 }\n"
	const odd = "package a\n\nimport _ \"unsafe\"\n\n//go:linkname raw\nfunc raw() int64\n\nfunc Plain() int { return 1 }\n"
	widened := func(src, name string) bool {
		w := newGoWorld(func(p string) ([]GoSourceFile, bool) {
			if p != "ex.test/a" {
				return nil, false
			}
			return []GoSourceFile{{Name: "a.go", Data: []byte(src)}, {Name: "other.go", Data: []byte("package a\n\nfunc Unrelated() int { return 3 }\n")}}, true
		})
		return len(w.closure("ex.test/a", name).Widened) > 0
	}
	for _, c := range []struct {
		name, src, fn string
		want          bool
	}{
		{"a reached pull", pull, "Reach", true},
		{"an unreached pull", pull, "Plain", false},
		{"a push", push, "Plain", true},
		{"an unclassified directive", odd, "Plain", true},
	} {
		if got := widened(c.src, c.fn); got != c.want {
			t.Errorf("%s: %s's key widens to the package: %v; want %v", c.name, c.fn, got, c.want)
		}
	}
}

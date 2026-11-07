package v1

import (
	"errors"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// pathSegments is the alphabet of generated paths: the empty segment,
// ".", "..", plain and dotted names, a dot file, separators alone and in
// runs, a multi-byte rune and an invalid byte (std-align S3a).
var pathSegments = []string{"", ".", "..", "a", "b.c", ".d", "e.", "/", "//", "é", "\xff"}

// genPath is a path of 0 to 6 segments, joined by one separator, by none
// or by a run, so separators lead, trail and repeat.
func genPath(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(7) {
		b.WriteString(pathSegments[r.IntN(len(pathSegments))])
		switch r.IntN(4) {
		case 0:
		case 1:
			b.WriteString("//")
		default:
			b.WriteString("/")
		}
	}
	if r.IntN(3) == 0 {
		return strings.TrimSuffix(b.String(), "/")
	}
	return b.String()
}

// lawCases is the number of generated cases per law.
const lawCases = 10_000

// TestFilepathLaws runs each law of std filepath (stdlib.md, Std laws)
// over the Go bodies its Ergo functions call, with 10,000 seeded paths per
// law. It is the ledger's runner for every filepath law.
func TestFilepathLaws(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 1))
	split := func(p string) (string, string) { return FilepathSplitDir(p), FilepathSplitFile(p) }
	trailing := func(p string) bool { _, f := split(p); return p != "" && f == "" }
	laws := map[string]func(p, q string) bool{
		"CleanIsIdempotent": func(p, _ string) bool { return FilepathClean(FilepathClean(p)) == FilepathClean(p) },
		"CleanKeepsRoot":    func(p, _ string) bool { return FilepathIsAbs(FilepathClean(p)) == FilepathIsAbs(p) },
		"SplitRebuilds": func(p, _ string) bool {
			d, f := split(p)
			return d+f == p && FilepathSplitFile(f) == f
		},
		"JoinOfDirAndBase": func(p, _ string) bool {
			if trailing(p) {
				return true
			}
			return FilepathClean(FilepathJoin([]string{FilepathDir(p), FilepathBase(p)})) == FilepathClean(p)
		},
		"BaseHasNoSeparator": func(p, _ string) bool {
			b := FilepathBase(p)
			return b == "/" || FilepathSplitDir(b) == ""
		},
		"ExtIsTheExtOfBase": func(p, _ string) bool {
			if FilepathSplitFile(p) == "" {
				return true
			}
			return FilepathExt(FilepathBase(p)) == FilepathExt(p)
		},
		"RelThenJoin": func(base, target string) bool {
			rel, err := FilepathRel(base, target)
			if err != nil {
				return true
			}
			return FilepathClean(FilepathJoin([]string{base, rel})) == FilepathClean(target)
		},
	}
	for name, law := range laws {
		related := 0
		for range lawCases {
			p, q := genPath(r), genPath(r)
			if name == "RelThenJoin" && r.IntN(2) == 0 {
				q = p + "/" + genPath(r) // a target under base, so Rel succeeds often
			}
			if name == "RelThenJoin" {
				if _, err := FilepathRel(p, q); err == nil {
					related++
				}
			}
			if !law(p, q) {
				t.Errorf("%s(%q, %q) is false", name, p, q)
				break
			}
		}
		if name == "RelThenJoin" && related < lawCases/4 {
			t.Errorf("RelThenJoin: Rel succeeded in %d of %d cases; the generator misses the law's domain", related, lawCases)
		}
	}
}

// TestFilepathMatchesGo holds each body to Go's path/filepath, the
// reference model, on generated paths: the same result, and for Rel the
// same success or failure, a failure being NotRelative with both paths.
func TestFilepathMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 8))
	for range lawCases {
		p, q := genPath(r), genPath(r)
		parts := []string{p, q, genPath(r)}[:r.IntN(4)]
		d, f := filepath.Split(p)
		if FilepathJoin(parts) != filepath.Join(parts...) || FilepathBase(p) != filepath.Base(p) ||
			FilepathDir(p) != filepath.Dir(p) || FilepathExt(p) != filepath.Ext(p) ||
			FilepathClean(p) != filepath.Clean(p) || FilepathIsAbs(p) != filepath.IsAbs(p) ||
			FilepathSplitDir(p) != d || FilepathSplitFile(p) != f {
			t.Fatalf("a body differs from path/filepath at %q, %q", p, parts)
		}
		want, wantErr := filepath.Rel(p, q)
		got, err := FilepathRel(p, q)
		var nr NotRelative
		switch {
		case wantErr == nil && (err != nil || got != want):
			t.Fatalf("FilepathRel(%q, %q) = %q, %v; want %q", p, q, got, err, want)
		case wantErr != nil && (!errors.As(err, &nr) || nr != NotRelative{Base: p, Target: q} || got != ""):
			t.Fatalf("FilepathRel(%q, %q) = %q, %v; want NotRelative", p, q, got, err)
		}
	}
	if got := (NotRelative{Base: "/a", Target: "b"}).Error(); got != "filepath.NotRelative{Base:/a Target:b}" {
		t.Errorf("NotRelative.Error() = %q", got)
	}
}

// TestPureImportsExceptRefused: path/filepath is in PureImports, and a
// marked body that names one of its members that reach the host, directly,
// as a value or through a dot import, is refused; a lexical member is not.
func TestPureImportsExceptRefused(t *testing.T) {
	lexical := `package v1

import fp "path/filepath"

//ergo:pure
func Clean(p string) string { return fp.Clean(p) }

//ergo:pure
func Abs(p string) string { s, _ := fp.Abs(p); return s }

//ergo:pure
func Glob(p string) int { f := fp.Glob; m, _ := f(p); return len(m) }
`
	dotted := `package v1

import . "path/filepath"

//ergo:pure
func Base2(p string) string { return Base(p) }
`
	decls, err := parseDecls(fstest.MapFS{"a.go": {Data: []byte(lexical)}, "b.go": {Data: []byte(dotted)}})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := decls.body("Clean"); err != nil || !b.Pure {
		t.Errorf("Clean: %+v, %v; want Pure", b, err)
	}
	for name, msg := range map[string]string{"Abs": "fp.Abs", "Glob": "fp.Glob", "Base2": "dot-imports path/filepath"} {
		if _, err := decls.body(name); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
	for _, name := range []string{"FilepathJoin", "FilepathBase", "FilepathDir", "FilepathExt", "FilepathClean", "FilepathIsAbs", "FilepathSplitDir", "FilepathSplitFile", "FilepathRel"} {
		if b, err := BodyOf(name); err != nil || !b.Pure {
			t.Errorf("BodyOf(%s) = %+v, %v; want Pure", name, b, err)
		}
	}
}

// The benchmarks hold each body to its Go function on the same inputs: the
// body is the control's one-line wrapper, so the target is parity.
var benchPaths = []string{"a/b/../c//d.txt", "/usr/local/lib/../share/./doc", "x", "../../a/b/c/d/e/f.go"}

var sinkString string

func BenchmarkFilepathClean(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			sinkString = FilepathClean(benchPaths[i%len(benchPaths)])
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			sinkString = filepath.Clean(benchPaths[i%len(benchPaths)])
		}
	})
}

func BenchmarkFilepathJoin(b *testing.B) {
	parts := []string{"a/b", "../c", "d.txt"}
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			sinkString = FilepathJoin(parts)
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			sinkString = filepath.Join(parts...)
		}
	})
}

func BenchmarkFilepathRel(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			sinkString, _ = FilepathRel("/usr/local/lib", "/usr/share/doc/x")
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			sinkString, _ = filepath.Rel("/usr/local/lib", "/usr/share/doc/x")
		}
	})
}

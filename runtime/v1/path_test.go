package v1

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"ergo.local/prototype/internal/lawledger"
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

// lawCases (collections_test.go) is the number of generated cases per law.

// TestFilepathLaws runs each law of std filepath (stdlib.md, Std laws)
// over the Go bodies its Ergo functions call, with 10,000 seeded paths per
// law. It is the ledger's runner for every filepath law.
func TestFilepathLaws(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 1))
	split := func(p string) (string, string) { s := FilepathSplit(p); return s.Dir, s.File }
	trailing := func(p string) bool { _, f := split(p); return p != "" && f == "" }
	laws := map[string]func(p, q string) bool{
		"CleanIsIdempotent": func(p, _ string) bool { return FilepathClean(FilepathClean(p)) == FilepathClean(p) },
		"CleanKeepsRoot":    func(p, _ string) bool { return FilepathIsAbs(FilepathClean(p)) == FilepathIsAbs(p) },
		"SplitRebuilds": func(p, _ string) bool {
			d, f := split(p)
			return d+f == p && FilepathSplit(f).File == f
		},
		"JoinOfDirAndBase": func(p, _ string) bool {
			if trailing(p) {
				return true
			}
			return FilepathClean(FilepathJoin([]string{FilepathDir(p), FilepathBase(p)})) == FilepathClean(p)
		},
		"BaseHasNoSeparator": func(p, _ string) bool {
			b := FilepathBase(p)
			return b == "/" || FilepathSplit(b).Dir == ""
		},
		"ExtIsSuffixOfBase": func(p, _ string) bool {
			return StringsHasSuffix(FilepathBase(p), FilepathExt(p))
		},
		"ExtIsTheExtOfBase": func(p, _ string) bool {
			if FilepathSplit(p).File == "" {
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
	ledger := map[string]lawledger.Law{}
	for name, law := range laws {
		ledger["filepath "+name] = lawledger.Law{Statement: filepathLawStatements["filepath "+name], Check: func(t *testing.T) {
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
		}}
	}
	lawledger.Run(t, ledger)
}

// filepathLawStatements is the statement each law of TestFilepathLaws
// restates, the law's source text as the ledger writes it; lawledger.Run
// holds it equal to the ledger (fu-ledger-runner).
var filepathLawStatements = map[string]string{
	"filepath BaseHasNoSeparator": `BaseHasNoSeparator(path string) { Base(path) == "/" || Split(Base(path)).Dir == "" }`,
	"filepath CleanIsIdempotent":  `CleanIsIdempotent(path string) { Clean(Clean(path)) == Clean(path) }`,
	"filepath CleanKeepsRoot":     `CleanKeepsRoot(path string) { IsAbs(Clean(path)) == IsAbs(path) }`,
	"filepath ExtIsSuffixOfBase":  `ExtIsSuffixOfBase(path string) { strings.HasSuffix(Base(path), Ext(path)) }`,
	"filepath ExtIsTheExtOfBase":  `ExtIsTheExtOfBase(path string) { if Split(path).File == "" { return true } Ext(Base(path)) == Ext(path) }`,
	"filepath JoinOfDirAndBase":   `JoinOfDirAndBase(path string) { if path != "" && Split(path).File == "" { return true } Clean(Join([]string{Dir(path), Base(path)})) == Clean(path) }`,
	"filepath RelThenJoin":        `RelThenJoin(base string, target string) { let r = Rel(base, target) catch { case NotRelative: return true } Clean(Join([]string{base, r})) == Clean(target) }`,
	"filepath SplitRebuilds":      `SplitRebuilds(path string) { let p = Split(path) p.Dir+p.File == path && Split(p.File).File == p.File }`,
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
			FilepathSplit(p) != (Parts{Dir: d, File: f}) {
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
	for _, name := range []string{"FilepathJoin", "FilepathBase", "FilepathDir", "FilepathExt", "FilepathClean", "FilepathIsAbs", "FilepathSplit", "FilepathRel"} {
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

var sinkBool bool

// benchString is a body of one path argument against its Go control.
func benchString(b *testing.B, body, control func(string) string) {
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			sinkString = body(benchPaths[i%len(benchPaths)])
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			sinkString = control(benchPaths[i%len(benchPaths)])
		}
	})
}

func BenchmarkFilepathBase(b *testing.B) { benchString(b, FilepathBase, filepath.Base) }

func BenchmarkFilepathDir(b *testing.B) { benchString(b, FilepathDir, filepath.Dir) }

func BenchmarkFilepathExt(b *testing.B) { benchString(b, FilepathExt, filepath.Ext) }

func BenchmarkFilepathIsAbs(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		for i := range b.N {
			sinkBool = FilepathIsAbs(benchPaths[i%len(benchPaths)])
		}
	})
	b.Run("go", func(b *testing.B) {
		for i := range b.N {
			sinkBool = filepath.IsAbs(benchPaths[i%len(benchPaths)])
		}
	})
}

// BenchmarkFilepathSplit: the body returns both parts from one scan, as the
// control does (round 1 bound Split to two bodies, one scan each: +47%).
func BenchmarkFilepathSplit(b *testing.B) {
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			p := FilepathSplit(benchPaths[i%len(benchPaths)])
			sinkString = p.Dir + p.File[:0]
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			d, f := filepath.Split(benchPaths[i%len(benchPaths)])
			sinkString = d + f[:0]
		}
	})
}

// TestFilepathBodiesHaveBenchmarks: every Filepath body of path.go has a
// benchmark pair against its Go function in this file (std-align S3a: one
// per function).
func TestFilepathBodiesHaveBenchmarks(t *testing.T) {
	decls, err := sourceDecls()
	if err != nil {
		t.Fatal(err)
	}
	test, err := os.ReadFile("path_test.go")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for name, d := range decls {
		if d.fn == nil || d.fn.Recv != nil || !strings.HasPrefix(name, "Filepath") {
			continue
		}
		n++
		if !strings.Contains(string(test), "\nfunc Benchmark"+name+"(b *testing.B)") {
			t.Errorf("the body %s has no Benchmark%s against its Go function", name, name)
		}
	}
	if n != 8 {
		t.Errorf("%d Filepath bodies; want the eight of std filepath", n)
	}
}

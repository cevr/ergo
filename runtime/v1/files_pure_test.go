package v1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// TestPureBodiesKeepTheAllowlist is the property of the purity fact over
// the embedded runtime files (docs/spec/stdlib.md, Pure bodies): every
// exported function marked //ergo:pure lives in a file that imports only
// PureImports, takes no hidden argument, and BodyOf reports it Pure. The
// file walk here is independent of parseDecls, so it holds BodyOf to the
// rule rather than to itself. Each std text body (G5a) is marked.
func TestPureBodiesKeepTheAllowlist(t *testing.T) {
	names, err := fs.Glob(Source, "*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var marked []string
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(Source, name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, data, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Doc == nil || !slices.ContainsFunc(fn.Doc.List, func(c *ast.Comment) bool { return c.Text == PureMark }) {
				continue
			}
			marked = append(marked, fn.Name.Name)
			if fn.Recv != nil || !fn.Name.IsExported() {
				t.Errorf("%s: %s marks %s, which is not an exported function", name, PureMark, fn.Name.Name)
				continue
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if _, guarded := PureImportHostMembers[p]; !slices.Contains(PureImports, p) && !guarded {
					t.Errorf("%s: pure %s, but the file imports %s", name, fn.Name.Name, p)
				}
			}
			b, err := BodyOf(fn.Name.Name)
			if err != nil {
				t.Errorf("BodyOf(%s): %v", fn.Name.Name, err)
				continue
			}
			if !b.Pure || len(b.Hidden) > 0 {
				t.Errorf("BodyOf(%s) = %+v, want Pure and no hidden argument", fn.Name.Name, b)
			}
		}
	}
	for _, name := range []string{
		"StringsSplit", "StringsJoin", "StringsHasPrefix", "StringsHasSuffix",
		"StringsTrimSpace", "StringsTrimPrefix", "StringsTrimSuffix", "StringsFields",
		"StringsRepeat", "StringsToLower", "StringsToUpper", "StringsIndex",
		"StringsContains", "StringsSlice", "StrconvItoa", "StrconvAtoi", "StrconvQuote",
		"TimeFormat", "TimeParse", "TimeSub", "TimeSubMono",
		"Append", "Get", "Delete", "Put", // the prelude's collection intrinsics (lw-pure-prelude)
	} {
		if !slices.Contains(marked, name) {
			t.Errorf("the std text body %s is not marked %s", name, PureMark)
		}
	}
	// A body that is not marked is not pure, whatever its file imports.
	if b, err := BodyOf("WithCapacity"); err != nil || b.Pure {
		t.Errorf("BodyOf(WithCapacity) = %+v, %v; want not Pure", b, err)
	}
}

// TestPureRefusals feeds marked bodies that break the rules through the
// same reader: the mark is refused, never dropped.
func TestPureRefusals(t *testing.T) {
	host := `package v1

import "os"

// Getenv reads the host.
//
//ergo:pure
func Getenv(k string) string { return os.Getenv(k) }

// Args is marked host.
//
//ergo:host
func Args() []string { return os.Args }
`
	text := `package v1

import "strings"

type Scope struct{}
type Site string
type Box struct{ N int }

var counter int

func helper(s string) string { return s }

//ergo:pure
func Trim(s string) string { return strings.TrimSpace(s) }

//ergo:pure
func Wrap(s string) Box { return Box{N: len(s)} }

//ergo:pure
func (b Box) Len() int { return b.N }

//ergo:pure
func Framed(s *Scope, n int) int { return n }

//ergo:pure
func Sited(at Site) {}

//ergo:pure
func Takes(f func()) {}

//ergo:pure
func Calls(s string) string { return helper(s) }

//ergo:pure
func Reads() int { return counter }

//ergo:pure
func Spawns() { go func() {}() }

//ergo:pure
func Sends(c chan int) { c <- 1 }

//ergo:pure
func Receives(c chan int) int { return <-c }
`
	// A file that imports time (PureImportHostMembers): a body that
	// computes is pure; one that reads the clock, a timer or the zone is
	// refused, by a package member or a method of that name.
	clock := `package v1

import "time"

//ergo:pure
func Fmt(sec int64) string { return time.Unix(sec, 0).UTC().Format(time.RFC3339) }

//ergo:pure
func Clock() int64 { return time.Now().Unix() }

//ergo:pure
func Zoned(sec int64) string { return time.Unix(sec, 0).Local().String() }

//ergo:pure
func Zone() *time.Location { return time.Local }

//ergo:pure
func Abbrev(s string) int64 { t, _ := time.Parse("MST", s); return t.Unix() }

//ergo:pure
func Waits() { time.Sleep(1) }
`
	decls, err := parseDecls(fstest.MapFS{"host.go": {Data: []byte(host)}, "text.go": {Data: []byte(text)}, "clock.go": {Data: []byte(clock)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Trim", "Wrap", "Fmt"} {
		if b, err := decls.body(name); err != nil || !b.Pure {
			t.Errorf("%s: %+v, %v; want Pure", name, b, err)
		}
	}
	if b, err := decls.body("Args"); err != nil || b.Pure || !b.Host {
		t.Errorf("Args: %+v, %v; want Host, not Pure", b, err)
	}
	bad := map[string]string{
		"Getenv":   "its file imports os",
		"Box.Len":  "it is a method",
		"Framed":   "hidden frame",
		"Sited":    "hidden site",
		"Takes":    "parameter 1 is a function",
		"Calls":    "names helper",
		"Reads":    "names counter",
		"Spawns":   "goroutine",
		"Sends":    "channel",
		"Receives": "channel",
		"Clock":    "names Now",
		"Zoned":    "names Local",
		"Zone":     "names Local",
		"Abbrev":   "names Parse",
		"Waits":    "names Sleep",
	}
	// time stays out of the user extern list (check, G9).
	if slices.Contains(PureImports, "time") {
		t.Error("PureImports lists time; a user extern would bind time.Now")
	}
	for name, msg := range bad {
		_, err := decls.body(name)
		if err == nil || !strings.Contains(err.Error(), msg) || !strings.Contains(err.Error(), PureMark) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
}

// TestHostMarkRefusals holds the host mark (fu4c; docs/spec/stdlib.md,
// Host packages 7): BodyOf refuses an unmarked body that reaches a host
// member through a helper function, a package variable or math/rand/v2's
// global source (whose import name is rand, not v2), and a body marked
// both pure and host; a marked body is Host, and a draw from a program's
// own stream is not host.
func TestHostMarkRefusals(t *testing.T) {
	src := `package v1

import (
	"math/rand/v2"
	"os"
)

var stdout = os.Stdout

func helper() string { return os.Getenv("X") }

func Helped() string { return helper() }

func Held() any { return stdout }

func Draws() uint64 { return rand.Uint64() }

func Seeded(seed uint64) uint64 { return rand.New(rand.NewPCG(seed, 1)).Uint64() }

//ergo:host
func Marked() string { return helper() }

//ergo:pure
//ergo:host
func Both() int { return 1 }
`
	decls, err := parseDecls(fstest.MapFS{"a.go": {Data: []byte(src)}})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := decls.body("Seeded"); err != nil || b.Host {
		t.Errorf("Seeded: %+v, %v; want no error, not Host", b, err)
	}
	if b, err := decls.body("Marked"); err != nil || !b.Host {
		t.Errorf("Marked: %+v, %v; want Host", b, err)
	}
	for name, msg := range map[string]string{
		"Helped": "reaches host state (os.Getenv) but is not marked //ergo:host",
		"Held":   "reaches host state (os.Stdout) but is not marked //ergo:host",
		"Draws":  "reaches host state (math/rand/v2.Uint64) but is not marked //ergo:host",
		"Both":   "is marked both //ergo:pure and //ergo:host",
	} {
		if _, err := decls.body(name); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, msg)
		}
	}
}

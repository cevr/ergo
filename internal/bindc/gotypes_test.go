package bindc

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/importer"
	goparser "go/parser"
	gotoken "go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const puregoPath = "github.com/ebitengine/purego"

// stubImporter imports the purego stub of testdata/puregostub for
// purego, and the standard library from source.
type stubImporter struct {
	std  types.Importer
	stub *types.Package
}

func (s stubImporter) Import(path string) (*types.Package, error) {
	if path == puregoPath {
		return s.stub, nil
	}
	return s.std.Import(path)
}

// checkGoPackage type-checks one Go package from its files' sources, with
// the gc sizes of arch and the purego stub.
func checkGoPackage(t *testing.T, std types.Importer, path, arch string, srcs map[string][]byte) error {
	t.Helper()
	fset := gotoken.NewFileSet()
	sizes := types.SizesFor("gc", arch)
	stubSrc := readInput(t, "testdata/puregostub/purego.go")
	sf, err := goparser.ParseFile(fset, "purego.go", stubSrc, goparser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	stub, err := (&types.Config{Sizes: sizes}).Check(puregoPath, fset, []*ast.File{sf}, nil)
	if err != nil {
		t.Fatalf("the purego stub does not type-check: %v", err)
	}
	var files []*ast.File
	for name, src := range srcs {
		f, err := goparser.ParseFile(fset, name, src, goparser.ParseComments)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	var errs []string
	conf := types.Config{Importer: stubImporter{std: std, stub: stub}, Sizes: sizes,
		Error: func(err error) {
			if len(errs) < 10 {
				errs = append(errs, err.Error())
			}
		}}
	if _, err := conf.Check(path, fset, files, nil); err != nil {
		return &goTypesError{errs}
	}
	return nil
}

type goTypesError struct{ errs []string }

func (e *goTypesError) Error() string { return strings.Join(e.errs, "\n") }

// goldenGoFiles reads the golden Go package of a header.
func goldenGoFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "go", "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden Go files in %s/go (%v)", dir, err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = src
	}
	return out
}

// TestGeneratedGoTypechecks: both golden Go packages are gofmt-clean and
// type-check with go/types against the purego stub, with the gc sizes of
// amd64 and of arm64, so their compile-time layout asserts hold on both.
// A control moves one size assert by one byte and must fail on both.
func TestGeneratedGoTypechecks(t *testing.T) {
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			b := bindHeader(t, h.dir, h.header, m)
			srcs := goldenGoFiles(t, h.dir)
			for name, src := range srcs {
				formatted, err := format.Source(src)
				if err != nil || !bytes.Equal(formatted, src) {
					t.Errorf("%s/go/%s is not gofmt-clean (%v)", h.dir, name, err)
				}
				if !bytes.Contains(src, []byte("//go:build (linux || darwin) && (amd64 || arm64)\n")) {
					t.Errorf("%s/go/%s has no build constraint for the tier-1 targets", h.dir, name)
				}
			}
			for _, arch := range []string{"amd64", "arm64"} {
				if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, arch, srcs); err != nil {
					t.Errorf("%s/go on %s:\n%v", h.dir, arch, err)
				}
				// The control: one struct size one byte off.
				bad, n := moveSizeAssert(srcs)
				if n == "" {
					t.Fatalf("%s/go has no size assert", h.dir)
				}
				if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, arch, bad); err == nil {
					t.Errorf("%s/go on %s: the size assert of %s, one byte off, type-checks", h.dir, arch, n)
				}
			}
		})
	}
}

var sizeAssert = regexp.MustCompile(`unsafe\.Sizeof\((\w+)\{\}\) - (\d+) \+ 1\]`)

// moveSizeAssert adds one to the first struct size assert it finds.
func moveSizeAssert(srcs map[string][]byte) (map[string][]byte, string) {
	out := map[string][]byte{}
	found := ""
	for name, src := range srcs {
		out[name] = src
		if found != "" {
			continue
		}
		if m := sizeAssert.FindSubmatchIndex(src); m != nil {
			n, _ := strconv.Atoi(string(src[m[4]:m[5]]))
			out[name] = append(append(append([]byte{}, src[:m[4]]...), strconv.Itoa(n+1)...), src[m[5]:]...)
			found = string(src[m[2]:m[3]])
		}
	}
	return out, found
}

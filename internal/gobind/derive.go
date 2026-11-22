package gobind

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/internal/gofacts"
)

// Derived foreign facts (LAWFFI LF0; docs/spec/interop.md, Foreign facts).
// The pinned Go toolchain's escape analysis already decides, for each
// parameter of a Go body it compiles, whether the function writes through
// it, keeps it, returns a part of it or calls it: Go's own zero-copy
// []byte(s) conversion rests on the same verdict. `ergo bind go` compiles
// each bound package with
//
//	-gcflags='-m -d=escapemutationscalls=1'
//
// and reads one line form, `<pos>: p does not escape, mutate, or call`, as
// the fact `reads p`. Every other verdict, and the silence Go keeps about a
// parameter that moves to the heap, is no fact. The debug flag has no
// compatibility promise, so one function (ParseEscape) reads its text, a
// line it cannot read is no fact, and the bindings are regenerated with the
// toolchain (TestGoStdBindingsMatchToolchain).
//
// Prior art: Go's own `[]byte(s)` copy elision (escape analysis, since Go
// 1.22) and `#cgo noescape`, which asserts the same fact for C with no
// check; MoonBit's `#borrow` parameter attribute.

// EscapeFlags are the compiler flags whose output Derive reads.
const EscapeFlags = "-m -d=escapemutationscalls=1"

// readsVerdict is the one escape line that is the `reads` fact.
const readsVerdict = " does not escape, mutate, or call"

// Facts are derived facts by extern target (`sort.SearchInts`,
// `sort.IntSlice.Search`): the Go names of the parameters, a method's
// receiver included, that the function only reads, and nonNilResult for
// a (*T, error) function whose pointer is non-nil when its error is nil
// (nonnil.go).
type Facts map[string]map[string]bool

// escapeLine is one line of the compiler's -m output.
var escapeLine = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^(.+\.go):(\d+):(\d+): (.*)$`) })

// srcPos is a position in a Go file, by its absolute name.
type srcPos struct {
	file      string
	line, col int
}

// ParseEscape reads the compiler's -m output: for each position it names,
// whether a verdict there is the `reads` line (reads) and whether any other
// verdict names it (other). A relative file name is relative to dir.
func ParseEscape(out, dir string) (reads, other map[srcPos]bool) {
	reads, other = map[srcPos]bool{}, map[srcPos]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := escapeLine().FindStringSubmatch(line)
		if m == nil {
			continue
		}
		l, err1 := strconv.Atoi(m[2])
		c, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		p := srcPos{filepath.Clean(file), l, c}
		msg := m[4]
		if name, ok := strings.CutSuffix(msg, readsVerdict); ok && isIdent(name) {
			reads[p] = true
			continue
		}
		other[p] = true
	}
	return reads, other
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// listedSource is go list's view of one package's compiled Go files.
type listedSource struct {
	ImportPath, Dir, Export string
	GoFiles                 []string
	ImportMap               map[string]string
	DepOnly                 bool
	Error                   *struct{ Err string }
}

// paramFacts walks the Go files of the package path and gives each named
// parameter of a non-generic function or method its verdict: reads when
// the escape output has the `reads` line at the parameter's name and no
// other line there. It returns the parsed files.
func paramFacts(fset *token.FileSet, path, dir string, files []string, reads, other map[srcPos]bool, out Facts) ([]*ast.File, error) {
	var parsed []*ast.File
	for _, name := range files {
		file := filepath.Clean(filepath.Join(dir, name))
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, f)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Type.TypeParams != nil {
				continue
			}
			target := path + "." + fd.Name.Name
			var fields []*ast.Field
			if fd.Recv != nil {
				rt := recvTypeName(fd.Recv.List[0].Type)
				if rt == "" {
					continue // a generic receiver, T[P]
				}
				target = path + "." + rt + "." + fd.Name.Name
				fields = append(fields, fd.Recv.List...)
			}
			fields = append(fields, fd.Type.Params.List...)
			for _, field := range fields {
				for _, id := range field.Names {
					p := fset.Position(id.Pos())
					at := srcPos{file, p.Line, p.Column}
					verdicts := out[target]
					if verdicts == nil {
						verdicts = map[string]bool{}
						out[target] = verdicts
					}
					if old, seen := verdicts[id.Name]; seen && !old {
						continue
					}
					verdicts[id.Name] = reads[at] && !other[at]
				}
			}
		}
	}
	return parsed, nil
}

// recvTypeName is T for a receiver T or *T, "" for a generic T[P].
func recvTypeName(x ast.Expr) string {
	if s, ok := x.(*ast.StarExpr); ok {
		x = s.X
	}
	if p, ok := x.(*ast.ParenExpr); ok {
		return recvTypeName(p.X)
	}
	if id, ok := x.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// goEnv is the go command's environment for platform p, as Load sets it.
func goEnv(env []string, p Platform) []string {
	flags := []string{"-mod=readonly"}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = []string{"-mod=readonly"}
			for _, f := range strings.Fields(v) {
				if !strings.HasPrefix(f, "-mod=") && !strings.HasPrefix(f, "--mod=") {
					flags = append(flags, f)
				}
			}
		}
	}
	return append(append([]string{}, env...), "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED=0", "GOFLAGS="+strings.Join(flags, " "))
}

// Derive compiles the packages that patterns name for platform p with
// EscapeFlags and returns their derived facts. It runs go list and go
// build in dir with env, as Load does; the build cache replays the
// compiler's output, so a second run compiles nothing.
func Derive(dir string, env []string, p Platform, patterns ...string) (Facts, error) {
	e := goEnv(env, p)
	run := func(args ...string) ([]byte, []byte, error) {
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Env = dir, e
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	}
	listed, stderr, err := run(append([]string{"list", "-deps", "-export", "-json=ImportPath,Dir,Export,GoFiles,ImportMap,DepOnly,Error"}, patterns...)...)
	if err != nil {
		return nil, fmt.Errorf("go list for %s: %v: %s", p, err, bytes.TrimSpace(stderr))
	}
	_, out, err := run(append([]string{"build", "-gcflags=" + EscapeFlags}, patterns...)...)
	if err != nil {
		return nil, fmt.Errorf("go build -gcflags=%q for %s: %v: %s", EscapeFlags, p, err, firstLines(out, 5))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	reads, other := ParseEscape(string(out), abs)
	facts := Facts{}
	var pkgs []nnPkg
	var roots []listedSource
	exports := map[string]string{}
	fset := token.NewFileSet()
	dec := json.NewDecoder(bytes.NewReader(listed))
	for {
		var l listedSource
		if err := dec.Decode(&l); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go list for %s: %v", p, err)
		}
		if l.Error != nil {
			return nil, fmt.Errorf("go list for %s: %s: %s", p, l.ImportPath, l.Error.Err)
		}
		exports[l.ImportPath] = l.Export
		if !l.DepOnly {
			roots = append(roots, l)
		}
	}
	imp := exportImporter(fset, exports)
	var checked []*totalPkg
	for _, l := range roots {
		files, err := paramFacts(fset, l.ImportPath, l.Dir, l.GoFiles, reads, other, facts)
		if err != nil {
			return nil, fmt.Errorf("%s for %s: %v", l.ImportPath, p, err)
		}
		pkgs = append(pkgs, nnPkg{l.ImportPath, files})
		if c := checkSource(fset, l.ImportPath, files, l.ImportMap, p.GOARCH, imp); c != nil {
			checked = append(checked, c)
		}
	}
	nonNil(pkgs, facts)
	total(checked, facts)
	return facts, nil
}

func firstLines(b []byte, n int) string {
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// meet keeps a fact only where every platform's facts hold it: a function
// with an assembly body on one platform and a Go body on another has the
// verdict of the weaker (LAWFFI risk 4).
func meet(all []Facts) Facts {
	if len(all) == 0 {
		return nil
	}
	out := Facts{}
	for target, first := range all[0] {
		for name, ok := range first {
			for _, f := range all[1:] {
				ok = ok && f[target][name]
			}
			if ok {
				if out[target] == nil {
					out[target] = map[string]bool{}
				}
				out[target][name] = true
			}
		}
	}
	return out
}

// reads sets d.Reads from facts: the Ergo binders of d's readable
// parameters whose Go name the facts hold, and d.Vouched from the reviewed
// table (gofacts.Rows) for a readable parameter the facts do not hold.
func (d *Decl) reads(path string, facts Facts) {
	f := facts[d.Target(path)]
	d.Reads, d.Vouched = nil, nil
	d.Total = (d.Kind == Func || d.Kind == Method) && f[totalFact]
	rows := gofacts.Of(d.Target(path))
	for _, r := range d.readable {
		if f[r.goName] {
			d.Reads = append(d.Reads, r.ergoName)
			continue
		}
		for _, row := range rows {
			if row.Fact == "reads" && row.Param == r.goName {
				d.Vouched = append(d.Vouched, Vouch{Param: r.ergoName, Reason: row.Reason})
			}
		}
	}
}

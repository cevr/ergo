package lower

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The G1 repro (batch 11, lane EX): a program of two files that calls
// flag.Describe, a std function with a match in a std package of several
// files, lowers, and the match's no-case defect names the match in its std
// file, not a line of the program.
func TestStdBodyInTwoFileProgram(t *testing.T) {
	u := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/g1/main.ergo", "func main() needs Console {\n\tConsole.Println(Usage())\n}\n"},
		{"/ergo-test/g1/usage.ergo", "import flag\n\nfunc Usage() string {\n\tflag.Describe(flag.Missing{Name: \"x\"})\n}\n"},
	}, nil)
	o := mustLowerUnit(t, "g1", u, false)
	want := "no case of the match at " + stdMatchSites(t, "flag", "Describe")[0] + " matched"
	if !strings.Contains(goText(o), want) {
		t.Fatalf("the lowering names no %q:\n%s", want, noCaseLines(goText(o)))
	}
}

// A program of one file that calls a std body with a match names the
// match in its std file. Before G1, sourceOf gave every node of a one-file
// program the program's file, so the defect named a line of main.ergo.
func TestStdBodyInOneFileProgram(t *testing.T) {
	u := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/g1one/main.ergo", "import flag\n\nfunc main() needs Console {\n\tConsole.Println(flag.Describe(flag.Missing{Name: \"x\"}))\n}\n"},
	}, nil)
	o := mustLowerUnit(t, "g1one", u, false)
	want := "no case of the match at " + stdMatchSites(t, "flag", "Describe")[0] + " matched"
	if !strings.Contains(goText(o), want) {
		t.Fatalf("the lowering names no %q:\n%s", want, noCaseLines(goText(o)))
	}
}

// matchStmt reports a match statement: a type match or a value match.
func matchStmt(n syntax.Node) bool {
	switch n.(type) {
	case *syntax.TypeSwitchStmt, *syntax.SwitchStmt:
		return true
	}
	return false
}

// stdFilePath is the path a location names for a file of the std package
// p (sources.go, locate).
func stdFilePath(p *check.Package, f *syntax.File) string {
	if p.Path == std.PreludePath {
		return "std/" + f.Name
	}
	return "std/" + p.Path + "/" + f.Name
}

// stdMatchSites are the locations, path:line:column, of the matches in the
// bodies of the functions named name of the std package path, in source
// order.
func stdMatchSites(t *testing.T, path, name string) []string {
	t.Helper()
	p, err := check.Std.Import(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, fn := range p.Funcs {
		if fn.Name != name || fn.Decl == nil || fn.Decl.Body == nil {
			continue
		}
		out = append(out, funcMatchSites(p, fn)...)
	}
	if len(out) == 0 {
		t.Fatalf("std %s.%s has no match", path, name)
	}
	return out
}

// funcMatchSites are the locations of the matches in fn's body.
func funcMatchSites(p *check.Package, fn *check.Func) []string {
	var file *syntax.File
	for _, f := range p.Files() {
		syntax.Inspect(f, func(n syntax.Node) bool {
			if n == fn.Decl {
				file = f
			}
			return file == nil
		})
	}
	if file == nil {
		return nil
	}
	var out []string
	syntax.Inspect(fn.Decl.Body, func(n syntax.Node) bool {
		if n != nil && matchStmt(n) {
			line, col := file.Position(n.Pos())
			out = append(out, stdFilePath(p, file)+":"+strconv.Itoa(line)+":"+strconv.Itoa(col))
		}
		return true
	})
	return out
}

// goText is the emitted Go of a lowered program, its main package's files.
func goText(o *lowered) string {
	var b strings.Builder
	for _, f := range o.files {
		if strings.HasSuffix(f.Path, ".go") && !strings.Contains(f.Path, "/") {
			b.Write(f.Data)
		}
	}
	return b.String()
}

var qualifierRE = regexp.MustCompile(`\b([a-z][a-z0-9]*)\.[A-Z]`)

var noCaseRE = regexp.MustCompile(`no case of the match at ([^ "]+) matched`)

// noCaseLines are the no-case defects of a lowered program, for a failure.
func noCaseLines(src string) string {
	return strings.Join(noCaseRE.FindAllString(src, -1), "\n")
}

// stdMatchFunc is an exported std function whose body holds a match.
type stdMatchFunc struct {
	pkg *check.Package
	fn  *check.Func
}

// stdMatchFuncs enumerates every exported function of the typed std
// packages whose body holds a match: the cases of the property, read from
// the checked packages, so a new std function with a match joins it.
func stdMatchFuncs(t *testing.T) []stdMatchFunc {
	t.Helper()
	var out []stdMatchFunc
	for _, path := range std.Packages() {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range p.Funcs {
			if fn.Decl == nil || fn.Decl.Body == nil || !isUpper(fn.Name) {
				continue
			}
			has := false
			syntax.Inspect(fn.Decl.Body, func(n syntax.Node) bool {
				has = has || n != nil && matchStmt(n)
				return !has
			})
			if has {
				out = append(out, stdMatchFunc{p, fn})
			}
		}
	}
	return out
}

func isUpper(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

// useSource is an Ergo function use that takes fn's parameters and calls
// fn on them, with fn's type parameters, results and rows: the call that
// makes the lowering lower fn's body. When method, fn is called on its
// first parameter, x.f(…), which a name the package declares more than
// once (an overload on its first parameter's type) needs.
func useSource(c stdMatchFunc, method bool) string {
	sig := c.fn.Sig
	var b strings.Builder
	b.WriteString("func use")
	if len(sig.TParams) > 0 {
		names := make([]string, len(sig.TParams))
		for i, tp := range sig.TParams {
			names[i] = tp.Name
		}
		b.WriteString("[" + strings.Join(names, ", ") + "]")
	}
	params, args := make([]string, len(sig.Params)), make([]string, len(sig.Params))
	for i, p := range sig.Params {
		args[i] = "p" + strconv.Itoa(i)
		params[i] = args[i] + " " + p.Type.String()
	}
	b.WriteString("(" + strings.Join(params, ", ") + ")")
	// The rest of the signature as the checker spells it: results, fails
	// and needs.
	// The parameter list ends at the ")" that closes its "(": a parameter
	// of function type has parentheses of its own (actor.StartMachine's
	// sink func(O) needs R).
	text := sig.String()
	end, depth := 0, 0
	for i, r := range text {
		if r == '(' {
			depth++
		} else if r == ')' {
			if depth--; depth == 0 {
				end = i
				break
			}
		}
	}
	b.WriteString(text[end+1:])
	call := c.pkg.Name + "." + c.fn.Name + "(" + strings.Join(args, ", ") + ")"
	if len(args) > 0 && method {
		call = args[0] + "." + c.fn.Name + "(" + strings.Join(args[1:], ", ") + ")"
	}
	if sig.Errors.String() != "" {
		call = "try " + call
	}
	b.WriteString(" {\n\t" + call + "\n}\n")
	return b.String()
}

// programSources are the files of a program of files files that calls c
// from the file at, as a package function or, when method, on its first
// parameter.
func programSources(c stdMatchFunc, files, at int, method bool) []pkgSource {
	srcs := make([]pkgSource, files)
	for i := range srcs {
		text := fmt.Sprintf("func filler%d() int {\n\t%d\n}\n", i, i)
		if i == 0 {
			text = "func main() {\n}\n\n" + text
		}
		if i == at {
			use := useSource(c, method)
			imports := "import " + c.pkg.Path + "\n"
			// The packages the signature names (fails os.Exit needs io.Stdout).
			seen := map[string]bool{c.pkg.Path: true}
			for _, m := range qualifierRE.FindAllStringSubmatch(use, -1) {
				if _, ok := std.Package(m[1]); ok && !seen[m[1]] {
					seen[m[1]] = true
					imports += "import " + m[1] + "\n"
				}
			}
			text = imports + "\n" + text + "\n" + use
		}
		srcs[i] = pkgSource{fmt.Sprintf("/ergo-test/g1p/f%d.ergo", i), text}
	}
	return srcs
}

// checkProgram checks srcs as package main; ok is false on a diagnostic.
func checkProgram(t *testing.T, srcs []pkgSource) (Unit, []check.Diagnostic) {
	t.Helper()
	u := Unit{}
	for _, s := range srcs {
		f, err := syntax.Parse(s.text)
		if err != nil {
			t.Fatalf("%s: %v\n%s", s.path, err, s.text)
		}
		f.Name = s.path
		u.Files = append(u.Files, f)
		u.Sources = append(u.Sources, Source{Path: s.path, Text: s.text})
	}
	pkg, ds := check.Check("main", u.Files, pkgImporter{})
	u.Package = pkg
	return u, ds
}

// The property (G1, batch 11): for a program of 1 to 4 files, every
// exported std function with a match, called from a seeded file of the
// program, lowers with no internal error, and every no-case defect of the
// output names a match where its std file or the program holds one.
func TestStdMatchBodiesLowerInAnyProgram(t *testing.T) {
	seed := int64(20261108)
	if s := os.Getenv("ERGO_G1_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	funcs := stdMatchFuncs(t)
	if len(funcs) == 0 {
		t.Fatal("no std function with a match")
	}
	all := allStdMatchSites(t)
	namedAny := map[int]int{}
	for _, c := range funcs {
		for files := 1; files <= 4; files++ {
			name := fmt.Sprintf("%s.%s(%d)/files=%d", c.pkg.Path, c.fn.Name, c.fn.Pos, files)
			at := rng.Intn(files)
			t.Run(name, func(t *testing.T) {
				// The package call, else the call on the first parameter;
				// the checker decides which one names fn.
				srcs := programSources(c, files, at, false)
				u, ds := checkProgram(t, srcs)
				if len(ds) > 0 {
					srcs = programSources(c, files, at, true)
					var ds2 []check.Diagnostic
					if u, ds2 = checkProgram(t, srcs); len(ds2) > 0 {
						t.Fatalf("seed %d: neither call checks: %v; %v\n%s", seed, ds, ds2, srcs[at].text)
					}
				}
				o := mustLowerUnit(t, name, u, false)
				named := map[string]bool{}
				for _, m := range noCaseRE.FindAllStringSubmatch(goText(o), -1) {
					named[m[1]] = true
				}
				for s := range named {
					if !all[s] {
						t.Fatalf("seed %d: a no-case defect names %q, where no match is: the program holds none, and no std file holds one there:\n%s", seed, s, noCaseLines(goText(o)))
					}
					namedAny[files]++
				}
			})
		}
	}
	// The position check is not vacuous: in programs of each file count
	// some std match's no-case defect is named.
	for files := 1; files <= 4; files++ {
		if namedAny[files] == 0 {
			t.Errorf("seed %d: no program of %d files names a std match", seed, files)
		}
	}
}

// allStdMatchSites are the locations of every match in the embedded std
// packages (the prelude holds none): a no-case defect of a program that
// holds no match must name one of them.
func allStdMatchSites(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, path := range std.Packages() {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files() {
			syntax.Inspect(f, func(n syntax.Node) bool {
				if n != nil && matchStmt(n) {
					line, col := f.Position(n.Pos())
					out[stdFilePath(p, f)+":"+strconv.Itoa(line)+":"+strconv.Itoa(col)] = true
				}
				return true
			})
		}
	}
	return out
}

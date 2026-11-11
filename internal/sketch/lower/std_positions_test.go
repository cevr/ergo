package lower

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

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

// stdMatchFuncs enumerates every externally nameable exported function of
// the typed std packages whose body holds a match: the cases of the property, read from
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
			// A method of a private provider (config.reader, for example)
			// cannot be named by this external caller. Its public entry
			// points still lower it and check its match locations.
			if !matchSignatureNameable(fn.Sig) {
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

// matchSignatureNameable checks only written names, not the private fields
// of a public type: the generated external wrapper must name its signature.
func matchSignatureNameable(sig *check.Signature) bool {
	var nameable func(check.Type) bool
	every := func(ts []check.Type) bool {
		return !slices.ContainsFunc(ts, func(t check.Type) bool { return !nameable(t) })
	}
	nameable = func(t check.Type) bool {
		switch t := t.(type) {
		case *check.Named:
			return isUpper(t.Obj.Name) && every(t.TArgs)
		case *check.Slice:
			return nameable(t.Elem)
		case *check.Map:
			return nameable(t.Key) && nameable(t.Elem)
		case *check.Pointer:
			return nameable(t.Elem)
		case *check.Tuple:
			return every(t.Types)
		case *check.Signature:
			for _, p := range t.Params {
				if !nameable(p.Type) {
					return false
				}
			}
			return every(t.Results) && every(t.Errors.Elems()) && every(t.Uses.Elems())
		}
		return true
	}
	return nameable(sig)
}

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
			// The constraint too: maps.GetOr's K is comparable, which a
			// Map key needs.
			names[i] = tp.Name
			if tp.Constraint != nil {
				names[i] += " " + tp.Constraint.String()
			}
		}
		b.WriteString("[" + strings.Join(names, ", ") + "]")
	}
	params, args := make([]string, len(sig.Params)), make([]string, len(sig.Params))
	for i, p := range sig.Params {
		args[i] = "p" + strconv.Itoa(i)
		params[i] = args[i] + " " + p.Type.String()
		// An escaping parameter (actor.StartDurable's save) takes an
		// escaping argument, so use's parameter is escaping too.
		if s, ok := p.Type.(*check.Signature); ok && !s.NonEscaping {
			params[i] = args[i] + " escaping " + p.Type.String()
		}
	}
	if check.TakesWorkSend(c.fn) {
		return workUseSource(c, params, args)
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
	name := c.fn.Name
	if sig.NBracket > 0 {
		var targs []string
		for _, tp := range sig.TParams[:sig.NBracket] {
			targs = append(targs, tp.Name)
		}
		name += "[" + strings.Join(targs, ", ") + "]"
	}
	call := c.pkg.Name + "." + name + "(" + strings.Join(args, ", ") + ")"
	if len(args) > 0 && method {
		call = args[0] + "." + c.fn.Name + "(" + strings.Join(args[1:], ", ") + ")"
	}
	if sig.Errors.String() != "" {
		call = "try " + call
	}
	b.WriteString(" {\n\t" + call + "\n}\n")
	if check.TakesDurableMachine(c.fn) {
		return durableUseSource(b.String(), sig.Params[0].Type.String())
	}
	return b.String()
}

// durableUseSource is the use of a durable runner
// (check.TakesDurableMachine) built from plain, its use with the machine
// parameter p0 passed through. The durable rule admits only a machine whose
// step the checker sees, so the call passes a Machine literal whose Step
// names useStep, a pure package function; the rest of the machine is p0's.
// The runner's lowered body does not depend on which step it gets.
func durableUseSource(plain, machine string) string {
	open := strings.Index(machine, "[")
	if open < 0 || !strings.HasPrefix(machine, "Machine[") {
		panic("a durable runner's first parameter is " + machine + ", not a Machine")
	}
	targs := strings.Split(machine[open+1:len(machine)-1], ", ")
	if len(targs) != 6 {
		panic("a durable runner's machine is " + machine + ": want Machine[S, E, O, F, R, W]")
	}
	s, e, o, f, r := targs[0], targs[1], targs[2], targs[3], targs[4]
	lit := machine + "{Initial: p0.Initial, Step: useStep[" + strings.Join(targs[:5], ", ") + "], After: p0.After, Work: p0.Work}"
	use := strings.Replace(plain, "(p0, ", "("+lit+", ", 1)
	step := "func useStep[" + strings.Join(targs[:5], ", ") + "](s " + s + ", e " + e + ") Decision[" + s + ", " + o + "] fails " + f + " needs " + r + " {\n\tIgnore\n}\n"
	return use + "\n" + step
}

// workUseSource is the use of a function that needs the work capability
// (check.TakesWorkSend): the call sits in a Listen work's function, the
// only place the child rule admits it, and the work's send is its first
// argument, so use takes the other parameters and returns the work.
//
// A written row argument has one element (Work[E, R]), so the work's row
// is the needs row's one concrete element, and each row parameter of that
// row is bound to it (R | Scheduler with R = Scheduler is Scheduler). The
// rows are erased in Go, so the lowered body is the same.
func workUseSource(c stdMatchFunc, params, args []string) string {
	sig := c.fn.Sig
	var bound []string
	concrete := ""
	for _, e := range sig.Uses.Elems() {
		if tp, ok := e.(*check.TypeParam); ok {
			bound = append(bound, tp.Name)
		} else if concrete == "" {
			concrete = e.String()
		} else {
			panic(fmt.Sprintf("%s.%s needs %s: a written work row has one element", c.pkg.Path, c.fn.Name, sig.Uses))
		}
	}
	if concrete == "" {
		panic(fmt.Sprintf("%s.%s needs %s: no concrete element to bind its row parameters to", c.pkg.Path, c.fn.Name, sig.Uses))
	}
	bind := func(s string) string {
		for _, n := range bound {
			s = regexp.MustCompile(`\b`+n+`\b`).ReplaceAllString(s, concrete)
		}
		return s
	}
	var names []string
	for _, tp := range sig.TParams {
		if !slices.Contains(bound, tp.Name) {
			names = append(names, tp.Name)
		}
	}
	head := "func use"
	if len(names) > 0 {
		head += "[" + strings.Join(names, ", ") + "]"
	}
	// The work's function captures use's parameters, so a function-typed
	// one is escaping (types.md, Function values 2).
	for i, p := range sig.Params {
		if s, ok := p.Type.(*check.Signature); ok && s.NonEscaping && i > 0 {
			params[i] = args[i] + " escaping " + p.Type.String()
		}
	}
	send := sig.Params[0].Type.(*check.Signature)
	work := "machines.Work[" + send.Params[0].Type.String() + ", " + concrete + "]"
	args[0] = "send"
	return head + "(" + bind(strings.Join(params[1:], ", ")) + ") " + work + " {\n\t" +
		work + "{Listen: func(send " + sig.Params[0].Type.String() + ") needs " + concrete + " {\n\t\tlet _ = " +
		c.pkg.Name + "." + c.fn.Name + "(" + strings.Join(args, ", ") + ")\n\t}}\n}\n"
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
// externally nameable exported std function with a match, called from a seeded file of the
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

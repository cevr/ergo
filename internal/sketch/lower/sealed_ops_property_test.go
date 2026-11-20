package lower

import (
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdOpType is an operation type a std file declares, as source text a
// program can write: its type parameters and its exported members, each
// name the package declares qualified by the package's name.
type stdOpType struct {
	path, pkg, name string
	tparams         string   // "[T any]", or ""
	tpnames         []string // T
	members         []string // exported members, qualified
	skipped         int      // members a program cannot spell (an unexported type)
	imports         []string // the file's import declarations
	resource        bool     // it has Close (R1): Conv takes its value
}

func (o stdOpType) ref(args []string) string {
	q := o.name
	if o.path != stdlib.PreludePath {
		q = o.pkg + "." + o.name
	}
	if len(args) > 0 {
		q += "[" + strings.Join(args, ", ") + "]"
	}
	return q
}

var identRe = regexp.MustCompile(`\.?\b[A-Za-z_][A-Za-z0-9_]*\b`)

// stdOpTypes reads every operation type (an interface type with no union)
// that the prelude and the std packages declare.
func stdOpTypes(t *testing.T) []stdOpType {
	sources := map[string]map[string]string{stdlib.PreludePath: stdlib.Prelude()}
	for _, p := range stdlib.Packages() {
		if files, ok := stdlib.Package(p); ok {
			sources[p] = files
		}
	}
	var out []stdOpType
	for p, files := range sources {
		var parsed []*syntax.File
		declared := map[string]bool{}
		for _, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("std package %s does not parse: %v", p, err)
			}
			parsed = append(parsed, f)
			for _, d := range f.Decls {
				if td, ok := d.(*syntax.TypeDecl); ok {
					declared[td.Name.Name] = true
				}
			}
		}
		pkg := path.Base(p)
		for _, f := range parsed {
			var imports []string
			for _, d := range f.Decls {
				if id, ok := d.(*syntax.ImportDecl); ok {
					imports = append(imports, f.Src[id.Pos():id.End()])
				}
			}
			for _, d := range f.Decls {
				td, ok := d.(*syntax.TypeDecl)
				// A binding's host type is Go's (gostd); a type that names
				// a runtime Go type (STD-NOPRIV NP4c), of runtime/v1 or one
				// of its leaves (runtime/v1/exec), is std's own.
				if !ok || td.Extern != nil && !strings.HasPrefix(td.Extern.Target.Value, runtimeImport+".") && !strings.HasPrefix(td.Extern.Target.Value, runtimeImport+"/") {
					continue
				}
				it, ok := td.Spec.(*syntax.InterfaceType)
				if !ok || len(it.Union) > 0 {
					continue
				}
				o := stdOpType{path: p, pkg: pkg, name: td.Name.Name, imports: append([]string{"import " + p}, imports...)}
				local := map[string]bool{}
				if td.TypeParams != nil {
					for _, fl := range td.TypeParams.List {
						for _, n := range fl.Names {
							local[n.Name] = true
							o.tpnames = append(o.tpnames, n.Name)
						}
					}
				}
				unexported := false
				qualify := func(text string) string {
					if p == stdlib.PreludePath {
						return text
					}
					return identRe.ReplaceAllStringFunc(text, func(id string) string {
						if strings.HasPrefix(id, ".") || local[id] || !declared[id] {
							return id
						}
						if !exportedName(id) {
							unexported = true
						}
						return pkg + "." + id
					})
				}
				if td.TypeParams != nil {
					o.tparams = qualify(f.Src[td.TypeParams.Opening : td.TypeParams.Closing+1])
				}
				for _, inc := range it.Includes {
					// An included operation type is a member as written:
					// `..Closer` is the release (resources R1).
					text := ".." + qualify(f.Src[inc.Type.Pos():inc.Type.End()])
					o.members = append(o.members, text)
					if text == "..Closer" {
						o.resource = true
					}
				}
				for _, m := range it.Methods {
					if len(m.Names) != 1 || !exportedName(m.Names[0].Name) {
						continue
					}
					unexported = false
					text := m.Names[0].Name + qualify(f.Src[m.Names[0].End():m.Type.End()])
					if unexported {
						o.skipped++
						continue
					}
					o.members = append(o.members, text)
					if m.Names[0].Name == "Close" {
						o.resource = true
					}
				}
				out = append(out, o)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path+"."+out[i].name < out[j].path+"."+out[j].name })
	return out
}

// sealedCase is one conversion of a sealed std protocol's value into an
// operation type: its name, its source, and whether the target is the
// program's own (a generated operation type with the source's members),
// which the checker must refuse: std declares none of a program's
// operation types (EXC1, q170). A target std declares is admitted exactly
// when the source includes it. A must case is a sealed operation type the
// program declares, which includes the target: it converts as a plain
// interface would.
type sealedCase struct {
	name, src string
	program   bool
	must      bool
}

// sealedSource is the Go-free program of one conversion: target is the
// operation type's text, decl its declaration when the program declares it.
func sealedSource(s stdOpType, imports []string, decl, target string) string {
	mode := ""
	if s.resource {
		mode = "own "
	}
	body := "\n" + decl + "\nfunc Conv" + s.tparams + "(s " + mode + s.ref(s.tpnames) + ") " + target + " { s }\n"
	var b strings.Builder
	seen := map[string]bool{}
	// s's own imports are the ones its type names, so they are matched
	// without the target: a Go target io.Writer names Go's io, not std io
	// (json.Cursor->writer imported std io beside `import go io`).
	own := decl + s.tparams + s.ref(s.tpnames)
	for k, list := range [][]string{s.imports, imports} {
		names := body
		if k == 0 {
			names = own
		}
		for _, i := range list {
			for _, p := range strings.Fields(strings.TrimPrefix(i, "import")) {
				// Only the packages the program names: an unused import is S110.
				if !seen[p] && regexp.MustCompile(`\b`+path.Base(p)+`\.`).MatchString(names) {
					seen[p] = true
					b.WriteString("import " + p + "\n")
				}
			}
		}
	}
	return b.String() + body
}

// TestSealedStdConvertsToItsOperationTypes is the generated property of
// the conversion of a sealed std operation type's value (check.Sealed, on a std type), whose
// Go form is the runtime's own type, into an operation type: every pair
// the checker admits (the source includes the target, EXC1) lowers to Go
// that type-checks (lower/sealedops.go), and every program operation type
// is refused, since std declares none of them. The pairs are every sealed std protocol of the
// prelude and the std packages crossed with (1) a generated operation type
// of the program with all its exported members, (2) one with each member
// alone, and (3) every other operation type the prelude and std declare,
// instantiated with the source's type parameters where it is generic;
// and a sealed operation type the program declares, crossed with (1) and
// (2): it has no runtime Go type, so it converts as a plain interface
// (check.sealedStd names std types only). Replay one case with ERGO_SEALED_CASE=<name>; a failure prints the
// case's Ergo source and Go.
func TestSealedStdConvertsToItsOperationTypes(t *testing.T) {
	all := stdOpTypes(t)
	only := os.Getenv("ERGO_SEALED_CASE")
	var cases []sealedCase
	sealedSeen := 0
	for _, s := range all {
		if s.path == stdlib.PreludePath && s.name != "Task" && len(s.members) == 0 {
			continue
		}
		probe := lowerSource("probe", strings.Replace(sealedSource(stdOpType{path: s.path, pkg: s.pkg, name: s.name, tparams: s.tparams, tpnames: s.tpnames, imports: s.imports}, nil, "", "int"), " int { s }", " int { 0 }", 1)+"func main() {}\n", false)
		if len(probe.diags) > 0 && probe.pkg == nil {
			continue
		}
		if !sealedParam(probe.pkg) {
			continue
		}
		sealedSeen++
		if s.skipped > 0 {
			t.Logf("%s.%s: %d members name an unexported type and are not generated", s.pkg, s.name, s.skipped)
		}
		gen := func(members []string) string {
			return "type Op" + s.tparams + " {\n" + strings.Join(members, ",\n") + ",\n}\n"
		}
		opRef := "Op"
		if len(s.tpnames) > 0 {
			opRef += "[" + strings.Join(s.tpnames, ", ") + "]"
		}
		// A member whose result is a mapped text is no program's to
		// declare (types.md, Borrows 8: fs.Text is written only as a
		// parameter's type), so its operation type may be refused.
		writable := func(members []string) bool {
			for _, m := range members {
				if _, result, ok := strings.Cut(m, ")"); ok && strings.Contains(result, "fs.Text") {
					return false
				}
			}
			return true
		}
		if len(s.members) > 0 {
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "/all", src: sealedSource(s, nil, gen(s.members), opRef), program: writable(s.members)})
		}
		// A resource (R1: it has Close) converts only to an operation type
		// with a release (R22, releasable): each member goes with Close.
		var release []string
		for _, m := range s.members {
			if strings.HasPrefix(m, "Close(") || m == "..Closer" {
				release = []string{m}
			}
		}
		for _, m := range s.members {
			if len(s.members) == 1 || len(release) > 0 && m == release[0] {
				continue
			}
			name := m
			if i := strings.IndexAny(m, "(["); i >= 0 {
				name = m[:i]
			}
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "/" + name, src: sealedSource(s, nil, gen(append([]string{m}, release...)), opRef), program: writable([]string{m})})
		}
		for _, o := range all {
			if o.path == s.path && o.name == s.name {
				continue
			}
			var args []string
			if len(o.tpnames) > 0 {
				if len(o.tpnames) != len(s.tpnames) {
					continue
				}
				args = s.tpnames
			}
			var imp []string
			if o.path != stdlib.PreludePath {
				imp = []string{"import " + o.path}
			}
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "->" + o.pkg + "." + o.name, src: sealedSource(s, imp, "", o.ref(args))})
		}
	}
	if sealedSeen < 10 {
		t.Fatalf("the property found %d sealed std protocols; the enumeration lost them", sealedSeen)
	}
	// A sealed operation type the program declares (Sealed: an unexported
	// operation) has no runtime Go type: sealing is the visibility rule, so
	// its value converts as a plain interface would (check.sealedStd's Std
	// term; batch-17 round 2 TS-1), and so, as every program operation type
	// under EXC1, only into an operation type it includes: a member subset
	// it does not include is the one refusal of the declared relation.
	user := []string{"Next() int", "Add(n int) int", "Name() string"}
	for i, members := range append([][]string{user}, [][]string{{user[0]}, {user[1]}, {user[2]}}...) {
		name := "main.Counter/all"
		if i > 0 {
			name = "main.Counter/" + members[0][:strings.Index(members[0], "(")]
		}
		own := []string{"..Op"}
		for _, m := range user {
			if !slices.Contains(members, m) {
				own = append(own, m)
			}
		}
		decl := "type Counter {\n" + strings.Join(own, ",\n") + ",\nreset(),\n}\n\ntype C { n int } implements Counter\n\nfunc Next(c C) int {\n\treturn c.n + 1\n}\n\nfunc Add(c C, n int) int {\n\treturn c.n + n\n}\n\nfunc Name(c C) string {\n\treturn \"c\"\n}\n\nfunc reset(c C) {\n}\n\nfunc Make(n int) Counter {\n\tC{n: n}\n}\n"
		cases = append(cases, sealedCase{name: name, src: "\n" + decl + "\ntype Op {\n" + strings.Join(members, ",\n") + ",\n}\n\nfunc Conv(s Counter) Op { s }\n", must: true})
	}
	var programs []*lowered
	admitted := map[string]bool{}
	for _, c := range cases {
		if only != "" && c.name != only {
			continue
		}
		o := lowerSource(c.name, c.src+"func main() {}\n", false)
		switch {
		case o.err != nil:
			t.Errorf("%s: %v\n%s", c.name, o.err, c.src)
		case len(o.diags) > 0:
			if c.must {
				t.Errorf("%s: the checker refuses a conversion into an operation type with the source's own members: %v\n%s\nreplay: ERGO_SEALED_CASE=%q go test ./internal/sketch/lower -run TestSealedStdConvertsToItsOperationTypes", c.name, o.diags, c.src, c.name)
			}
			if c.program && (len(o.diags) != 1 || !strings.Contains(o.diags[0].Message, "does not include")) {
				t.Errorf("%s: want the one refusal of the declared relation, got %v\n%s\nreplay: ERGO_SEALED_CASE=%q go test ./internal/sketch/lower -run TestSealedStdConvertsToItsOperationTypes", c.name, o.diags, c.src, c.name)
			}
		case c.program:
			t.Errorf("%s: a sealed std value converts into the program's own operation type, which std cannot declare\n%s", c.name, c.src)
		case o.panicked != nil:
			t.Errorf("%s: lowering panicked: %v\n%s\nreplay: ERGO_SEALED_CASE=%q", c.name, o.panicked, c.src, c.name)
		case len(o.refused) > 0:
			t.Errorf("%s: refused %+v\n%s", c.name, o.refused, c.src)
		default:
			admitted[c.name] = true
			o.name = strings.NewReplacer(".", "_", "/", "_", "->", "_to_").Replace(c.name)
			programs = append(programs, o)
		}
	}
	// The known pair: a std runtime type into a prelude operation type.
	// json.Text into the Writer was it until LR3, where Text became a
	// provider cell, which converts to the Writer only as encode's operand
	// (types.md, Interface assignability 6); rand.Stream into Random was
	// it until EXC1, where a pair needs the source to include the target
	// and Stream does not include Random. exec.Running includes Closer.
	if only == "" && !admitted["exec.Running->prelude.Closer"] {
		t.Errorf("exec.Running into the prelude's Closer is not among the admitted pairs (%d): the property lost its known pair", len(admitted))
	}
	t.Logf("%d sealed std protocols, %d cases, %d admitted", sealedSeen, len(cases), len(admitted))
	typecheckGo(t, programs)
}

// sealedParam reports whether Conv's parameter is a sealed std protocol.
func sealedParam(p *check.Package) bool {
	if p == nil {
		return false
	}
	for _, o := range p.Exported() {
		if f, ok := o.(*check.Func); ok && f.Name == "Conv" && len(f.Sig.Params) == 1 {
			n, ok := f.Sig.Params[0].Type.(*check.Named)
			if !ok || n.Obj == nil || !n.Obj.Pkg.Std() || !check.Sealed(n) {
				return false
			}
			// A provider cell (json.Text since LR3) is sealed by its
			// declaration too, but its Go form is a cell, not a runtime
			// type, and it converts to no operation type but as encode's
			// writer operand (types.md, Interface assignability 6).
			_, cell := check.CellProvider(n)
			return !cell
		}
	}
	return false
}

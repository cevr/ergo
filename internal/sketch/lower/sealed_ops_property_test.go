package lower

import (
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
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
	if o.path != std.PreludePath {
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
	sources := map[string]map[string]string{std.PreludePath: std.Prelude()}
	for _, p := range std.Packages() {
		if files, ok := std.Package(p); ok {
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
				if !ok || td.Extern != nil {
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
					if p == std.PreludePath {
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
// operation type: its name, its source, and whether the checker must admit
// it (a generated operation type with the source's members) or may refuse
// it (an operation type the std declares, which the member match decides).
type sealedCase struct {
	name, src string
	must      bool
}

// sealedSource is the Go-free program of one conversion: target is the
// operation type's text, decl its declaration when the program declares it.
func sealedSource(s stdOpType, imports []string, decl, target string) string {
	takes := ""
	if s.resource {
		takes = " takes s"
	}
	body := "\n" + decl + "\nfunc Conv" + s.tparams + "(s " + s.ref(s.tpnames) + ") " + target + takes + " { s }\n"
	var b strings.Builder
	seen := map[string]bool{}
	for _, list := range [][]string{s.imports, imports} {
		for _, i := range list {
			for _, p := range strings.Fields(strings.TrimPrefix(i, "import")) {
				// Only the packages the program names: an unused import is S110.
				if !seen[p] && regexp.MustCompile(`\b`+path.Base(p)+`\.`).MatchString(body) {
					seen[p] = true
					b.WriteString("import " + p + "\n")
				}
			}
		}
	}
	return b.String() + body
}

// TestSealedStdConvertsToItsOperationTypes is the generated property of
// the conversion of a sealed std protocol's value (check.SealedStd), whose
// Go form is the runtime's own type, into an operation type: every pair
// the checker's member match admits lowers to Go that type-checks
// (lower/sealedops.go). The pairs are every sealed std protocol of the
// prelude and the std packages crossed with (1) a generated operation type
// of the program with all its exported members, (2) one with each member
// alone, and (3) every other operation type the prelude and std declare,
// instantiated with the source's type parameters where it is generic.
// Replay one case with ERGO_SEALED_CASE=<name>; a failure prints the
// case's Ergo source and Go.
func TestSealedStdConvertsToItsOperationTypes(t *testing.T) {
	all := stdOpTypes(t)
	only := os.Getenv("ERGO_SEALED_CASE")
	var cases []sealedCase
	sealedSeen := 0
	for _, s := range all {
		if s.path == std.PreludePath && s.name != "Task" && len(s.members) == 0 {
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
		if len(s.members) > 0 {
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "/all", src: sealedSource(s, nil, gen(s.members), opRef), must: true})
		}
		// A resource (R1: it has Close) converts only to an operation type
		// with a release (R22, releasable): each member goes with Close.
		var release []string
		for _, m := range s.members {
			if strings.HasPrefix(m, "Close(") {
				release = []string{m}
			}
		}
		for _, m := range s.members {
			if len(s.members) == 1 || len(release) > 0 && m == release[0] {
				continue
			}
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "/" + m[:strings.IndexAny(m, "([")], src: sealedSource(s, nil, gen(append([]string{m}, release...)), opRef), must: true})
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
			if o.path != std.PreludePath {
				imp = []string{"import " + o.path}
			}
			cases = append(cases, sealedCase{name: s.pkg + "." + s.name + "->" + o.pkg + "." + o.name, src: sealedSource(s, imp, "", o.ref(args))})
		}
	}
	if sealedSeen < 10 {
		t.Fatalf("the property found %d sealed std protocols; the enumeration lost them", sealedSeen)
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
	if only == "" && !admitted["json.Text->prelude.Writer"] {
		t.Errorf("json.Text into the prelude's Writer is not among the admitted pairs (%d): the property lost its known pair", len(admitted))
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
			return ok && check.SealedStd(n)
		}
	}
	return false
}

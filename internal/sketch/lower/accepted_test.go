package lower

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A family is a kind of lowered construct: a syntax node type, a call kind,
// a pattern kind, an implicit conversion kind or a basic type of an
// expression.
//
// implementedFamilies are the families lower-v1 stage 2 lowers from Info;
// each needs a witness, a source that lowers whole and compiles.
var implementedFamilies = []string{
	"File", "ImportDecl", "ImportSpec", "TypeDecl", "FuncDecl", "VarDecl", "VarSpec", "WireDecl", "WireBody", "WireEntry",
	"Field", "FieldList", "FuncType", "StructType", "InterfaceType", "VariantSpec", "ErrorRow", "NeedsClause", "ExternSpec",
	"Ident", "BasicLit", "CompositeLit", "KeyValueExpr", "FuncLit", "ParenExpr", "SelectorExpr", "IndexExpr", "SliceExpr",
	"CallExpr", "UnaryExpr", "BinaryExpr", "StarExpr", "TryExpr", "CatchExpr", "CatchArm", "AsPattern", "ArrayType", "MapType",
	"DeclStmt", "ExprStmt", "AssignStmt", "ReturnStmt", "BlockStmt", "IfStmt", "ForStmt", "BranchStmt",
	"TypeSwitchStmt", "SwitchStmt", "CaseClause", "TypeGuard", "InterpolatedString", "RangeStmt", "WireLit",
	"call:Func", "call:Op", "call:ProviderOp", "call:FuncValue", "call:Conversion", "call:Len", "call:With",
	"pattern:Wildcard", "pattern:TypeTest", "pattern:Const", "pattern:As", "pattern:Bind", "pattern:Record", "pattern:Tuple", "TupleExpr",
	"conv:Variant", "conv:Operations", "conv:Row",
	"basic:bool", "basic:string", "basic:int", "basic:int8", "basic:int16", "basic:int32", "basic:int64",
	"basic:uint", "basic:uint8", "basic:uint16", "basic:uint32", "basic:uint64", "basic:float32", "basic:float64", "basic:duration",
}

// refusedFamilies are the families whose lowering waits for a later stage,
// with the owner the refusal names. A source that holds one is refused at
// it, never lowered partly.
var refusedFamilies = map[string]string{
	"MachineEntry": "lower-v1 stage 4", // entries are refused with their enclosing MachineType
	"MachineType":  "lower-v1 stage 4",
	// Narrower families: a form of an implemented node that waits.
	"ExternSpec:user":             "check-v1 stage E",
	"call:Func:externTarget":      "lower-v1",
	"NoValue:generic":             "check-v1 stage E",
	"Task:nested":                 "check-v1 stage E",
	"InterpolatedString:nonbasic": "check-v1 stage E",
	"call:With:imported":          "check-v1 stage B",
	"call:With:context":           "check-v1 stage E",
	"FuncValue:std":               "check-v1 stage E",
	"TypeDecl:errorText":          "lower-v1 stage 3",
	// Stage 3b of #199: the release forms an owning with cannot call by a
	// Go name, and an owning with outside a function body.
	"call:With:release": "check-v1 stage E",
	"call:With:owned":   "check-v1 stage E",
}

// refusalReasons classifies a refusal by its message into the refused
// family it stands at. Every refusal the corpus meets must match one, and
// the family must be a documented refusal with the owner the message names,
// never an implemented family: so a refusal of an implemented construct,
// or a new refusal nobody documented, fails the test.
var refusalReasons = []struct {
	family  string
	pattern *regexp.Regexp
}{
	{"InterpolatedString:nonbasic", regexp.MustCompile(`^an interpolated value of type .+ is lowered after check-v1 stage E`)},
	{"MachineType", regexp.MustCompile(`^machine plan values are lowered in lower-v1 stage 4`)},
	{"ExternSpec:user", regexp.MustCompile(`^a user extern go binding \(here of \S+\) is lowered after check-v1 stage E`)},
	{"call:Func:externTarget", regexp.MustCompile(`^a call to the std function \S+ \(extern go target \S+\) is lowered in lower-v1 \(a runtime/v1 body for \S+\)$`)},
	{"NoValue:generic", regexp.MustCompile(`^(the type .+, a declared generic type whose type argument a block with no value binds,|a type parameter of \S+ that a block with no value binds, inside a function type other than a parameter's own \(func\(…\) T\),) is lowered after check-v1 stage E$`)},
	// A Task held inside another type or through a type argument (stage D3b
	// lowers a Task returned as itself), and the std forms with no
	// runtime/v1 body.
	{"Task:nested", regexp.MustCompile(`^(a call whose result holds a Task|the function \S+ used as a value returns a Task|an operation provided by \S+, whose result holds a Task|a function whose result holds a Task inside another type|a method value of an operation that returns a Task) .*check-v1 stage E`)},
	{"call:With:imported", regexp.MustCompile(`^a with of an imported wire is lowered after check-v1 stage B`)},
	{"call:With:context", regexp.MustCompile(`^(a with of a context value outside a function body|a context value whose \S+ is (generic|an extern function)) is lowered after check-v1 stage E$`)},
	{"FuncValue:std", regexp.MustCompile(`^the std function \S+ used as a value (takes its call's hidden \S+|takes a function|returns a Seq), which is lowered after check-v1 stage E$`)},
	{"TypeDecl:errorText", regexp.MustCompile(`^the text of an error type with a field of .+ is lowered in lower-v1 stage 3`)},
	{"call:With:release", regexp.MustCompile(`^a provider whose Close (is an operation of its type|is generic|is a std or extern function|takes a pointer to its value) is released after check-v1 stage E$`)},
	{"call:With:owned", regexp.MustCompile(`^a with that owns a provider with a Close outside a function body is lowered after check-v1 stage E$`)},
}

// refusalFamily is the refused family a refusal message stands at, or "".
func refusalFamily(message string) string {
	for _, r := range refusalReasons {
		if r.pattern.MatchString(message) {
			return r.family
		}
	}
	return ""
}

// families lists the families in a checked source.
func families(file *syntax.File, info *check.Info) map[string]bool {
	out := map[string]bool{}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if n != nil {
			out[strings.TrimPrefix(fmt.Sprintf("%T", n), "*syntax.")] = true
		}
		return true
	})
	callKinds := []string{"Func", "Op", "ProviderOp", "FuncValue", "Conversion", "Len", "With", "Prelude"}
	for _, c := range info.Calls {
		out["call:"+callKinds[c.Kind]] = true
	}
	patternKinds := map[check.PatternKind]string{check.PatWildcard: "Wildcard", check.PatBind: "Bind", check.PatTypeTest: "TypeTest",
		check.PatRecord: "Record", check.PatConst: "Const", check.PatTuple: "Tuple", check.PatAs: "As"}
	for _, p := range info.Patterns {
		out["pattern:"+patternKinds[p.Kind]] = true
	}
	// case _ of a type match is a clause with no list, which has no
	// Patterns entry.
	syntax.Inspect(file, func(n syntax.Node) bool {
		if s, ok := n.(*syntax.TypeSwitchStmt); ok {
			for _, cl := range s.Clauses {
				if len(cl.List) == 0 {
					out["pattern:Wildcard"] = true
				}
			}
		}
		return true
	})
	convKinds := map[check.ConversionKind]string{check.ConvVariant: "Variant", check.ConvOperations: "Operations", check.ConvRow: "Row"}
	for _, c := range info.Implicit {
		out["conv:"+convKinds[c.Kind]] = true
	}
	for _, t := range info.Types {
		if b, ok := t.(*check.Basic); ok {
			out["basic:"+b.Name] = true
		}
	}
	return out
}

// TestAcceptedNodesLower: every source the checker accepts, from the
// checker's fixtures, the prototype's probes, the live examples, generated
// typed compositions and this package's witness programs, either lowers
// whole, with Go that go vet and go build accept, or is refused at a
// located construct with a refusal naming the stage that owns it. A
// rejected source is counted. No lowering panics, no source meets an
// internal compiler error (a missing Info fact), and no implemented family
// is refused. Every family the accepted sources hold is either implemented,
// with a lowered witness, or a documented refusal, so a family the checker
// newly admits fails here until it is lowered or its refusal is recorded.
// Sources with oracles run against them: the examples' expected output and
// families.out.
func TestAcceptedNodesLower(t *testing.T) {
	type source struct{ name, path, src, want string }
	var sources []source
	clean := regexp.MustCompile(`[^A-Za-z0-9]+`)
	for _, root := range []string{"../check/testdata", "../proto/testdata", "../../../examples", "testdata"} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ergo") {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := source{name: fmt.Sprintf("f%d_%s", len(sources), clean.ReplaceAllString(strings.TrimSuffix(filepath.Base(p), ".ergo"), "_")), path: p, src: string(data)}
			for _, oracle := range []string{strings.TrimSuffix(p, ".ergo") + ".out", filepath.Join(filepath.Dir(p), "expected.out")} {
				if want, err := os.ReadFile(oracle); err == nil && (filepath.Base(p) == "main.ergo" || !strings.HasSuffix(oracle, "expected.out")) {
					s.want = string(want)
					break
				}
			}
			sources = append(sources, s)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		g := &orderGen{rng: rand.New(rand.NewSource(int64(7000 + i)))}
		src, want := g.program(25)
		sources = append(sources, source{name: fmt.Sprintf("g%d_generated", i), path: "generated", src: src, want: want})
	}
	for _, c := range append(rowsCases(), genericCases()...) {
		if c.exit == 0 {
			sources = append(sources, source{name: "w_" + c.name, path: "witness " + c.name, src: c.src, want: c.stdout})
		}
	}
	if len(sources) < 100 {
		t.Fatalf("the domain has %d sources; the fixture directories moved?", len(sources))
	}

	implemented := map[string]bool{}
	for _, f := range implementedFamilies {
		implemented[f] = true
	}
	accepted := map[string]bool{} // families of checker-accepted sources
	refusedAt := map[string]int{} // refusals by the refused family they stand at
	witnessed := map[string]int{} // families of sources that lowered
	refusedBy := map[string]int{} // refusals by owner
	var lowerable, runnable []*lowered
	wants := map[string]string{}
	rejected := 0
	for _, s := range sources {
		entry := s.want != ""
		o := lowerSource(s.name, s.src, entry)
		switch {
		case o.err != nil && o.file == nil:
			rejected++ // a syntax error: the parser's negative fixture
			continue
		case len(o.diags) > 0:
			rejected++
			continue
		}
		fams := families(o.file, o.pkg.Info)
		for f := range fams {
			accepted[f] = true
		}
		switch {
		case o.panicked != nil:
			t.Errorf("%s: lowering panicked: %v", s.path, o.panicked)
		case o.err != nil:
			t.Errorf("%s: %v", s.path, o.err)
		case len(o.refused) > 0:
			for _, r := range o.refused {
				family := refusalFamily(r.Message)
				owner := refusedFamilies[family]
				switch {
				case family == "":
					t.Errorf("%s:%d:%d: an unclassified refusal (add it to refusalReasons with a refused family, or lower the form): %s", s.path, r.Line, r.Column, r.Message)
				case implemented[family]:
					t.Errorf("%s:%d:%d: a refusal of the implemented family %s: %s", s.path, r.Line, r.Column, family, r.Message)
				case owner == "" || !strings.Contains(r.Message, owner):
					t.Errorf("%s:%d:%d: the refusal stands at %s, documented as owned by %q, and names another owner: %s", s.path, r.Line, r.Column, family, owner, r.Message)
				}
				refusedBy[owner]++
				refusedAt[family]++
			}
		default:
			for f := range fams {
				witnessed[f]++
			}
			lowerable = append(lowerable, o)
			if entry {
				runnable = append(runnable, o)
				wants[o.name] = s.want
			}
		}
	}
	for _, f := range implementedFamilies {
		if witnessed[f] == 0 {
			t.Errorf("the implemented family %s has no lowered witness", f)
		}
	}
	var names []string
	for f := range accepted {
		names = append(names, f)
		if !implemented[f] && refusedFamilies[f] == "" {
			t.Errorf("the checker accepts the family %s, which is neither lowered nor a documented refusal", f)
		}
	}
	sort.Strings(names)
	var inventory strings.Builder
	fmt.Fprintf(&inventory, "sources %d: rejected by the checker %d, lowered %d, refused %d\n", len(sources), rejected, len(lowerable), len(sources)-rejected-len(lowerable))
	for _, f := range names {
		status := fmt.Sprintf("lowered, %d witnesses", witnessed[f])
		if !implemented[f] {
			status = "refused: " + refusedFamilies[f]
		}
		fmt.Fprintf(&inventory, "  %-22s %s\n", f, status)
	}
	var owners []string
	for o, n := range refusedBy {
		owners = append(owners, fmt.Sprintf("%s: %d", o, n))
	}
	sort.Strings(owners)
	fmt.Fprintf(&inventory, "refusals by owner: %s\n", strings.Join(owners, ", "))
	var at []string
	for f, n := range refusedAt {
		at = append(at, fmt.Sprintf("%s: %d", f, n))
	}
	sort.Strings(at)
	fmt.Fprintf(&inventory, "refusals by family: %s\n", strings.Join(at, ", "))
	var ran []string
	for _, o := range runnable {
		ran = append(ran, o.name)
	}
	fmt.Fprintf(&inventory, "run against an oracle: %s\n", strings.Join(ran, ", "))
	t.Log("family inventory\n" + inventory.String())

	var toBuild []*lowered
	for _, o := range lowerable {
		if wants[o.name] == "" {
			toBuild = append(toBuild, o)
		}
	}
	buildAll(t, toBuild, false)
	exes := buildAll(t, runnable, true)
	for _, o := range runnable {
		if exes == nil {
			break
		}
		stdout, stderr, code := runExe(t, exes[o.name])
		if code != 0 || stdout != wants[o.name] {
			t.Errorf("%s: exit %d, stderr %q\n%s", o.name, code, firstLines(stderr, 8), diffLines(stdout, wants[o.name]))
		}
	}
}

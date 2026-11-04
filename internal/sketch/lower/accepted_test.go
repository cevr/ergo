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
	"File", "ImportDecl", "ImportSpec", "TypeDecl", "FuncDecl", "VarDecl", "VarSpec", "ContextDecl", "ContextBody", "ContextEntry",
	"Field", "FieldList", "FuncType", "StructType", "InterfaceType", "VariantSpec", "ErrorRow", "NeedsClause", "ExternSpec",
	"Ident", "BasicLit", "CompositeLit", "KeyValueExpr", "FuncLit", "ParenExpr", "SelectorExpr", "IndexExpr", "SliceExpr",
	"CallExpr", "UnaryExpr", "BinaryExpr", "StarExpr", "TryExpr", "CatchExpr", "CatchArm", "AsPattern", "ArrayType", "MapType",
	"DeclStmt", "ExprStmt", "AssignStmt", "ReturnStmt", "BlockStmt", "IfStmt", "ForStmt", "BranchStmt",
	"TypeSwitchStmt", "SwitchStmt", "CaseClause", "TypeGuard", "InterpolatedString", "RangeStmt", "ContextLit",
	"call:Func", "call:Op", "call:ProviderOp", "call:FuncValue", "call:Conversion", "call:Len", "call:With",
	"pattern:Wildcard", "pattern:TypeTest", "pattern:Const", "pattern:As", "pattern:Bind", "pattern:Record", "pattern:Tuple", "TupleExpr",
	"conv:Variant", "conv:Operations", "conv:Row",
	"basic:bool", "basic:string", "basic:int", "basic:int8", "basic:int16", "basic:int32", "basic:int64",
	"basic:uint", "basic:uint8", "basic:uint16", "basic:uint32", "basic:uint64", "basic:float32", "basic:float64", "basic:duration",
	// A test and its asserts lower in the test build (LowerTests, swept by
	// stage_e3a_test.go); the run build this test lowers skips them whole.
	"TestDecl", "AssertStmt",
	// A law is checked, not run (#162): neither build lowers it, so a
	// source that holds one lowers whole without it (stage E3b-2).
	"LawDecl", "call:LawOp",
	// A machine declaration lowers to a plan value (lower-v1 stage 4,
	// machine.go; TestMachinePlanValues).
	"MachineType", "MachineEntry",
}

// refusedFamilies are the families whose lowering waits for a later stage,
// with the owner the refusal names. A source that holds one is refused at
// it, never lowered partly.
var refusedFamilies = map[string]string{
	// Narrower families: a form of an implemented node that waits.
	"call:With:imported": "196b",
	"TypeDecl:errorText": "lower-v1 stage 3",
	// Stage 4 of lower-v1 removed the family of a call of a std function
	// with no runtime/v1 body (timeout.After): v1.After takes the caller's
	// Scheduler as a hidden argument (Q-S4-7; TestTimeoutAfterLowers), and
	// a std extern with no body is an internal error
	// (TestStdExternsHaveBodies).
	// Stage 4 of lower-v1 removed the family of a type argument that a
	// block with no value binds, in a declared generic type or inside
	// another function type (C11): a function value with no result has the
	// one Go shape func(…) (struct{}, error) (types.go, funcType;
	// TestNoValueTypeArgumentsLower).
	// Stage E6b of check-v1 removed the family of a context value whose
	// protocol function is generic: the checker records its instance
	// (ContextBinding.KeyInst, TextInst) and the lowering calls it
	// (stage_e6b_test.go).
	// Stage E6a of check-v1 removed the family of an interpolated value of
	// a type that is not basic: the checker refuses a value with no text
	// form (D-E6), and a value of a type defined over a basic type lowers
	// (stage_e6a_test.go).
	// Stage E4 of check-v1 removed three families: a std function used as a
	// value, an owning with's release that no Go name calls, and a user
	// extern. An extern is call-only (C25, P3), the checker refuses those
	// releases (C29), and a user extern in its checked domain lowers as a
	// direct Go call (C26). The lowering's remaining checks of those forms
	// are internal errors (stage_e4_test.go).
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
	{"call:With:imported", regexp.MustCompile(`^a with of an imported context is lowered when 196b lowers packages`)},
	{"TypeDecl:errorText", regexp.MustCompile(`^the text of an error type with a field of .+ is lowered in lower-v1 stage 3`)},
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
	callKinds := []string{"Func", "Op", "ProviderOp", "FuncValue", "Conversion", "Len", "With", "Prelude", "LawOp"}
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

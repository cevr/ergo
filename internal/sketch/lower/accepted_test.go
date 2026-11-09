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
	// A law lowers in the test build, which runs it (#162 law-run-1,
	// law_run_test.go); the run build this test lowers skips it whole
	// (stage E3b-2).
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
	// 196b U4 removed the family of a with or an embedding of an imported
	// context: the unit sources the imported package, and its contexts are
	// built as the root's are (TestRunImportedContext).
	// The text of an error type with a field whose type has no text form
	// (a Map, a tuple, a pointer): derived text forms are #49's.
	"TypeDecl:errorText": "#49",
	// An indexed slice literal `[]int{2: 5}`: the checker admits any int
	// key, Go takes only distinct constant keys, and #196 has not decided
	// what a computed, negative or repeated key does.
	"CompositeLit:indexedSlice": "#196",
	// Stage 4 of lower-v1 (S4d) made every other refusal of a form that a
	// checker or parser rule refuses an internal compiler error that names
	// the rule (unreachable, unreachable.go; TestRefusalsNameOpenOwners),
	// and lowered a record whose renamed Error field meets a field named
	// Error_ (TestErrorFieldNamesStayDistinct).
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
	// extern. An extern is call-only (C25, P3), those releases lower since
	// lower-v1 stage 4 (C29, stage4_close_test.go; the checker refuses only
	// a Close whose release would pass witnesses, closeWitnessed), and a user extern in
	// its checked domain lowers as a direct Go call (C26). The lowering's
	// remaining checks of those forms are internal errors (stage_e4_test.go).
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
	{"TypeDecl:errorText", regexp.MustCompile(`^the text of an error type with a field of type .+ waits for derived text forms \(#49\)`)},
	{"CompositeLit:indexedSlice", regexp.MustCompile(`^an indexed slice literal waits for #196 to decide its keys`)},
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
//
// The test runs in parts (acceptedSplit): each part lowers every
// acceptedParts-th source, checks its refusals, and builds and runs its
// programs in modules of its own. TestAcceptedNodesLowerFamilies lowers
// every source again, with no build, for the checks over the whole domain:
// each implemented family has a witness, and each accepted family is
// lowered or a documented refusal.

// acceptedSource is one source of the domain, with its oracle ("" for
// none: the source is built, not run).
type acceptedSource struct{ name, path, src, want string }

// acceptedSources reads the domain: the fixtures, the probes, the examples
// and this package's testdata, then the generated programs and the
// witnesses.
func acceptedSources() ([]acceptedSource, error) {
	var sources []acceptedSource
	clean := regexp.MustCompile(`[^A-Za-z0-9]+`)
	for _, root := range []string{"../check/testdata", "../testdata", "../../../examples", "testdata"} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ergo") {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := acceptedSource{name: fmt.Sprintf("f%d_%s", len(sources), clean.ReplaceAllString(strings.TrimSuffix(filepath.Base(p), ".ergo"), "_")), path: p, src: string(data)}
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
			return nil, err
		}
	}
	for i := 0; i < 2; i++ {
		g := &orderGen{rng: rand.New(rand.NewSource(int64(7000 + i)))}
		src, want := g.program(25)
		sources = append(sources, acceptedSource{name: fmt.Sprintf("g%d_generated", i), path: "generated", src: src, want: want})
	}
	for _, c := range append(rowsCases(), genericCases()...) {
		if c.exit == 0 {
			sources = append(sources, acceptedSource{name: "w_" + c.name, path: "witness " + c.name, src: c.src, want: c.stdout})
		}
	}
	return sources, nil
}

// TestAcceptedNodesLowerFamilies: over the whole domain, every implemented
// family has a lowered witness, and every family a checker-accepted source
// holds is lowered or a documented refusal. The inventory is logged.
func TestAcceptedNodesLowerFamilies(t *testing.T) {
	sources, err := acceptedSources()
	if err != nil {
		t.Fatal(err)
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
	var ran []string
	lowered, rejected := 0, 0
	for _, s := range sources {
		o := lowerSource(s.name, s.src, s.want != "")
		if o.file == nil || len(o.diags) > 0 {
			rejected++
			continue
		}
		fams := families(o.file, o.pkg.Info)
		for f := range fams {
			accepted[f] = true
		}
		switch {
		case o.panicked != nil || o.err != nil:
			// A part reports it.
		case len(o.refused) > 0:
			for _, r := range o.refused {
				family := refusalFamily(r.Message)
				owner := refusedFamilies[family]
				refusedBy[owner]++
				refusedAt[family]++
			}
		default:
			for f := range fams {
				witnessed[f]++
			}
			lowered++
			if s.want != "" {
				ran = append(ran, o.name)
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
	fmt.Fprintf(&inventory, "sources %d: rejected by the checker %d, lowered %d, refused %d\n", len(sources), rejected, lowered, len(sources)-rejected-lowered)
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
	fmt.Fprintf(&inventory, "run against an oracle: %s\n", strings.Join(ran, ", "))
	t.Log("family inventory\n" + inventory.String())
}

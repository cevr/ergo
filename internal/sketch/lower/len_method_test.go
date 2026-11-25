package lower

import (
	"encoding/json"
	"go/constant"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestLenMethodBCEAndIR verifies that x.Len() (direct and generic over
// core.Lengthed) matches len(x) on bounds-check elimination (including let mut
// receivers), constant folding (constant strings and pure fixed arrays), and
// direct IR len lowering with zero fallbacks.
func TestLenMethodBCEAndIR(t *testing.T) {
	// 1. BCE parity between len(x) and x.Len() on immutable and let mut slices/strings.
	bceCases := []struct {
		name      string
		src       string
		wantSites int
	}{
		{
			name: "immutable slice dot Len",
			src: `func f1(xs []int, i int) int {
	let mut acc = 0
	if 0 <= i && i < xs.Len() {
		let r1 = xs[i]
		acc = r1
	}
	acc
}

func main() {
	let _ = f1([]int{1, 2}, 0)
}
`,
			wantSites: 1,
		},
		{
			name: "let mut slice dot Len",
			src: `func f1(n int) int {
	let mut xs = []int{1, 2, 3}
	let mut i = 0
	let mut acc = 0
	for 0 <= i && i < xs.Len() {
		let r1 = xs[i]
		acc = acc + r1
		i = i + 1
	}
	if acc < n {
		xs = []int{4}
	}
	acc + xs.Len()
}

func main() {
	let _ = f1(0)
}
`,
			wantSites: 1,
		},
		{
			name: "string dot Len index and cut",
			src: `func f1(s string, p int) int {
	let mut acc = 0
	if 0 <= p && p <= s.Len()-4 {
		let r1 = s[p]
		let r2 = s[p : p+4]
		acc = int(r1) + r2.Len()
	}
	acc
}

func main() {
	let _ = f1("abcdef", 1)
}
`,
			wantSites: 2,
		},
		{
			name: "let mut string dot Len",
			src: `func f1(s0 string) int {
	let mut s = s0
	let mut i = 0
	let mut acc = 0
	for 0 <= i && i < s.Len() {
		let r1 = s[i]
		acc = acc + int(r1)
		i = i + 1
	}
	if acc < 0 {
		s = ""
	}
	acc + s.Len()
}

func main() {
	let _ = f1("abc")
}
`,
			wantSites: 1,
		},
	}
	for _, tc := range bceCases {
		t.Run("bce/"+tc.name, func(t *testing.T) {
			if factLowering == "checked" {
				t.Skip("reference lowering writes no unchecked site")
			}
			if got := boundsAgree(t, tc.name, tc.src, []string{"f1"}, false); got != tc.wantSites {
				t.Fatalf("%s: got %d unchecked sites, want %d", tc.name, got, tc.wantSites)
			}
		})
	}

	// 2. Constant folding for constant string and pure fixed array matches len(...)
	// in both checker Info.Values and IR const emission, while impure calls keep the call.
	constSrc := `func makeArr() [3]int {
	[3]int{1, 2, 3}
}

func fConstStr() int {
	"hello".Len()
}

func fConstArr(a [3]int) int {
	a.Len()
}

func fImpureArr() int {
	makeArr().Len()
}

func main() {
	let _ = fConstStr() + fConstArr([3]int{1, 2, 3}) + fImpureArr()
}
`
	f, err := syntax.Parse(constSrc)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, check.Std)
	if len(diags) != 0 {
		t.Fatalf("constSrc diagnostics: %v", diags)
	}
	constCalls := map[string]int64{}
	for _, d := range f.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			if val, ok := pkg.Info.Values[call]; ok && val != nil && val.Kind() == constant.Int {
				if v, exact := constant.Int64Val(val); exact {
					constCalls[fd.Name.Name] = v
				}
			}
			return true
		})
	}
	if constCalls["fConstStr"] != 5 {
		t.Fatalf("checker did not fold \"hello\".Len() to constant 5: got %v", constCalls)
	}
	if constCalls["fConstArr"] != 3 {
		t.Fatalf("checker did not fold a.Len() to constant 3: got %v", constCalls)
	}
	if _, folded := constCalls["fImpureArr"]; folded {
		t.Fatalf("checker folded impure makeArr().Len() into a constant: %v", constCalls)
	}

	constFiles, constRep, err := LowerIR(factsUnit(t, constSrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(constRep.Fallback) != 0 {
		t.Fatalf("unexpected IR fallbacks in constSrc: %v", constRep.Fallback)
	}
	var constIR irFile
	for _, lf := range constFiles {
		if lf.Path == IRFileName {
			if err := json.Unmarshal(lf.Data, &constIR); err != nil {
				t.Fatal(err)
			}
		}
	}
	fnByName := map[string]*irFunc{}
	for i := range constIR.Funcs {
		fnByName[constIR.Funcs[i].Name] = &constIR.Funcs[i]
	}
	hasExprKind := func(fn *irFunc, kind, val string) bool {
		found := false
		irWalk(fn.Body, false, func(*irStmt, bool) {}, func(n *irExpr, _ bool) {
			if n.K == kind && (val == "" || n.V == val) {
				found = true
			}
		})
		return found
	}
	if fn := fnByName["fConstStr"]; fn == nil || !hasExprKind(fn, "const", "5") || hasExprKind(fn, "len", "") {
		t.Fatalf("fConstStr did not fold to IR const 5: %+v", fn)
	}
	if fn := fnByName["fConstArr"]; fn == nil || !hasExprKind(fn, "const", "3") || hasExprKind(fn, "len", "") {
		t.Fatalf("fConstArr did not fold to IR const 3: %+v", fn)
	}
	if fn := fnByName["fImpureArr"]; fn == nil || !hasExprKind(fn, "call", "") {
		t.Fatalf("fImpureArr dropped call to makeArr(): %+v", fn)
	}

	// 3. Direct and generic [C core.Lengthed] calls lower to IR len nodes with
	// zero witness parameters, zero wrapper calls, and zero fallbacks.
	irSrc := `import core

func directSlice(xs []int) int { xs.Len() }
func directMap(m Map[string, int]) int { m.Len() }
func directStr(s string) int { s.Len() }
func genericLen[C core.Lengthed](c C) int { c.Len() }

func main() {
	let _ = directSlice([]int{1, 2}) +
		directMap(Map[string, int]{"a": 1}) +
		directStr("abc") +
		genericLen([]int{1, 2, 3}) +
		genericLen(Map[string, int]{"a": 1}) +
		genericLen("abcd") +
		genericLen([4]int{1, 2, 3, 4})
}
`
	u := factsUnit(t, irSrc)
	u.NoFallback = true
	irFiles, irRep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(irRep.Fallback) != 0 {
		t.Fatalf("unexpected IR fallbacks in irSrc: %v", irRep.Fallback)
	}
	var ir irFile
	for _, lf := range irFiles {
		if lf.Path == IRFileName {
			if err := json.Unmarshal(lf.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, wantName := range []string{"directSlice", "directMap", "directStr"} {
		var fn *irFunc
		for i := range ir.Funcs {
			if ir.Funcs[i].Name == wantName {
				fn = &ir.Funcs[i]
				break
			}
		}
		if fn == nil || !hasExprKind(fn, "len", "") || hasExprKind(fn, "call", "") || hasExprKind(fn, "vcall", "") {
			t.Fatalf("%s did not lower directly to IR len node without call/vcall: %+v", wantName, fn)
		}
	}
	var fnNames []string
	genericCopies := 0
	for i := range ir.Funcs {
		fn := &ir.Funcs[i]
		fnNames = append(fnNames, fn.Name)
		if !strings.HasPrefix(fn.Name, "genericLenMono_") {
			continue
		}
		genericCopies++
		if hasExprKind(fn, "vcall", "") || hasExprKind(fn, "call", "") {
			t.Fatalf("monomorphized %s emitted call/vcall instead of direct len/const: %+v", fn.Name, fn)
		}
		if !hasExprKind(fn, "len", "") && !hasExprKind(fn, "const", "4") {
			t.Fatalf("monomorphized %s emitted neither IR len nor folded const 4: %+v", fn.Name, fn)
		}
	}
	if genericCopies != 4 {
		t.Fatalf("expected 4 monomorphized genericLen copies ([]int, Map, string, [4]int), got %d (funcs: %v)", genericCopies, fnNames)
	}
}

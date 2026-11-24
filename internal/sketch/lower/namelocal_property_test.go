package lower

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// nameLocalPool are the spellings the property's locals, callees and
// unrelated declarations draw from: plain names and the bases the
// lowering's own helpers use (live, kept, rel, err, slot, env).
var nameLocalPool = []string{"x", "count", "total", "item", "acc", "step", "live", "kept", "rel", "err", "slot", "env", "g", "h"}

// nameLocalDefault is lib's fixed part. D's let of Reg takes a generated
// default (types.md, Zero values 3) and its call of id a monomorphic copy
// (mono.go); E's call of id is an inline site on the IR as well as in the
// text (D's default holding a Map keeps D off the IR); H calls a Go
// package (a host import); G, which only the reach form calls, takes
// another copy of id and inlines it before E does. K's call of chk is an
// inline site on the IR whose callee fails with a local (INLINE-FAIL-LOCAL).
// Its failure is a generic record so the flat result ABI does not take
// that call instead. J, which the reach form calls first, inlines chk
// too: the tag of each copy's lines is its site's key (rule 7), so K's
// IR does not move. nameLocalHelperNames are the
// spellings of D's default without rule 3's ergo__ and alias, and the host
// import's: every added declaration set holds them, so a helper or an
// import spelled outside "__" meets one.
const nameLocalDefault = "import go strings\n\ntype Reg (int, Map[int, int])\n\nfunc id[T](v T) T { v }\n\nfunc two[T](v T, n int) T {\n\tif n > 0 {\n\t\treturn two(v, n - 1)\n\t}\n\tv\n}\n\n" +
	"func G(k int) int {\n\tlet s = id(\"x\")\n\tlet r Reg\n\tlet (_, m) = r\n\tlet mut w = m\n\tlet t = id(s)\n\tw[k] = len(two(t, 1))\n\tlen(w)\n}\n\n" +
	"func E(k int) int {\n\tlet q = id(k)\n\tq + 1\n}\n\n" +
	"func D(k int) int {\n\tlet r Reg\n\tlet (_, m) = r\n\tlet mut w = m\n\tlet q = id(k)\n\tw[two(q, 1)] = 5\n\tlen(w)\n}\n\n" +
	"func H(s string) int needs go.Foreign { strings.Count(s, \"a\") }\n\n" +
	"type Bad[T] { n T }\n\nfunc chk(a int) int fails Bad[int] {\n\tif a < 0 {\n\t\tlet b = Bad[int]{n: a}\n\t\treturn b\n\t}\n\ta + 1\n}\n\n" +
	"func J(k int) int {\n\tlet mut t = 0\n\tfor i in 0..k {\n\t\tt = t + (chk(i - 9) catch {\n\t\tcase Bad[int] as b:\n\t\t\tb.n\n\t\t})\n\t}\n\tt * 2\n}\n\n" +
	"func K(k int) int {\n\tlet mut t = 0\n\tfor i in 0..k {\n\t\tt = t + (chk(i - 1) catch {\n\t\tcase Bad[int] as b:\n\t\t\tb.n\n\t\t})\n\t}\n\tt + 3\n}\n\n"

var nameLocalHelperNames = []string{"DefaultReg", "ergoDefaultReg", "strings"}

// nameLocalProgram is a seeded program: package lib (a callee and a
// function of lets that calls it), a main helper of lets, and, when
// noise, an unrelated package noise that main imports, whose top-level
// functions take spellings of the pool.
type nameLocalProgram struct {
	lib, noise, helper string
	callee             string
	locals             []string
	noiseNames         []string
}

func genNameLocal(seed uint64) nameLocalProgram {
	r := rand.New(rand.NewPCG(seed, 0x6e616d65))
	pool := slices.Clone(nameLocalPool)
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	p := nameLocalProgram{callee: pool[0], locals: pool[1 : 2+r.IntN(5)]}
	var lib strings.Builder
	fmt.Fprintf(&lib, "func %s(a int) int {\n\tif a > %d {\n\t\treturn %s(a - 1)\n\t}\n\ta * %d\n}\n\n", p.callee, 3+r.IntN(7), p.callee, 2+r.IntN(5))
	lib.WriteString("func F(n int) int {\n\tif n > 100 {\n\t\treturn F(n - 100)\n\t}\n")
	prev := "n"
	for i, l := range p.locals {
		if i%2 == 0 {
			fmt.Fprintf(&lib, "\tlet %s = %s(%s)\n", l, p.callee, prev)
		} else {
			fmt.Fprintf(&lib, "\tlet %s = %s + %d\n", l, prev, i)
		}
		prev = l
	}
	fmt.Fprintf(&lib, "\t%s\n}\n", prev)
	p.lib = nameLocalDefault + lib.String()

	var h strings.Builder
	h.WriteString("func helper(n int) int {\n\tif n > 100 {\n\t\treturn helper(n - 100)\n\t}\n")
	prev = "n"
	for i, l := range p.locals {
		fmt.Fprintf(&h, "\tlet %s = %s * %d\n", l, prev, i+2)
		prev = l
	}
	fmt.Fprintf(&h, "\t%s\n}\n", prev)
	// K's call of chk is an inline site on the IR whose callee fails with
	// a local (INLINE-FAIL-LOCAL): its copy's lines carry the site's key
	// (rule 7), which must not move when a package is added.
	h.WriteString("\ntype Bad[T] { n T }\n\nfunc chk(a int) int fails Bad[int] {\n\tif a < 0 {\n\t\tlet b = Bad[int]{n: a}\n\t\treturn b\n\t}\n\ta + 1\n}\n\n")
	h.WriteString("func K(k int) int {\n\tlet mut t = 0\n\tfor i in 0..k {\n\t\tt = t + (chk(i - 1) catch {\n\t\tcase Bad[int] as b:\n\t\t\tb.n\n\t\t})\n\t}\n\tt + 3\n}\n")
	p.helper = h.String()

	var noise strings.Builder
	names := append(append([]string{p.callee}, p.locals...), nameLocalHelperNames...)
	for _, n := range nameLocalPool {
		if !slices.Contains(names, n) && r.IntN(2) == 0 {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	p.noiseNames = names
	sum := []string{}
	for i, n := range names {
		fmt.Fprintf(&noise, "func %s() int { %d }\n\n", n, i+1)
		sum = append(sum, n+"()")
	}
	fmt.Fprintf(&noise, "func Touch() int { %s }\n", strings.Join(sum, " + "))
	p.noise = noise.String()
	return p
}

// The forms of the program: without the added declarations, with them as
// the package noise, with them in the root package itself, after main so
// no position of the root moves, and, in the reach form, without them but
// with lib's G reached first.
const (
	nameLocalBase = iota
	nameLocalPackage
	nameLocalRoot
	nameLocalReach
)

// nameLocalUse is the root's use, which calls lib on a parameter: main's
// call of it is a constant the IR folds (ir_constfold.go), but use's own
// body keeps lib's functions reached, so they are IR bodies too. In the
// reach form it calls G first; its line holds the change, so no other
// position of the root moves.
func nameLocalUse(form int) string {
	g := ""
	if form == nameLocalReach {
		g = "lib.G(k) + lib.J(k) + "
	}
	return "\nfunc use(k int) int needs go.Foreign { " + g + "lib.F(k) + lib.D(k) + lib.E(k) + lib.H(\"aa\") + lib.K(k) + K(k) }\n"
}

func (p nameLocalProgram) main(form int) string {
	switch form {
	case nameLocalPackage:
		return "import lib\nimport noise\n\n" + p.helper + nameLocalUse(form) + "\nfunc main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(use(2) + helper(3) + noise.Touch())\")\n}\n"
	case nameLocalRoot:
		return "import lib\n\n\n" + p.helper + nameLocalUse(form) + "\nfunc main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(use(2) + helper(3) + Touch())\")\n}\n\n" + p.noise
	}
	return "import lib\n\n\n" + p.helper + nameLocalUse(form) + "\nfunc main() needs Console | go.Foreign {\n\tConsole.Println(\"\\(use(2) + helper(3))\")\n}\n"
}

// nameLocalFuncs lowers the program on the IR and as Go text and gives,
// for each function declared at a position other than main's own main,
// its IR (positions as paths) and its Go text, keyed by its declaration's
// path:line:col, and the text path's program, to type-check.
func nameLocalFuncs(t *testing.T, p nameLocalProgram, form int, name string) (ir, text map[string]string, prog *lowered) {
	t.Helper()
	imports := cxImporter{"lib": cxPackage(t, "lib", p.lib)}
	if form == nameLocalPackage {
		imports["noise"] = cxPackage(t, "noise", p.noise)
	}
	src := p.main(form)
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, imports)
	if len(ds) != 0 {
		t.Fatalf("%v\n%s", ds, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}, Imports: imports.units()}
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	ir, text = map[string]string{}, map[string]string{}
	for _, f := range files {
		switch {
		case f.Path == IRFileName:
			var doc struct {
				Files []string          `json:"files"`
				Funcs []json.RawMessage `json:"funcs"`
			}
			if err := json.Unmarshal(f.Data, &doc); err != nil {
				t.Fatal(err)
			}
			for _, raw := range doc.Funcs {
				var fn any
				if err := json.Unmarshal(raw, &fn); err != nil {
					t.Fatal(err)
				}
				fn = irPathsOf(fn, doc.Files)
				m := fn.(map[string]any)
				// A generic function's copies share its position, so
				// the copy's name is part of the key.
				key := fmt.Sprint(m["pos"], " ", m["name"])
				data, _ := json.Marshal(fn)
				ir[key] = string(data)
			}
		}
	}
	// The text path: the same unit lowered with no IR.
	files, err = Lower(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".go") {
			for _, fn := range goFuncsByLine(string(f.Data)) {
				text[fn[0]] = fn[1]
			}
		}
	}
	return ir, text, &lowered{name: name, src: src, file: f, pkg: pkg, files: files}
}

// irPathsOf is v with each position [file, line, col] spelled with its
// file's path, so two IR files that list their files in other orders
// compare equal.
func irPathsOf(v any, files []string) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if pos, ok := x.([]any); ok && len(pos) == 3 && (k == "pos" || k == "end") {
				if i, ok := pos[0].(float64); ok && int(i) >= 0 && int(i) < len(files) {
					v[k] = fmt.Sprintf("%s:%v:%v", files[int(i)], pos[1], pos[2])
				}
				continue
			}
			v[k] = irPathsOf(x, files)
		}
	case []any:
		for i := range v {
			v[i] = irPathsOf(v[i], files)
		}
	}
	return v
}

// nameDiff names the first line (or, for one-line text, the first 80
// bytes from the first byte) where a and b differ.
func nameDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] == bl[i] {
			continue
		}
		x, y := al[i], bl[i]
		j := 0
		for j < len(x) && j < len(y) && x[j] == y[j] {
			j++
		}
		from := max(j-40, 0)
		return fmt.Sprintf("line %d, without %q, with %q", i+1, x[from:min(len(x), j+40)], y[from:min(len(y), j+40)])
	}
	return fmt.Sprintf("lengths %d and %d lines", len(al), len(bl))
}

// funcEnd is a top-level function's closing brace: at column 0, after a
// /*line*/ comment in a //line-directed body.
var funcEnd = regexp.MustCompile(`\n(?:/\*line [^*]*\*/)?}\n`)

// goFuncsByLine cuts Go text into its top-level functions, each keyed by
// the first //line directive in it (the declaration's position).
func goFuncsByLine(text string) [][2]string {
	var out [][2]string
	parts := strings.Split(text, "\nfunc ")
	for _, part := range parts[1:] {
		// A body ends at a closing brace alone on its line, which a
		// //line-directed body writes after a /*line*/ comment.
		end := len(part)
		if loc := funcEnd.FindStringIndex(part); loc != nil {
			end = loc[0]
		}
		body := "func " + part[:end]
		i := strings.Index(body, "//line ")
		if i < 0 {
			continue
		}
		line := body[i+len("//line "):]
		if j := strings.IndexAny(line, "\n*"); j >= 0 {
			line = line[:j]
		}
		out = append(out, [2]string{line, body})
	}
	return out
}

// TestFunctionNamesIgnoreUnrelatedPackages: a function's Go names (its
// locals, its callees, its own) are a function of its declaration and its
// own package (NAME-LOCAL; S4's naming rule): adding a package that the
// function never names to the program leaves its IR and its Go text
// byte-identical. The added package declares top-level functions with
// the spellings of the function's locals and callee, of the bases the
// lowering's helpers take, and of lib's generated default without its
// ergo__ and alias (mutant helper-prefix-dropped); and every form's Go
// type-checks. ERGO_NAMELOCAL_SEED=<n> replays one seed.
func TestFunctionNamesIgnoreUnrelatedPackages(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_NAMELOCAL_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	var progs []*lowered
	for _, seed := range seeds {
		p := genNameLocal(seed)
		mainLine := strings.Count(p.main(nameLocalBase)[:strings.Index(p.main(nameLocalBase), "func main(")], "\n") + 1
		baseIR, baseText, bp := nameLocalFuncs(t, p, nameLocalBase, fmt.Sprintf("namelocal%d", seed))
		noiseIR, noiseText, np := nameLocalFuncs(t, p, nameLocalPackage, fmt.Sprintf("namelocal%dpackage", seed))
		rootIR, rootText, rp := nameLocalFuncs(t, p, nameLocalRoot, fmt.Sprintf("namelocal%droot", seed))
		reachIR, reachText, xp := nameLocalFuncs(t, p, nameLocalReach, fmt.Sprintf("namelocal%dreach", seed))
		progs = append(progs, bp, np, rp, xp)
		// Both K's calls of chk (the root's, key "… K", and lib's, whose
		// Go name is lib's alias and K) are inlined on the IR (their lines
		// carry the site's tag), so rule 7 is checked on fail-local copies.
		for _, k := range []string{" K", "__K"} {
			inlined := false
			for key, ir := range baseIR {
				if strings.HasSuffix(key, k) && strings.Contains(ir, "ergo-inline-") {
					inlined = true
				}
			}
			if !inlined {
				t.Errorf("seed %d: the K whose IR key ends %q does not inline chk on the IR, so no fail-local copy is compared", seed, k)
			}
		}
		for _, cmp := range []struct {
			what       string
			base, with map[string]string
			libOnly    bool // the root changed: only lib's functions are compared
		}{{"IR", baseIR, noiseIR, false}, {"Go text", baseText, noiseText, false}, {"Go text (declarations added to the root)", baseText, rootText, true}, {"IR (declarations added to the root)", baseIR, rootIR, true}, {"Go text (lib reaches more)", baseText, reachText, true}, {"IR (lib reaches more)", baseIR, reachIR, true}} {
			n, libs := 0, 0
			for key, b := range cmp.base {
				if cmp.libOnly && !strings.Contains(key, "/lib/") {
					continue
				}
				if line, ok := strings.CutPrefix(key, testSourcePath+":"); ok {
					if n, _ := strconv.Atoi(line[:strings.Index(line, ":")]); n >= mainLine {
						continue // main's own main, which calls noise
					}
				}
				w, ok := cmp.with[key]
				if !ok {
					t.Errorf("seed %d: the %s function at %s is gone when an unrelated package is added", seed, cmp.what, key)
					continue
				}
				n++
				if strings.Contains(key, "/lib/") {
					libs++
				}
				if b != w {
					t.Errorf("seed %d: the %s of the function at %s changes when an unrelated package declaring %v is added: %s\nlib:\n%s\nreplay: ERGO_NAMELOCAL_SEED=%d", seed, cmp.what, key, p.noiseNames, nameDiff(b, w), p.lib, seed)
				}
			}
			if n == 0 || libs == 0 {
				keys := slices.Sorted(maps.Keys(cmp.base))
				t.Errorf("seed %d: %d %s functions besides main's main to compare, %d of them lib's; want lib's too; functions at %v", seed, n, cmp.what, libs, keys)
			}
		}
	}
	// Every form builds: a name two packages spell alike, or a helper a
	// declaration spells, is two Go names (mutant decl-alias-dropped).
	typecheckGo(t, progs)
}

package gobind_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// use is what a generated function does with one parameter, and the
// model's verdict: only a plain read is `reads`.
type use struct {
	name  string
	types []string // the parameter types the use is written for
	body  string   // the statement, with P for the parameter
	reads bool
}

var uses = []use{
	{"read-len", []string{"[]int", "[]byte", "[][]int", "[]*Pt", "[]Pt", "S"}, "n += len(P)", true},
	{"read-index", []string{"[]int", "S"}, "if len(P) > 0 { n += P[0] }", true},
	{"read-range", []string{"[]int", "S"}, "for _, v := range P { n += v }", true},
	{"read-deref", []string{"[]*Pt"}, "if len(P) > 0 && P[0] != nil { n += P[0].X }", true},
	{"read-field", []string{"[]Pt"}, "if len(P) > 0 { n += P[0].X }", true},
	{"read-nested", []string{"[][]int"}, "if len(P) > 0 && len(P[0]) > 0 { n += P[0][0] }", true},
	{"read-bytes", []string{"[]byte"}, "for _, b := range P { n += int(b) }", true},
	{"write-index", []string{"[]int", "S"}, "if len(P) > 0 { P[0] = n }", false},
	{"write-alias", []string{"[]int", "S"}, "q := P; if len(q) > 0 { q[0]++ }", false},
	{"write-copy", []string{"[]int", "S"}, "copy(P, []int{n})", false},
	{"write-append", []string{"[]int"}, "_ = append(P[:0], n)", false},
	{"write-sort", []string{"[]int"}, "sort.Ints(P)", false},
	{"write-nested", []string{"[][]int"}, "if len(P) > 0 && len(P[0]) > 0 { P[0][0] = n }", false},
	{"write-deref", []string{"[]*Pt"}, "if len(P) > 0 && P[0] != nil { P[0].Y = n }", false},
	{"write-field", []string{"[]Pt"}, "if len(P) > 0 { P[0].X = n }", false},
	{"write-bytes", []string{"[]byte"}, "if len(P) > 0 { P[0] = byte(n) }", false},
	{"write-clear", []string{"[]int"}, "clear(P)", false},
	{"keep", []string{"[]int", "[]byte", "[][]int", "[]*Pt", "[]Pt", "S"}, "keptAny = P", false},
	{"keep-elem", []string{"[][]int", "[]*Pt"}, "if len(P) > 0 { keptAny = P[0] }", false},
	{"call-hook", []string{"[]int"}, "hook(P)", false},
	{"interface", []string{"[]int", "[]byte"}, "sinkI.Take(P)", false},
	// Go's analysis does not count a write into a map as a mutation; the
	// map types must get no fact whatever the body does.
	{"map-put", []string{"map[string]int"}, "P[\"k\"] = n", false},
	{"map-delete", []string{"map[string]int"}, "delete(P, \"k\")", false},
	{"map-read", []string{"map[string]int"}, "n += P[\"k\"]", false},
	{"map-in-slice", []string{"[]map[string]int"}, "if len(P) > 0 { P[0][\"k\"] = n }", false},
}

// share is the result-sharing use: the function returns a part of P.
const share = "share"

// TestDerivedFactsSound generates Go packages whose functions read, write,
// keep, return a part of, pass to a function variable or to an interface,
// or (for a map) write each parameter, with a model that knows which, binds
// them with ergo bind's own path on every platform, and holds the
// binding's `// ergo:reads` lines to the model: no parameter the model says
// is written, kept, shared or called is `reads` (soundness), and every
// plain read of a slice is (the fact is not vacuous). A method's receiver,
// a hidden context.Context and a renamed binder shift the index between
// Go's names and the binding's (LAWFFI risk 2); they are generated too. A
// failing seed prints the Go source and the binding; ERGO_DERIVE_SEED
// replays one seed.
//
// It builds Go because the derivation is the Go compiler's own escape
// analysis output (go build -gcflags=-m), which no in-process check
// reproduces; so it runs in the full tier only (needsBuild).
//
// Mutants it alone catches: ffi-derive-reads-on-mutates,
// ffi-derive-reads-on-leak-to-result, ffi-derive-wrong-parameter,
// ffi-derive-any-verdict-is-reads, ffi-derive-map-reads,
// ffi-derive-map-elem-reads.
func TestDerivedFactsSound(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_DERIVE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_DERIVE_SEED: %v", err)
		}
		seeds = []uint64{n}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.test/derive\n\ngo 1.27\n")
	type pkg struct {
		path, src string
		model     map[string][]string // function -> the binders the model says only read
		all       map[string][]string // function -> every slice binder
	}
	var pkgs []pkg
	var patterns []string
	for _, seed := range seeds {
		name := fmt.Sprintf("d%d", seed)
		src, model, all := genDerive(name, rand.New(rand.NewPCG(seed, 0xfac7)))
		write(t, filepath.Join(dir, name, name+".go"), src)
		pkgs = append(pkgs, pkg{"example.test/derive/" + name, src, model, all})
		patterns = append(patterns, "./"+name)
	}
	files, err := gobind.Bind(dir, os.Environ(), "go1.27", patterns...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		text := files[p.path]
		got, bound := readsLines(p.path, text)
		var bad []string
		for fn, all := range p.all {
			if !bound[fn] {
				continue // refused: no binding, no fact
			}
			want := p.model[fn]
			for _, b := range all {
				g, w := slices.Contains(got[fn], b), slices.Contains(want, b)
				switch {
				case g && !w:
					bad = append(bad, fmt.Sprintf("%s: %s is reads, but the function writes, keeps, shares or calls it", fn, b))
				case !g && w:
					bad = append(bad, fmt.Sprintf("%s: %s is only read, but the binding has no reads fact", fn, b))
				}
			}
			for _, b := range got[fn] {
				if !slices.Contains(all, b) {
					bad = append(bad, fmt.Sprintf("%s: reads names %s, no slice parameter", fn, b))
				}
			}
		}
		for fn := range got {
			if _, ok := p.all[fn]; !ok {
				bad = append(bad, fmt.Sprintf("%s: a reads line, but the function has no slice parameter", fn))
			}
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			t.Errorf("seed package %s (replay: ERGO_DERIVE_SEED=%s):\n%s\n--- Go source:\n%s\n--- binding:\n%s", p.path, strings.TrimPrefix(filepath.Base(p.path), "d"), strings.Join(bad, "\n"), p.src, text)
		}
	}
}

var (
	readsRe  = regexp.MustCompile(`^// ergo:reads (.+)$`)
	externRe = regexp.MustCompile(`^extern go (\S+) `)
)

// readsLines reads the reads lines of the binding of the Go package path,
// by the name below each (F, or T.M), and the names it binds.
func readsLines(path, text string) (reads map[string][]string, bound map[string]bool) {
	reads, bound = map[string][]string{}, map[string]bool{}
	var pending []string
	for _, line := range strings.Split(text, "\n") {
		if m := readsRe.FindStringSubmatch(line); m != nil {
			pending = strings.Fields(m[1])
			continue
		}
		if m := externRe.FindStringSubmatch(line); m != nil {
			name := strings.TrimPrefix(m[1], path+".")
			bound[name] = true
			if pending != nil {
				reads[name] = pending
			}
			pending = nil
			continue
		}
		if !strings.HasPrefix(line, "//") {
			pending = nil
		}
	}
	return reads, bound
}

// genDerive writes a Go package of functions and methods whose parameters
// each take one use, and the model: for each function, by its binding name
// (F, or S.M), the binders the model says are only read, and every binder
// of a slice parameter (a map, an []any or a []map is no slice the fact
// may name; it is in neither list).
func genDerive(name string, r *rand.Rand) (string, map[string][]string, map[string][]string) {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"context\"\n\t\"sort\"\n)\n\n", name)
	b.WriteString("type Pt struct{ X, Y int }\n\ntype S []int\n\nvar keptAny any\n\nvar hook = func([]int) {}\n\nvar sinkI interface{ Take(any) }\n\nvar _ = sort.Ints\n\nvar _ context.Context\n\n")
	model, all := map[string][]string{}, map[string][]string{}
	// Binders: Go names that stay, and Ergo keywords that Go allows,
	// which the binding renames p<i> (the index shift of risk 2).
	names := []string{"a", "b", "xs", "data", "match", "let", "fails"}
	for i := 0; i < 24; i++ {
		fn := fmt.Sprintf("F%d", i)
		method := r.IntN(4) == 0
		ctx := !method && r.IntN(4) == 0
		n := 1 + r.IntN(3)
		var params, body []string
		key := fn
		recv := ""
		if method {
			key = "S." + fn
			u := pickUse(r, "S")
			recv = "s"
			if u.name != share {
				body = append(body, strings.ReplaceAll(u.body, "P", "s"))
			}
			all[key] = append(all[key], "s")
			if u.reads {
				model[key] = append(model[key], "s")
			}
		}
		if ctx {
			params = append(params, "ctx context.Context")
			body = append(body, "_ = ctx")
		}
		var shared string
		used := map[string]bool{}
		for j := 0; j < n; j++ {
			pn := names[r.IntN(len(names))]
			for used[pn] {
				pn = names[r.IntN(len(names))]
			}
			used[pn] = true
			types := []string{"[]int", "[]byte", "[][]int", "[]*Pt", "[]Pt", "map[string]int", "[]map[string]int"}
			typ := types[r.IntN(len(types))]
			var u use
			if shared == "" && strings.HasPrefix(typ, "[]") && typ != "[]map[string]int" && r.IntN(6) == 0 {
				u = use{name: share}
				shared = pn
			} else {
				u = pickUse(r, typ)
			}
			params = append(params, pn+" "+typ)
			if u.name != share {
				body = append(body, strings.ReplaceAll(u.body, "P", pn))
			}
			// The binding's binder: a keyword is renamed p<i>, i Go's index.
			binder := pn
			if pn == "match" || pn == "let" || pn == "fails" {
				idx := j
				if ctx {
					idx++
				}
				binder = fmt.Sprintf("p%d", idx)
			}
			if typ == "map[string]int" || typ == "[]map[string]int" {
				continue
			}
			all[key] = append(all[key], binder)
			if u.reads {
				model[key] = append(model[key], binder)
			}
		}
		result := "int"
		ret := "n"
		if shared != "" {
			result = "(int, " + typeOfParam(params, shared) + ")"
			ret = "n, " + shared + "[len(" + shared + ")/2:]"
		}
		if method {
			fmt.Fprintf(&b, "func (%s S) %s(%s) %s {\n\tn := 0\n", recv, fn, strings.Join(params, ", "), result)
		} else {
			fmt.Fprintf(&b, "func %s(%s) %s {\n\tn := 0\n", fn, strings.Join(params, ", "), result)
		}
		for _, s := range body {
			b.WriteString("\t" + s + "\n")
		}
		b.WriteString("\treturn " + ret + "\n}\n\n")
	}
	return b.String(), model, all
}

func typeOfParam(params []string, name string) string {
	for _, p := range params {
		if n, t, _ := strings.Cut(p, " "); n == name {
			return t
		}
	}
	return ""
}

// pickUse picks a use written for typ.
func pickUse(r *rand.Rand, typ string) use {
	var fit []use
	for _, u := range uses {
		if slices.Contains(u.types, typ) {
			fit = append(fit, u)
		}
	}
	return fit[r.IntN(len(fit))]
}

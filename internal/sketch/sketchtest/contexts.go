package sketchtest

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
)

// ContextPackage is one package of a ContextProgram: its import path
// ("gen" for the root), its one file's slash path in the module and the
// file's text.
type ContextPackage struct {
	Path, File, Text string
}

// ContextProgram is a generated module "gen" of packages, in the order a
// checker takes them: each after the packages it imports, the root last.
type ContextProgram struct {
	Seed     int64
	Packages []ContextPackage
}

// Files are the module's files by slash path, ergo.mod included.
func (p ContextProgram) Files() map[string]string {
	files := map[string]string{"ergo.mod": "module gen\n"}
	for _, q := range p.Packages {
		files[q.File] = q.Text
	}
	return files
}

// String is the module's files, for a failure.
func (p ContextProgram) String() string {
	var b strings.Builder
	for _, q := range p.Packages {
		fmt.Fprintf(&b, "--- %s\n%s", q.File, q.Text)
	}
	return b.String()
}

// GenerateContextProgram generates, from seed, a module of lib packages
// l1..lk (k up to 3) and a root. Each package declares operation types
// implemented by records, and contexts that bind them in a shuffled order,
// some through a constructor, and that embed contexts of the packages it
// imports; the root binds its own and embeds every context no other embeds,
// and its test and main call every capability. Every capability is bound once and
// every context embedded at most once, so the program checks. It is the
// shape where two packages' context entries once tied in the checker's
// order (wf10 NONDET1).
func GenerateContextProgram(seed int64) ContextProgram {
	rng := rand.New(rand.NewSource(seed))
	type ctx struct {
		pkg, name string
		embedded  bool
	}
	k := 1 + rng.Intn(3)
	var contexts []*ctx // of the libs, deepest package first
	var calls []string
	total := 0
	pkgs := make([]string, k+1) // pkgs[0] is the root
	for i := 1; i <= k; i++ {
		pkgs[i] = "l" + strconv.Itoa(i)
	}
	// One package's text: its capabilities, then its contexts, which embed
	// free contexts of deeper packages.
	pkg := func(i int) (string, []string) {
		var b strings.Builder
		imports := map[string]bool{}
		var entries []string
		for c := 0; c < 1+rng.Intn(3); c++ {
			op, rec, v := fmt.Sprintf("C%d_%d", i, c), fmt.Sprintf("R%d_%d", i, c), 1+rng.Intn(9)
			method := fmt.Sprintf("Get%d_%d", i, c)
			fmt.Fprintf(&b, "type %s { %s() int }\ntype %s {} implements %s\nfunc %s(_ %s) int { %d }\n", op, method, rec, op, method, rec, v)
			provider := rec
			if rng.Intn(2) == 0 {
				provider = "New" + rec + "()"
				fmt.Fprintf(&b, "func New%s() %s { %s }\n", rec, rec, rec)
			}
			entries = append(entries, op+": "+provider)
			if i == 0 {
				calls = append(calls, fmt.Sprintf("%s.%s()", op, method))
			} else {
				calls = append(calls, fmt.Sprintf("%s.%s.%s()", pkgs[i], op, method))
			}
			total += v
		}
		var free []*ctx
		for _, c := range contexts {
			if !c.embedded {
				free = append(free, c)
			}
		}
		var names []string
		nctx := 1 + rng.Intn(2)
		if i == 0 {
			nctx = 1
		}
		for n := 0; n < nctx; n++ {
			name := fmt.Sprintf("Ctx%d_%d", i, n)
			var es []string
			if n == nctx-1 {
				es = append(es, entries...) // the last context binds what is left
			} else {
				cut := rng.Intn(len(entries) + 1)
				es, entries = append(es, entries[:cut]...), entries[cut:]
			}
			rng.Shuffle(len(es), func(a, b int) { es[a], es[b] = es[b], es[a] })
			// Embeddings come after the bindings: `x.W, K: v` is one entry
			// that binds both x.W and K to v.
			var embeds []string
			for _, c := range free {
				if !c.embedded && (i == 0 || rng.Intn(2) == 0) {
					c.embedded = true
					imports[c.pkg] = true
					embeds = append(embeds, c.pkg+"."+c.name)
				}
			}
			rng.Shuffle(len(embeds), func(a, b int) { embeds[a], embeds[b] = embeds[b], embeds[a] })
			es = append(es, embeds...)
			fmt.Fprintf(&b, "let %s = context { %s }\n", name, strings.Join(es, ", "))
			names = append(names, name)
		}
		var head strings.Builder
		for _, p := range pkgs[1:] {
			if imports[p] || i == 0 {
				fmt.Fprintf(&head, "import gen/%s\n", p)
			}
		}
		return head.String() + b.String(), names
	}
	p := ContextProgram{Seed: seed}
	for i := k; i >= 1; i-- {
		text, names := pkg(i)
		p.Packages = append(p.Packages, ContextPackage{Path: "gen/" + pkgs[i], File: pkgs[i] + "/" + pkgs[i] + ".ergo", Text: text})
		for _, n := range names {
			contexts = append(contexts, &ctx{pkg: pkgs[i], name: n})
		}
	}
	text, names := pkg(0)
	sum := strings.Join(calls, " + ")
	text += fmt.Sprintf("test Runs { assert with %s { %s } == %d }\n", names[0], sum, total)
	text += fmt.Sprintf("func main() {\n\tlet _ = with %s { %s }\n}\n", names[0], sum)
	p.Packages = append(p.Packages, ContextPackage{Path: "gen", File: "main.ergo", Text: text})
	return p
}

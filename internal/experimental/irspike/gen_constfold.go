package irspike

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// GenerateConstFold is a program of build-time folds (CF1): std html pages
// whose static parts are calls of constants (a zero-operand function whose
// body is one page tree of constants, and the same trees passed to Render
// in main), pages with dynamic parts (a parameter, Each over a list), and
// calls of the program's own pure functions on constants whose values are
// records, lists, a union's variants, floats, strings and bools, some
// bound to a let and read at run time. Some seeds
// end in a call of constants that raises a defect (an index out of range),
// which stays a call; every seed has a call of constants that runs past
// the fold's budget (spin), which stays as written. main prints each
// value. lits and recs call an impure function inside list and record
// literals, so the order their elements run in prints. fromLets reads
// package lets (a let of a call, a let that reads it, a node let, std
// code's let and a let that costs more than a fold's budget), which a fold
// binds on its first read (LAZY-LET), paying none of their steps. The second
// result reports a seed that ends in a defect.
func GenerateConstFold(seed uint64) (string, bool) {
	r := rand.New(rand.NewPCG(seed, seed^0x51ed270b2f3a9c4d))
	g := &cfGen{r: r}
	var b strings.Builder
	b.WriteString("import code\nimport html\nimport json\nimport toml\n\n")
	b.WriteString("type Circle { R int }\n\ntype Square { S int }\n\ntype Shape = Circle | Square\n\n")
	b.WriteString("type Pt { X int, Y float64, Name string, Tags []string, On bool, Shape Shape }\n\n")
	k := 1 + r.IntN(9)
	fmt.Fprintf(&b, "func mk(k int) Shape {\n\tif k %% 2 == 0 {\n\t\treturn Circle{R: k}\n\t}\n\tSquare{S: k * %d}\n}\n\n", k)
	b.WriteString("func area(s Shape) int {\n\tmatch s {\n\tcase Circle as c:\n\t\tc.R * c.R * 3\n\tcase Square as q:\n\t\tq.S * q.S\n\t}\n}\n\n")
	fmt.Fprintf(&b, "func pt(k int, name string) Pt {\n\tlet mut tags = []string{name}\n\tfor i in 0..k %% 4 {\n\t\ttags = tags.Append(\"t\\(i)\")\n\t}\n\tPt{X: k * %d, Y: float64(k) / 4.0, Name: name + \"!\", Tags: tags, On: k > 3, Shape: mk(k)}\n}\n\n", k)
	b.WriteString("func show(p Pt) string {\n\tlet mut s = \"\\(p.X) \\(p.Y) \\(p.Name) \\(p.On) \\(area(p.Shape))\"\n\tfor t in p.Tags {\n\t\ts = s + \",\" + t\n\t}\n\ts\n}\n\n")
	b.WriteString("func pick(xs []int, i int) int { xs[i] }\n\n")
	b.WriteString(foldRowDecls)
	// Impure calls inside list and record literals (the IR's spill):
	// noisy prints, so the order of the elements' calls is in the output.
	b.WriteString("func noisy(n int) int needs Console {\n\tConsole.Println(\"noisy \\(n)\")\n\tn\n}\n\n")
	fmt.Fprintf(&b, "func lits(n int) []int needs Console {\n\t[]int{noisy(n), n * %d, noisy(n + 1)}\n}\n\n", k)
	b.WriteString("func recs(n int) Pt needs Console {\n\tlet name = \"r\\(n)\"\n\tPt{X: noisy(n), Y: 0.5, Name: name, Tags: []string{name, name + \"!\"}, On: true, Shape: Circle{R: noisy(n + 1)}}\n}\n\n")
	b.WriteString("func spin(n int) int {\n\tlet mut s = 0\n\tfor i in 0..n {\n\t\ts = s + i % 7\n\t}\n\ts\n}\n\n")
	// Package lets a folded call reads: a let of a call, a let of that
	// let, a list of calls, a node, std code's let (pkg.v), and a let whose
	// initializer runs past a fold's budget but not past a let's.
	fmt.Fprintf(&b, "let base = pt(%d, %s)\n\nlet names = base.Tags.Append(\"n\")\n\nlet table = []int{area(mk(%d)), %d}\n\nlet node = %s\n\nlet heavy = spin(%d)\n\n", r.IntN(12), g.text(), r.IntN(20), r.IntN(50), g.tree(2, false), 1_000_000+r.IntN(1000))
	b.WriteString("func fromLets(i int) string {\n\tshow(base) + \" \\(len(names)) \\(pick(table, i)) \\(len(code.RenameNames)) \\(heavy) \" + html.Render(node)\n}\n\n")
	pages := 2 + r.IntN(3)
	for i := 0; i < pages; i++ {
		fmt.Fprintf(&b, "func static%d() html.Node {\n\t%s\n}\n\n", i, g.tree(3, false))
		fmt.Fprintf(&b, "func dyn%d(s string, xs []string) html.Node {\n\t%s\n}\n\n", i, g.tree(3, true))
	}
	b.WriteString("func main() needs Console {\n")
	writeFoldRowCases(&b, r)
	for i := 0; i < pages; i++ {
		fmt.Fprintf(&b, "\tConsole.Println(html.Render(static%d()))\n", i)
		fmt.Fprintf(&b, "\tConsole.Println(html.Render(%s))\n", g.tree(2, false))
		fmt.Fprintf(&b, "\tConsole.Println(html.Render(dyn%d(%s, []string{%s, %s})))\n", i, g.text(), g.text(), g.text())
	}
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, "\tConsole.Println(show(pt(%d, %s)))\n", r.IntN(12), g.text())
		// A let of a folded record (its list, its variant) read at run time.
		fmt.Fprintf(&b, "\tlet p%d = pt(%d, %s)\n\tConsole.Println(show(p%d))\n\tConsole.Println(\"\\(len(p%d.Tags)) \\(area(p%d.Shape))\")\n", i, r.IntN(12), g.text(), i, i, i)
		fmt.Fprintf(&b, "\tlet s%d = pt(%d, %s).Tags\n\tfor t in s%d {\n\t\tConsole.Println(t)\n\t}\n", i, r.IntN(12), g.text(), i)
		fmt.Fprintf(&b, "\tConsole.Println(\"\\(area(mk(%d)))\")\n", r.IntN(20))
	}
	fmt.Fprintf(&b, "\tConsole.Println(fromLets(%d))\n", r.IntN(2))
	fmt.Fprintf(&b, "\tlet ls = lits(%d)\n\tfor v in ls {\n\t\tConsole.Println(\"\\(v)\")\n\t}\n\tConsole.Println(show(recs(%d)))\n", r.IntN(9), r.IntN(9))
	// A node of run-time data is no markup in a built program: html's
	// inFold is false there, whatever the fold's evaluator answers.
	fmt.Fprintf(&b, "\tlet nw = noisy(%d)\n\tlet w = \"w\\(nw)\"\n\tlet pw = html.P([]html.Child{html.Text(w)})\n\tlet sw = html.Static(pw)\n\tlet tw = html.Static(html.Text(w))\n\tConsole.Println(\"\\(sw) \\(tw)\")\n", r.IntN(9))
	fmt.Fprintf(&b, "\tConsole.Println(\"\\(spin(%d))\")\n", 2_000_000+r.IntN(1000))
	defect := seed%2 == 0
	idx := r.IntN(3)
	if defect {
		idx = 3 + r.IntN(4)
	}
	fmt.Fprintf(&b, "\tConsole.Println(\"\\(pick([]int{4, 5, 6}, %d))\")\n", idx)
	b.WriteString("}\n")
	return b.String(), defect
}

const foldRowDecls = `type FoldNumber { N int }
type FoldWord { Word string }
type FoldRow = FoldNumber | FoldWord

func foldRowLabel(row FoldRow) string {
	match row {
	case FoldNumber as n:
		"number \(n.N)"
	case FoldWord as w:
		"word \(w.Word)"
	}
}

func foldRowJSON(text string) string {
	let v = json.Decode(json.Codec[FoldNumber | FoldWord]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case WireUnknownTag as e:
		return "json tag \(e.Tag)"
	case _:
		return "json failure"
	}
	foldRowLabel(v)
}

func foldRowTOML(text string) string {
	let v = toml.Decode(json.Codec[FoldNumber | FoldWord]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case WireUnknownTag as e:
		return "toml tag \(e.Tag)"
	case _:
		return "toml failure"
	}
	foldRowLabel(v)
}

func foldRowForward[T](x T, stop bool) T fails FoldNumber {
	if stop {
		return FoldNumber{N: -1}
	}
	return x
}

func foldRowGeneric(n int, stop bool) string {
	let v = foldRowForward(FoldNumber{N: n}, stop) catch {
	case FoldNumber as f:
		return "caught \(f.N)"
	}
	"returned \(v.N)"
}

`

func writeFoldRowCases(b *strings.Builder, r *rand.Rand) {
	n := r.IntN(2001) - 1000
	word := fmt.Sprintf("maple-%d", r.IntN(1000))
	for _, row := range []struct{ call, text string }{
		{"foldRowJSON", fmt.Sprintf(`{"_tag":"FoldNumber","n":%d}`, n)},
		{"foldRowJSON", fmt.Sprintf(`{"word":%q,"_tag":"FoldWord"}`, word)},
		{"foldRowJSON", `{"_tag":"Foreign"}`},
		{"foldRowTOML", fmt.Sprintf("_tag = \"FoldNumber\"\nn = %d\n", n)},
		{"foldRowTOML", fmt.Sprintf("_tag = \"FoldWord\"\nword = %q\n", word)},
		{"foldRowTOML", "_tag = \"Foreign\"\n"},
	} {
		fmt.Fprintf(b, "\tConsole.Println(%s(%s))\n", row.call, strconv.Quote(row.text))
	}
	fmt.Fprintf(b, "\tConsole.Println(foldRowGeneric(%d, false))\n\tConsole.Println(foldRowGeneric(%d, true))\n", n, n)
}

// cfGen writes GenerateConstFold's page trees.
type cfGen struct{ r *rand.Rand }

// cfTexts are the texts of the trees: markup's special characters, quotes,
// a tab, non-ASCII.
var cfTexts = []string{"plain", "a <b> & c", "\"quoted\" 'single'", "tab\there", "é ß Σ", "", "x&amp;y", "</p>"}

func (g *cfGen) text() string { return strconv.Quote(cfTexts[g.r.IntN(len(cfTexts))]) }

// tree is a node expression of depth at most d; dyn lets it read s and xs.
func (g *cfGen) tree(d int, dyn bool) string {
	leaf := []string{
		"html.Text(" + g.text() + ")",
		"html.Int(" + strconv.Itoa(g.r.IntN(2000)-1000) + ")",
		"html.Raw(html.Trust(" + g.text() + "))",
		"html.Br([]html.Attr{})",
		"html.Img([]html.Attr{html.Alt(" + g.text() + "), html.Src(html.LocalURL(" + g.text() + "))})",
	}
	if dyn {
		leaf = append(leaf, "html.Text(s)", "html.Each(xs, html.Text)", "html.Int(len(xs))")
	}
	if d == 0 || g.r.IntN(4) == 0 {
		return leaf[g.r.IntN(len(leaf))]
	}
	if g.r.IntN(6) == 0 {
		return fmt.Sprintf("html.When(%v, %s)", g.r.IntN(2) == 0, g.tree(d-1, dyn))
	}
	tags := []string{"Div", "P", "Li", "Ul", "Section", "Span", "Strong", "A"}
	attrs := []string{
		"html.Class(" + g.text() + ")",
		"html.Aria(\"label\", " + g.text() + ")",
		"html.Href(html.LocalURL(" + g.text() + "))",
		"html.Datetime(\"2026-11-17\")",
	}
	var kids []string
	for n := g.r.IntN(3); n > 0; n-- {
		kids = append(kids, attrs[g.r.IntN(len(attrs))])
	}
	for n := g.r.IntN(4); n > 0; n-- {
		kids = append(kids, g.tree(d-1, dyn))
	}
	return "html." + tags[g.r.IntN(len(tags))] + "([]html.Child{" + strings.Join(kids, ", ") + "})"
}

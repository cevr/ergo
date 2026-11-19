package irspike

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// GenerateTemplate is a program of templates (CF2): std html pages whose
// markup is partly constants and partly data the program has only when it
// runs (holes). A page function takes strings, a list, an int and a
// record, and reads them in every place a page may: a text, an attribute
// value, an attribute name and a boolean attribute from data, a link, an
// Each, an Int, a When on a condition, an if as a child, a component of
// the program's own, a Raw of trusted data, a string built with +, a
// Template, a void element with data attributes, and an impure call
// (noisy, which prints) inside a text, so the order the holes run in
// prints. main calls each page with data it computes, and renders some
// pages inside a document.
func GenerateTemplate(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x2c1b3c6d5e4f7a89))
	g := &tmGen{cfGen: cfGen{r: r}}
	var b strings.Builder
	b.WriteString("import html\n\n")
	b.WriteString("type Post { Slug string, Title string, N int }\n\n")
	b.WriteString("func noisy(s string) string needs Console {\n\tConsole.Println(\"noisy \" + s)\n\ts + \"~\"\n}\n\n")
	fmt.Fprintf(&b, "func comp(t string, k int) html.Node {\n\t%s\n}\n\n", g.page(2, "t", "k"))
	pages := 2 + r.IntN(3)
	for i := 0; i < pages; i++ {
		fmt.Fprintf(&b, "func page%d(s string, u string, xs []string, n int, p Post) html.Node needs Console {\n\t%s\n}\n\n", i, g.page(3, "", ""))
	}
	b.WriteString("func main() needs Console {\n")
	b.WriteString("\tlet mut xs = []string{}\n")
	fmt.Fprintf(&b, "\tfor i in 0..%d {\n\t\txs = xs.Append(%s + \"\\(i)\")\n\t}\n", r.IntN(4), g.text())
	for i := 0; i < pages; i++ {
		for k := 0; k < 2; k++ {
			fmt.Fprintf(&b, "\tlet s%d_%d = noisy(%s)\n\tlet u%d_%d = %s + \"\\(len(xs) + %d)\"\n", i, k, g.text(), i, k, g.text(), k)
			fmt.Fprintf(&b, "\tlet p%d_%d = Post{Slug: s%d_%d + \"/x\", Title: u%d_%d, N: len(xs) * %d}\n", i, k, i, k, i, k, r.IntN(5))
			call := fmt.Sprintf("page%d(s%d_%d, u%d_%d, xs, len(xs) + %d, p%d_%d)", i, i, k, i, k, r.IntN(4), i, k)
			if r.IntN(3) == 0 {
				call = "html.Doc(" + call + ")"
			}
			fmt.Fprintf(&b, "\tConsole.Println(html.Render(%s))\n", call)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// tmGen writes GenerateTemplate's pages.
type tmGen struct{ cfGen }

// page is a node expression of depth at most d over the data in scope:
// a component's t and k when they are named, else a page's parameters.
func (g *tmGen) page(d int, t, k string) string {
	r := g.r
	str := func() string {
		if t != "" {
			return []string{t, t + " + " + g.text(), g.text()}[r.IntN(3)]
		}
		return []string{"s", "u", "p.Title", "s + " + g.text() + " + u", g.text(), "p.Slug"}[r.IntN(6)]
	}
	num := func() string {
		if k != "" {
			return k
		}
		return []string{"n", "p.N", "len(xs)"}[r.IntN(3)]
	}
	leaf := []string{
		"html.Text(" + str() + ")",
		"html.Text(" + g.text() + ")",
		"html.Int(" + num() + ")",
		"html.Raw(html.Trust(" + str() + "))",
		"html.Br([]html.Attr{html.Id(" + str() + "), html.Flag(" + str() + ")})",
		"html.Img([]html.Attr{html.Alt(" + str() + "), html.Src(html.LocalURL(" + g.text() + "))})",
	}
	if t == "" {
		leaf = append(leaf,
			"html.Each(xs, html.Text)",
			"html.Text(noisy("+str()+"))",
			"comp("+str()+", "+num()+")",
			"html.Template(html.Trust(\"<i\"), []html.Child{html.Class("+str()+"), html.Text("+str()+")}, html.Trust(\">\"), html.Trust(\"</i>\"))",
		)
	}
	if d == 0 || r.IntN(5) == 0 {
		return leaf[r.IntN(len(leaf))]
	}
	switch r.IntN(8) {
	case 0:
		return fmt.Sprintf("html.When(%s > %d, %s)", num(), r.IntN(3), g.page(d-1, t, k))
	case 1:
		return fmt.Sprintf("if %s %% 2 == 0 { %s } else { %s }", num(), g.page(d-1, t, k), g.page(d-1, t, k))
	}
	tags := []string{"Div", "P", "Li", "Ul", "Section", "Span", "Strong", "A"}
	attrs := []string{
		"html.Class(" + str() + ")",
		"html.Class(" + g.text() + ")",
		"html.Aria(\"label\", " + str() + ")",
		"html.Attribute(" + str() + ", " + str() + ")",
		"html.Flag(" + str() + ")",
		"html.Href(html.LocalURL(" + strconv.Quote("/") + " + " + str() + "))",
		"html.Datetime(\"2026-11-17\")",
	}
	var kids []string
	for n := r.IntN(3); n > 0; n-- {
		kids = append(kids, attrs[r.IntN(len(attrs))])
	}
	for n := 1 + r.IntN(3); n > 0; n-- {
		kids = append(kids, g.page(d-1, t, k))
	}
	r.Shuffle(len(kids), func(i, j int) { kids[i], kids[j] = kids[j], kids[i] })
	return "html." + tags[r.IntN(len(tags))] + "([]html.Child{" + strings.Join(kids, ", ") + "})"
}

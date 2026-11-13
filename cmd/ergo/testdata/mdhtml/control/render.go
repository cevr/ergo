package main

import (
	"strconv"
	"strings"
)

// The output: the blocks and inlines as a tree of HTML nodes, then the
// tree as text with CommonMark's line breaks between blocks.

type hkind uint8

const (
	hElem hkind = iota
	hText
	hRaw      // inline raw HTML
	hRawBlock // an HTML block
)

type hattr struct{ name, value string }

type hnode struct {
	kind  hkind
	tag   string
	attrs []hattr
	kids  []hnode
	s     string
}

func el(tag string, kids []hnode) hnode { return hnode{kind: hElem, tag: tag, kids: kids} }

const omitted = "<!-- raw HTML omitted -->"

type converter struct {
	p         *parser
	q         *inlineParser
	unsafe    bool
	highlight bool
}

// convert is the HTML tree of the document src.
func convert(src string, unsafe, highlight bool) []hnode {
	p := parseBlocks(src)
	q := &inlineParser{refs: p.refs, top: -1}
	for id := range p.blocks {
		if k := p.blocks[id].kind; k == bPara || k == bHeading {
			p.blocks[id].inlines = q.parse(p.blocks[id].content)
		}
	}
	c := &converter{p: p, q: q, unsafe: unsafe, highlight: highlight}
	return c.children(0, false)
}

func (c *converter) children(id int, tight bool) []hnode {
	var out []hnode
	for k := c.p.blocks[id].first; k >= 0; k = c.p.blocks[k].next {
		b := c.p.blocks[k]
		if b.kind == bPara && tight {
			out = append(out, c.inlines(b.inlines)...)
			continue
		}
		out = append(out, c.block(k))
	}
	return out
}

// safeURL is url, or "" when url is a script or data that is not an
// image, as cmark's safe mode writes it.
func (c *converter) safeURL(url string) string {
	if c.unsafe {
		return url
	}
	l := strings.ToLower(url)
	if strings.HasPrefix(l, "javascript:") || strings.HasPrefix(l, "vbscript:") || strings.HasPrefix(l, "file:") {
		return ""
	}
	if strings.HasPrefix(l, "data:") {
		for _, img := range []string{"image/png", "image/gif", "image/jpeg", "image/webp"} {
			if strings.HasPrefix(l[5:], img) {
				return url
			}
		}
		return ""
	}
	return url
}

func (c *converter) block(id int) hnode {
	b := c.p.blocks[id]
	switch b.kind {
	case bQuote:
		return el("blockquote", c.children(id, false))
	case bList:
		var items []hnode
		for k := b.first; k >= 0; k = c.p.blocks[k].next {
			items = append(items, el("li", c.children(k, b.list.tight)))
		}
		if !b.list.ordered {
			return el("ul", items)
		}
		n := el("ol", items)
		if b.list.start != 1 {
			n.attrs = []hattr{{"start", strconv.Itoa(b.list.start)}}
		}
		return n
	case bPara:
		return el("p", c.inlines(b.inlines))
	case bHeading:
		return el("h"+strconv.Itoa(b.level), c.inlines(b.inlines))
	case bRule:
		return el("hr", nil)
	case bCode:
		lang := b.info
		if i := strings.IndexAny(lang, " \t\n\r\f\v"); i >= 0 {
			lang = lang[:i]
		}
		code := el("code", c.highlightCode(lang, b.literal))
		if lang != "" {
			code.attrs = []hattr{{"class", "language-" + lang}}
		}
		return el("pre", []hnode{code})
	case bHTML:
		if !c.unsafe {
			return hnode{kind: hRawBlock, s: omitted}
		}
		return hnode{kind: hRawBlock, s: b.literal}
	}
	return el("div", nil)
}

var goKeywords = []string{"break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough", "for", "func", "go", "goto", "if", "import", "interface", "map", "package", "range", "return", "select", "struct", "switch", "type", "var"}

func wordByte(c byte) bool { return asciiAlnum(c) || c == '_' }

// highlightCode is a code block's text; with -highlight, Go's keywords in
// a block of language go are <span class="kw">.
func (c *converter) highlightCode(lang, code string) []hnode {
	if !c.highlight || lang != "go" {
		return []hnode{{kind: hText, s: code}}
	}
	var out []hnode
	for i := 0; i < len(code); {
		start := i
		word := wordByte(code[i])
		for i < len(code) && wordByte(code[i]) == word {
			i++
		}
		piece := code[start:i]
		if word && oneOf(piece, goKeywords) {
			out = append(out, hnode{kind: hElem, tag: "span", attrs: []hattr{{"class", "kw"}}, kids: []hnode{{kind: hText, s: piece}}})
		} else {
			out = append(out, hnode{kind: hText, s: piece})
		}
	}
	return out
}

func (c *converter) inlines(id int) []hnode {
	var out []hnode
	for k := c.q.nodes[id].first; k >= 0; k = c.q.nodes[k].next {
		out = append(out, c.inline(k))
	}
	return out
}

func (c *converter) inline(id int) hnode {
	n := c.q.nodes[id]
	switch n.kind {
	case iText:
		return hnode{kind: hText, s: n.text}
	case iSoft:
		return hnode{kind: hText, s: "\n"}
	case iHard:
		return el("br", nil)
	case iCode:
		return el("code", []hnode{{kind: hText, s: n.text}})
	case iEmph:
		return el("em", c.inlines(id))
	case iStrong:
		return el("strong", c.inlines(id))
	case iLink:
		a := el("a", c.inlines(id))
		a.attrs = []hattr{{"href", c.safeURL(n.dest)}}
		if n.title != "" {
			a.attrs = append(a.attrs, hattr{"title", n.title})
		}
		return a
	case iImage:
		var alt strings.Builder
		c.plain(&alt, id)
		img := el("img", nil)
		img.attrs = []hattr{{"src", c.safeURL(n.dest)}, {"alt", alt.String()}}
		if n.title != "" {
			img.attrs = append(img.attrs, hattr{"title", n.title})
		}
		return img
	case iHTML:
		if !c.unsafe {
			return hnode{kind: hRaw, s: omitted}
		}
		return hnode{kind: hRaw, s: n.text}
	}
	return hnode{kind: hText}
}

// plain writes the text of the inlines below id, for an image's alt.
func (c *converter) plain(b *strings.Builder, id int) {
	for k := c.q.nodes[id].first; k >= 0; k = c.q.nodes[k].next {
		n := c.q.nodes[k]
		switch n.kind {
		case iText, iCode, iHTML:
			b.WriteString(n.text)
		case iSoft, iHard:
			b.WriteString("\n")
		default:
			c.plain(b, k)
		}
	}
}

// ---- writing ----

func escape(s string) string {
	if strings.IndexAny(s, "&<>\"") < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

type writer struct {
	b  strings.Builder
	nl bool // the last thing written ends a line (or nothing is written)
}

func (w *writer) lit(s string) {
	if s == "" {
		return
	}
	w.b.WriteString(s)
	w.nl = s[len(s)-1] == '\n'
}

func (w *writer) cr() {
	if !w.nl {
		w.lit("\n")
	}
}

func (w *writer) open(n hnode, selfClose bool) {
	w.lit("<" + n.tag)
	for _, a := range n.attrs {
		w.lit(" " + a.name + "=\"" + escape(a.value) + "\"")
	}
	if selfClose {
		w.lit(" />")
	} else {
		w.lit(">")
	}
}

func (w *writer) kids(n hnode) {
	for _, k := range n.kids {
		w.node(k)
	}
}

func (w *writer) node(n hnode) {
	switch n.kind {
	case hText:
		w.lit(escape(n.s))
		return
	case hRaw:
		w.lit(n.s)
		return
	case hRawBlock:
		w.cr()
		w.lit(n.s)
		w.cr()
		return
	}
	switch n.tag {
	case "p", "h1", "h2", "h3", "h4", "h5", "h6", "pre":
		w.cr()
		w.open(n, false)
		w.kids(n)
		w.lit("</" + n.tag + ">")
		w.cr()
	case "blockquote", "ul", "ol":
		w.cr()
		w.open(n, false)
		w.cr()
		w.kids(n)
		w.cr()
		w.lit("</" + n.tag + ">")
		w.cr()
	case "li":
		w.open(n, false)
		w.kids(n)
		w.lit("</li>")
		w.cr()
	case "hr":
		w.cr()
		w.open(n, true)
		w.cr()
	case "br":
		w.open(n, true)
		w.cr()
	case "img":
		w.open(n, true)
	default:
		w.open(n, false)
		w.kids(n)
		w.lit("</" + n.tag + ">")
	}
}

// render is the HTML text of ns.
func render(ns []hnode) string {
	w := &writer{nl: true}
	for _, n := range ns {
		w.node(n)
	}
	return w.b.String()
}

package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdHTMLLawsProbe restates each law of std html (ledger runner
// TestStdHTMLLawsRun) as a property test of its own name
// (stdLawsTestName): the laws' helpers of internal/sketch/std/html/tree.ergo
// over the package's exported functions, each law a function the test
// asserts. A law changed in source fails TestStdHTMLLawsRun until this
// restatement changes with it.
const stdHTMLLawsProbe = `import html
import strings

type tnode = tel{tag string, attrs []tattr, kids []tnode} | tvoid{tag string, attrs []tattr} | tscript{attrs []tattr, body string} | ttext{s string}

type tattr { name string, value string, bare bool }

type trng { x int }

func tdraw(r mut trng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

func pieces() []string {
	[]string{"a", "Zz", "<", ">", "&", "\"", "'", "&amp;", "&lt;", "</style>", "</script>", "<script>alert(1)</script>", "javascript:", " ", "=", "/>", "-->", "é", "\t", "{\"@type\":\"Post\"}"}
}

func tstring(r mut trng) string {
	let mut b = strings.NewBuilder()
	for _ in 0..tdraw(r, 5) {
		let ps = pieces()
		b.WriteString(ps[tdraw(r, len(ps))])
	}
	b.String()
}

type built { model []tnode, kids []html.Child }

func addText(m []tnode, s string) []tnode {
	if s == "" {
		return m
	}
	if len(m) > 0 {
		match m[len(m)-1] {
		case ttext as t:
			let mut out = m[0 : len(m)-1]
			out = out.Append(ttext{s: t.s + s})
			return out
		case tel, tvoid, tscript:
		}
	}
	m.Append(ttext{s: s})
}

func tname(s string) string {
	if s == "" {
		return "data-"
	}
	let mut b = strings.NewBuilder()
	for i in 0..len(s) {
		let c = s[i]
		let ok = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':'
		b.WriteByte(if ok { c } else { '-' })
	}
	b.String()
}

func tattrs(r mut trng, unsafe mut int) ([]tattr, []html.Attr) {
	let mut model = []tattr{}
	let mut out = []html.Attr{}
	for _ in 0..tdraw(r, 4) {
		let v = tstring(r)
		let n = tstring(r)
		let k = tdraw(r, 7)
		let (m, a) = if k == 0 {
			(tattr{name: "class", value: v, bare: false}, html.Class(v))
		} else if k == 1 {
			(tattr{name: "id", value: v, bare: false}, html.Id(v))
		} else if k == 2 {
			(tattr{name: "title", value: v, bare: false}, html.TitleAttr(v))
		} else if k == 3 {
			(tattr{name: tname(n), value: v, bare: false}, html.Attribute(n, v))
		} else if k == 4 {
			(tattr{name: tname(n), value: "", bare: true}, html.Flag(n))
		} else if k == 5 {
			(tattr{name: tname("data-" + n), value: v, bare: false}, html.Data(n, v))
		} else {
			let u = html.ParseURL(v) catch {
			case html.UnsafeURL:
				html.LocalURL(v)
			}
			let sc = html.Scheme(html.URLText(u))
			if !(sc == "" || sc == "http" || sc == "https" || sc == "mailto" || sc == "tel") {
				*unsafe = *unsafe + 1
			}
			(tattr{name: "href", value: html.URLText(u), bare: false}, html.Href(u))
		}
		model = model.Append(m)
		out = out.Append(a)
	}
	(model, out)
}

func tkids(r mut trng, d int, unsafe mut int) built {
	let mut model = []tnode{}
	let mut kids = []html.Child{}
	for _ in 0..tdraw(r, 5) {
		let k = if d > 3 { tdraw(r, 4) } else { tdraw(r, 8) }
		if k == 0 {
			let s = tstring(r)
			model = addText(model, s)
			kids = kids.Append(html.Text(s))
		} else if k == 1 {
			let n = tdraw(r, 2001) - 1000
			model = addText(model, "\(n)")
			kids = kids.Append(html.Int(n))
		} else if k == 2 {
			let show = tdraw(r, 2) == 0
			let s = tstring(r)
			if show {
				model = addText(model, s)
			}
			kids = kids.Append(html.When(show, html.Text(s)))
		} else if k == 3 {
			let (m, as2) = tattrs(r, unsafe)
			let tags = []string{"br", "img", "input", "hr", "meta"}
			let t = tdraw(r, len(tags))
			model = model.Append(tvoid{tag: tags[t], attrs: m})
			kids = kids.Append([]html.Node{html.Br(as2), html.Img(as2), html.Input(as2), html.Hr(as2), html.Meta(as2)}[t])
		} else if k == 4 {
			let items = []string{tstring(r), tstring(r)}
			for s in items {
				model = addText(model, s)
			}
			kids = kids.Append(html.Each(items, html.Text))
		} else if k == 5 {
			let (m, as2) = tattrs(r, unsafe)
			let body = []string{"{\"@context\":\"https://schema.org\"}", "let x = 1 < 2 && \"a\"", ""}[tdraw(r, 3)]
			model = model.Append(tscript{attrs: m, body: body})
			kids = kids.Append(html.Script(as2, html.Trust(body)))
		} else {
			let (m, as2) = tattrs(r, unsafe)
			let inner = tkids(r, d+1, unsafe)
			let tags = []string{"div", "p", "span", "li", "td", "title", "textarea", "a"}
			let t = tdraw(r, len(tags))
			let mut all = []html.Child{}
			// attributes before, between and after the children: they are
			// written in the start tag in their order.
			let cut = if len(as2) == 0 { 0 } else { tdraw(r, len(as2)+1) }
			for a in as2[0:cut] {
				all = all.Append(a)
			}
			for c in inner.kids {
				all = all.Append(c)
			}
			for a in as2[cut:len(as2)] {
				all = all.Append(a)
			}
			let n = []html.Node{html.Div(all), html.P(all), html.Span(all), html.Li(all), html.Td(all), html.Title(all), html.Textarea(all), html.A(all)}[t]
			model = model.Append(tel{tag: tags[t], attrs: m, kids: inner.model})
			kids = kids.Append(n)
		}
	}
	built{model: model, kids: kids}
}

func unescape(s string) string {
	let mut b = strings.NewBuilder()
	let mut i = 0
	for i < len(s) {
		let rest = strings.Slice(s, i, len(s))
		if strings.HasPrefix(rest, "&amp;") {
			b.WriteString("&")
			i = i + 5
		} else if strings.HasPrefix(rest, "&lt;") {
			b.WriteString("<")
			i = i + 4
		} else if strings.HasPrefix(rest, "&gt;") {
			b.WriteString(">")
			i = i + 4
		} else if strings.HasPrefix(rest, "&#34;") {
			b.WriteString("\"")
			i = i + 5
		} else if strings.HasPrefix(rest, "&#39;") {
			b.WriteString("'")
			i = i + 5
		} else {
			b.WriteByte(s[i])
			i = i + 1
		}
	}
	b.String()
}

func inert(t string) bool {
	for i in 0..len(t) {
		let c = t[i]
		if c == '<' || c == '>' || c == '"' || c == '\'' {
			return false
		}
		if c == '&' {
			let rest = strings.Slice(t, i, len(t))
			if !(strings.HasPrefix(rest, "&amp;") || strings.HasPrefix(rest, "&lt;") || strings.HasPrefix(rest, "&gt;") || strings.HasPrefix(rest, "&#34;") || strings.HasPrefix(rest, "&#39;")) {
				return false
			}
		}
	}
	true
}

func refused(s string) bool {
	let _ = html.ParseURL(s) catch {
	case html.UnsafeURL:
		return true
	}
	false
}

type tparser { s string, i int, ok bool }

func tvoids() []string { []string{"br", "img", "input", "hr", "meta"} }

func ptext(p mut tparser) string {
	let start = p.i
	for p.i < len(p.s) && p.s[p.i] != '<' {
		p.i = p.i + 1
	}
	unescape(strings.Slice(p.s, start, p.i))
}

func pname(p mut tparser) string {
	let start = p.i
	for p.i < len(p.s) && p.s[p.i] != ' ' && p.s[p.i] != '=' && p.s[p.i] != '>' && p.s[p.i] != '/' && p.s[p.i] != '"' && p.s[p.i] != '\'' && p.s[p.i] != '<' {
		p.i = p.i + 1
	}
	if p.i == start {
		p.ok = false
	}
	strings.Slice(p.s, start, p.i)
}

func pattrs(p mut tparser) []tattr {
	let mut out = []tattr{}
	let mut more = true
	for more && p.ok && p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i = p.i + 1
		let n = pname(p)
		if p.i < len(p.s) && p.s[p.i] == '=' {
			if p.i+1 >= len(p.s) || p.s[p.i+1] != '"' {
				p.ok = false
				more = false
			} else {
				p.i = p.i + 2
				let start = p.i
				for p.i < len(p.s) && p.s[p.i] != '"' {
					p.i = p.i + 1
				}
				let v = strings.Slice(p.s, start, p.i)
				if p.i >= len(p.s) || !inert(v) {
					p.ok = false
				}
				p.i = p.i + 1
				out = out.Append(tattr{name: n, value: unescape(v), bare: false})
			}
		} else {
			out = out.Append(tattr{name: n, value: "", bare: true})
		}
	}
	out
}

func pkids(p mut tparser, close string) []tnode {
	let mut out = []tnode{}
	let mut done = false
	for !done && p.ok {
		if p.i >= len(p.s) {
			if close != "" {
				p.ok = false
			}
			done = true
		} else if p.s[p.i] != '<' {
			out = out.Append(ttext{s: ptext(p)})
		} else if strings.HasPrefix(strings.Slice(p.s, p.i, len(p.s)), "</") {
			let end = "</" + close + ">"
			if close != "" && strings.HasPrefix(strings.Slice(p.s, p.i, len(p.s)), end) {
				p.i = p.i + len(end)
			} else {
				p.ok = false
			}
			done = true
		} else {
			out = out.Append(pelement(p))
		}
	}
	out
}

func pelement(p mut tparser) tnode {
	p.i = p.i + 1
	let tag = pname(p)
	let as2 = pattrs(p)
	let rest = strings.Slice(p.s, p.i, len(p.s))
	if strings.HasPrefix(rest, "/>") {
		p.i = p.i + 2
		let mut known = false
		for v in tvoids() {
			if v == tag {
				known = true
			}
		}
		if !known {
			p.ok = false
		}
		return tvoid{tag: tag, attrs: as2}
	}
	if !strings.HasPrefix(rest, ">") {
		p.ok = false
		return ttext{s: ""}
	}
	p.i = p.i + 1
	for v in tvoids() {
		if v == tag {
			p.ok = false
		}
	}
	if tag == "script" {
		let start = p.i
		for p.i < len(p.s) && !strings.HasPrefix(strings.Slice(p.s, p.i, len(p.s)), "</script>") {
			p.i = p.i + 1
		}
		if p.i >= len(p.s) {
			p.ok = false
		}
		let body = strings.Slice(p.s, start, p.i)
		p.i = p.i + 9
		return tscript{attrs: as2, body: body}
	}
	tel{tag: tag, attrs: as2, kids: pkids(p, tag)}
}

func tparse(s string) Option[[]tnode] {
	let mut p = tparser{s: s, i: 0, ok: true}
	let out = pkids(&p, "")
	if p.ok { Some[[]tnode]{Value: out} } else { None }
}

func sameAttrs(a []tattr, b []tattr) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x in a {
		if x.name != b[i].name || x.value != b[i].value || x.bare != b[i].bare {
			return false
		}
	}
	true
}

func sameNodes(a []tnode, b []tnode) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x in a {
		let ok = match x {
		case tel as e:
			match b[i] {
			case tel as f:
				e.tag == f.tag && sameAttrs(e.attrs, f.attrs) && sameNodes(e.kids, f.kids)
			case tvoid, tscript, ttext:
				false
			}
		case tvoid as e:
			match b[i] {
			case tvoid as f:
				e.tag == f.tag && sameAttrs(e.attrs, f.attrs)
			case tel, tscript, ttext:
				false
			}
		case tscript as e:
			match b[i] {
			case tscript as f:
				e.body == f.body && sameAttrs(e.attrs, f.attrs)
			case tel, tvoid, ttext:
				false
			}
		case ttext as e:
			match b[i] {
			case ttext as f:
				e.s == f.s
			case tel, tvoid, tscript:
				false
			}
		}
		if !ok {
			return false
		}
	}
	true
}

func lawTextIsInert(s string, t string) bool {
	let x = s + "<a href=\"x\" title='y'>&amp;</a>" + t
	let e = html.Escape(x)
	let text = html.Render(html.Text(x))
	let attr = html.Render(html.Span([]html.Child{html.Class(x)}))
	inert(e) && unescape(e) == x && text == e && attr == "<span class=\""+e+"\"></span>" && html.Escape(s+">") == html.Escape(s)+"&gt;"
}

func lawRenderIsAppend(a string, c string, k string) bool {
	let ta = html.Render(html.Text(a))
	let tc = html.Render(html.Text(c))
	let ek = html.Escape(k)
	let list = html.Render(html.Each([]string{a, c}, html.Text))
	let div = html.Render(html.Div([]html.Child{html.Text(a), html.Class(k), html.Text(c), html.Id(k)}))
	let br = html.Render(html.Br([]html.Attr{html.Class(k)}))
	let doc = html.Render(html.Doc(html.P([]html.Child{html.When(a == c, html.Text(a)), html.Int(len(a))})))
	list == ta+tc && div == "<div class=\""+ek+"\" id=\""+ek+"\">"+ta+tc+"</div>" && br == "<br class=\""+ek+"\"/>" && doc == "<!DOCTYPE html><p>"+(if a == c { ta } else { "" })+"\(len(a))</p>"
}

func lawStaticIsItsTree(a string, c string, k string) bool {
	let kids = []html.Child{html.Text(a), html.Class(k), html.P([]html.Child{html.Text(c), html.Int(len(c))}), html.Br([]html.Attr{html.Id(k)}), html.Raw(html.Trust(c)), html.When(a == c, html.Text(k)), html.Script([]html.Attr{html.Type(k)}, html.Trust(c))}
	let mut late = []html.Child{}
	for x in kids {
		match x {
		case html.Attr as at:
			late = late.Append(at)
		case html.Node as n:
			late = late.Append(html.Each([]int{0}, func(i int) html.Node { n }))
		}
	}
	let tree = html.Div(kids)
	let deferred = html.Div(late)
	let frozen = html.Raw(html.Trust(html.Render(tree)))
	html.Static(frozen) && !html.Static(deferred) && html.Render(frozen) == html.Render(tree) && html.Render(tree) == html.Render(deferred) && html.Render(html.Doc(frozen)) == html.Render(html.Doc(deferred)) && html.Render(html.Doc(tree)) == html.Render(html.Doc(deferred))
}

func lawTemplateIsItsTree(a string, c string, k string) bool {
	let kids = []html.Child{html.Text(a), html.Class(k), html.P([]html.Child{html.Text(c), html.Int(len(c))}), html.Flag(c), html.Br([]html.Attr{html.Id(k), html.Flag(a)}), html.Raw(html.Trust(c)), html.When(a == c, html.Text(k))}
	let inner = html.Template(html.Trust("<p"), []html.Child{html.Text(c), html.Id(a)}, html.Trust(">"), html.Trust("</p>"))
	let t = html.Template(html.Trust("<div"), kids.Append(inner), html.Trust(">"), html.Trust("</div>"))
	let tree = html.Div(kids.Append(html.P([]html.Child{html.Text(c), html.Id(a)})))
	let br = html.Template(html.Trust("<br"), []html.Child{html.Id(k), html.Flag(a)}, html.Trust("/>"), html.Trust(""))
	!html.Static(t) && html.Render(t) == html.Render(tree) && html.Render(br) == html.Render(html.Br([]html.Attr{html.Id(k), html.Flag(a)})) && html.Render(html.Doc(t)) == html.Render(html.Doc(tree)) && html.Render(html.Template(html.Trust("<!DOCTYPE html>"), []html.Child{tree}, html.Trust(""), html.Trust(""))) == html.Render(html.Doc(tree))
}

func lawLinksRefuseScripts(pick int, s string) bool {
	let k = ((pick % 6) + 6) % 6
	let bad = []string{"javascript:", "JaVaScRiPt:", " \t java\tscript:", "jav\nascript:", "data:text/html,", "VBScript:"}[k]
	let good = []string{"/", "https://", "http://", "mailto:", "tel:", "#"}[k]
	let kept = html.ParseURL(good + "x" + s) catch {
	case html.UnsafeURL:
		return false
	}
	let local = html.URLText(html.LocalURL(s))
	let near = html.URLText(html.LocalURL(strings.Slice("\t//\\/", k%3, 5) + s))
	refused(bad+s) && html.URLText(kept) == good+"x"+s && html.Scheme(local) == "" && strings.HasPrefix(local, "/") && !strings.HasPrefix(local, "//") && !strings.HasPrefix(local, "/\\") && !strings.HasPrefix(near, "//") && !strings.HasPrefix(near, "/\\")
}

func lawRenderParsesBack(seed int) bool {
	let mut r = trng{x: (seed%2147483648 + 2147483648) % 2147483648}
	let mut unsafe = 0
	let b = tkids(&r, 0, &unsafe)
	let markup = html.Render(html.Div(b.kids))
	let doc = html.Render(html.Doc(html.Body(b.kids)))
	let want = []tnode{tel{tag: "div", attrs: []tattr{}, kids: b.model}}
	let wantDoc = []tnode{tel{tag: "body", attrs: []tattr{}, kids: b.model}}
	let got = tparse(markup)
	let gotDoc = tparse(strings.TrimPrefix(doc, "<!DOCTYPE html>"))
	let back = match got {
	case Some[[]tnode] as t:
		sameNodes(t.Value, want)
	case None:
		false
	}
	let backDoc = match gotDoc {
	case Some[[]tnode] as t:
		sameNodes(t.Value, wantDoc)
	case None:
		false
	}
	back && backDoc && strings.HasPrefix(doc, "<!DOCTYPE html>") && unsafe == 0
}

test HtmlTextIsInert(s string, t string) {
	assert lawTextIsInert(s, t)
}

test HtmlRenderIsAppend(a string, c string, k string) {
	assert lawRenderIsAppend(a, c, k)
}

test HtmlStaticIsItsTree(a string, c string, k string) {
	assert lawStaticIsItsTree(a, c, k)
}

test HtmlTemplateIsItsTree(a string, c string, k string) {
	assert lawTemplateIsItsTree(a, c, k)
}

test HtmlLinksRefuseScripts(pick int, s string) {
	assert lawLinksRefuseScripts(pick, s)
}

test HtmlRenderParsesBack(seed int) {
	assert lawRenderParsesBack(seed)
}
`

// stdHTMLLawStatements is the statement of each html law, as the ledger
// writes it, by ledger key.
var stdHTMLLawStatements = map[string]string{
	"html TextIsInert":        `TextIsInert(s string, t string) { let x = s + "<a href=\"x\" title='y'>&amp;</a>" + t let e = Escape(x) let text = Render(Text(x)) let attr = Render(Span([]Child{Class(x)})) inert(e) && unescape(e) == x && text == e && attr == "<span class=\""+e+"\"></span>" && Escape(s+">") == Escape(s)+"&gt;" }`,
	"html RenderIsAppend":     `RenderIsAppend(a string, c string, k string) { let ta = Render(Text(a)) let tc = Render(Text(c)) let ek = Escape(k) let list = Render(Each([]string{a, c}, Text)) let div = Render(Div([]Child{Text(a), Class(k), Text(c), Id(k)})) let br = Render(Br([]Attr{Class(k)})) let doc = Render(Doc(P([]Child{When(a == c, Text(a)), Int(len(a))}))) list == ta+tc && div == "<div class=\""+ek+"\" id=\""+ek+"\">"+ta+tc+"</div>" && br == "<br class=\""+ek+"\"/>" && doc == "<!DOCTYPE html><p>"+(if a == c { ta } else { "" })+"\(len(a))</p>" }`,
	"html StaticIsItsTree":    `StaticIsItsTree(a string, c string, k string) { let kids = []Child{Text(a), Class(k), P([]Child{Text(c), Int(len(c))}), Br([]Attr{Id(k)}), Raw(Trust(c)), When(a == c, Text(k)), Script([]Attr{Type(k)}, Trust(c))} let mut late = []Child{} for x in kids { match x { case Attr as at: late = late.Append(at) case Node as n: late = late.Append(Each([]int{0}, func(i int) Node { n })) } } let tree = Div(kids) let deferred = Div(late) let frozen = Raw(Trust(Render(tree))) Static(frozen) && !Static(deferred) && Render(frozen) == Render(tree) && Render(tree) == Render(deferred) && Render(Doc(frozen)) == Render(Doc(deferred)) && Render(Doc(tree)) == Render(Doc(deferred)) }`,
	"html TemplateIsItsTree":  `TemplateIsItsTree(a string, c string, k string) { let kids = []Child{Text(a), Class(k), P([]Child{Text(c), Int(len(c))}), Flag(c), Br([]Attr{Id(k), Flag(a)}), Raw(Trust(c)), When(a == c, Text(k))} let inner = Template(Trust("<p"), []Child{Text(c), Id(a)}, Trust(">"), Trust("</p>")) let t = Template(Trust("<div"), kids.Append(inner), Trust(">"), Trust("</div>")) let tree = Div(kids.Append(P([]Child{Text(c), Id(a)}))) let br = Template(Trust("<br"), []Child{Id(k), Flag(a)}, Trust("/>"), Trust("")) !Static(t) && Render(t) == Render(tree) && Render(br) == Render(Br([]Attr{Id(k), Flag(a)})) && Render(Doc(t)) == Render(Doc(tree)) && Render(Template(Trust("<!DOCTYPE html>"), []Child{tree}, Trust(""), Trust(""))) == Render(Doc(tree)) }`,
	"html LinksRefuseScripts": `LinksRefuseScripts(pick int, s string) { let k = ((pick % 6) + 6) % 6 let bad = []string{"javascript:", "JaVaScRiPt:", " \t java\tscript:", "jav\nascript:", "data:text/html,", "VBScript:"}[k] let good = []string{"/", "https://", "http://", "mailto:", "tel:", "#"}[k] let kept = ParseURL(good + "x" + s) catch { case UnsafeURL: return false } let local = URLText(LocalURL(s)) let near = URLText(LocalURL(strings.Slice("\t//\\/", k%3, 5) + s)) refused(bad+s) && URLText(kept) == good+"x"+s && Scheme(local) == "" && strings.HasPrefix(local, "/") && !strings.HasPrefix(local, "//") && !strings.HasPrefix(local, "/\\") && !strings.HasPrefix(near, "//") && !strings.HasPrefix(near, "/\\") }`,
	"html RenderParsesBack":   `RenderParsesBack(seed int) { let mut r = trng{x: (seed%2147483648 + 2147483648) % 2147483648} let mut unsafe = 0 let b = tkids(&r, 0, &unsafe) let markup = Render(Div(b.kids)) let doc = Render(Doc(Body(b.kids))) let want = []tnode{tel{tag: "div", attrs: []tattr{}, kids: b.model}} let wantDoc = []tnode{tel{tag: "body", attrs: []tattr{}, kids: b.model}} let got = tparse(markup) let gotDoc = tparse(strings.TrimPrefix(doc, "<!DOCTYPE html>")) let back = match got { case Some[[]tnode] as t: sameNodes(t.Value, want) case None: false } let backDoc = match gotDoc { case Some[[]tnode] as t: sameNodes(t.Value, wantDoc) case None: false } back && backDoc && strings.HasPrefix(doc, "<!DOCTYPE html>") && unsafe == 0 }`,
}

// TestStdHTMLLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's html laws of this
// runner, each with the row's statement, and that each passed. The web0-*
// mutants of internal/sketch/std/html in testdata/mutants.json are its
// kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdHTMLLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdhtml", stdHTMLLawsProbe, stdHTMLLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

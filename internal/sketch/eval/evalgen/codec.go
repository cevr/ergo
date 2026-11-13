package evalgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// codec: a seeded Doc (the prelude's record of every codec part: a list of
// records with an int8, a tagged union, a Map of uint8, a tuple, an
// Optional and a Nullable field, a declared key and a capital run),
// encoded with std json, then decoded from texts made from its canonical
// text: as it is; reordered with white space, an unknown member and a
// repeated key (the later wins); with a member dropped; with a wrong
// kind, an out-of-range integer, a missing or unknown tag, a short or
// long tuple, or broken text. A json.Value decodes and re-encodes a
// seeded text. The model writes each expected line from the document's
// structure and the paths std json names.
func (g *mainGen) codec(k int) {
	g.kinds["codec"]++
	d := g.doc()
	g.line("")
	g.line("func s%d() needs Console {", k)
	g.line("\tlet d = %s", d.literal())
	g.line("\tConsole.Println(\"s%d enc \" + json.Encode(json.Codec[Doc]{MaxBytes: 0, MaxDepth: 0}, d))", k)
	fmt.Fprintf(&g.out, "s%d enc %s\n", k, d.canonical())
	show := func(v int, text, want string) {
		g.line("\tshowDoc(\"s%d v%d\", %s)", k, v, strconv.Quote(text))
		fmt.Fprintf(&g.out, "s%d v%d %s\n", k, v, want)
	}
	canon := d.canonical()
	show(0, canon, canon)

	// Reordered, spaced, an unknown member, a repeated key.
	ms := d.members()
	g.r.Shuffle(len(ms), func(i, j int) { ms[i], ms[j] = ms[j], ms[i] })
	at := g.r.IntN(len(ms) + 1)
	ms = append(ms[:at], append([]member{{"zz", `[1, {"q": null}, "s"]`}}, ms[at:]...)...)
	ms = append([]member{{"name", `"old"`}}, ms...)
	show(1, g.spaced(ms), canon)

	// A member dropped: an absent note is Absent, any other is missing.
	ms = d.members()
	drop := g.r.IntN(len(ms))
	key := ms[drop].key
	ms = append(ms[:drop], ms[drop+1:]...)
	if key == "note" {
		e := d
		e.note = nil
		show(2, object(ms), e.canonical())
	} else {
		show(2, object(ms), "missing  "+key)
	}
	v := 3

	// In a list's record: a wrong kind, each end of int8's range, a
	// missing field. A document with no point gets one.
	e := d.clone()
	if len(e.pts) == 0 {
		e.pts = append(e.pts, ptModel{x: "1", label: "q"})
	}
	i := g.r.IntN(len(e.pts))
	p := fmt.Sprintf("pts[%d]", i)
	for _, bad := range []struct{ x, want string }{{`"7"`, "an integer"}, {"128", "int8"}, {"-129", "int8"}, {"200", "int8"}} {
		f := e.clone()
		f.pts[i].x = bad.x
		show(v, f.canonical(), "wrong "+p+".x "+bad.want)
		v++
	}
	f := e.clone()
	f.pts[i].dropLabel = true
	show(v, f.canonical(), "missing "+p+" label")
	v++
	show(v, strings.Replace(canon, fmt.Sprintf(`"doc_id":%d`, d.id), `"doc_id":"7"`, 1), "wrong doc_id an integer")
	v++

	// A Map value out of range; a Nullable of the wrong kind.
	e = d.clone()
	e.tags["c"] = 256 + g.r.IntN(100)
	show(v, e.canonical(), "wrong tags.c uint8")
	v++
	with := func(key, text string) string {
		ms := d.members()
		for i := range ms {
			if ms[i].key == key {
				ms[i].text = text
			}
		}
		return object(ms)
	}
	show(v, with("level", `"x"`), "wrong level an integer")
	v++

	// The union's tag: unknown, missing, or after the variant's member.
	show(v, with("shape", `{"_tag":"Hex","side":1}`), "tag shape Hex")
	v++
	show(v, with("shape", `{"side":1}`), "missing shape _tag")
	v++
	side := g.r.IntN(50)
	sq := d
	sq.circle, sq.side = false, side
	show(v, with("shape", fmt.Sprintf(`{"side":%d,"_tag":"Square"}`, side)), sq.canonical())
	v++

	// The tuple: short, long, empty, or a wrong second element.
	for _, bad := range [][2]string{{"[1]", "wrong pair[0] a list of 2"}, {"[1,true,3]", "wrong pair[1] a list of 2"}, {"[]", "wrong pair a list of 2"}, {"[1,2]", "wrong pair[1] a boolean"}} {
		show(v, with("pair", bad[0]), bad[1])
		v++
	}

	// Broken text.
	show(v, canon+" x", fmt.Sprintf("malformed %d text after the value", len(canon)+1))
	show(v+1, "", "malformed 0 the text ends before a value")
	show(v+2, `{"name":`, "malformed 8 the text ends before a value")

	// json.Value keeps order, repeated keys and spellings.
	in, out := g.anyValue(0)
	g.line("\tshowValue(\"s%d j\", %s)", k, strconv.Quote(in))
	fmt.Fprintf(&g.out, "s%d j %s\n", k, out)
	g.line("\tshowValue(\"s%d jb\", \"[1,]\")", k)
	fmt.Fprintf(&g.out, "s%d jb malformed 3 an unexpected \"]\" where a value belongs\n", k)
	g.line("}")
	g.calls = append(g.calls, fmt.Sprintf("s%d()", k))
}

type member struct{ key, text string }

func object(ms []member) string {
	var parts []string
	for _, m := range ms {
		parts = append(parts, strconv.Quote(m.key)+":"+m.text)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// spaced is object(ms) with white space around its tokens.
func (g *mainGen) spaced(ms []member) string {
	ws := func() string { return []string{"", " ", "\n", "  "}[g.r.IntN(4)] }
	var parts []string
	for _, m := range ms {
		parts = append(parts, ws()+strconv.Quote(m.key)+ws()+":"+ws()+m.text+ws())
	}
	return ws() + "{" + strings.Join(parts, ",") + "}" + ws()
}

type ptModel struct {
	x         string // its JSON spelling
	label     string
	dropLabel bool
}

type docModel struct {
	name     string
	pts      []ptModel
	circle   bool
	r        [2]string // the Ergo literal and the JSON spelling
	side     int
	tags     map[string]int
	pair     int
	pairB    bool
	note     *string
	level    *int
	id, code int
}

func (d docModel) clone() docModel {
	e := d
	e.pts = append([]ptModel(nil), d.pts...)
	e.tags = map[string]int{}
	for k, v := range d.tags {
		e.tags[k] = v
	}
	return e
}

func (g *mainGen) text() string {
	alphabet := []string{"a", "b", "z", "<", "\"", "\\", "é", " "}
	var b strings.Builder
	for range g.r.IntN(5) {
		b.WriteString(alphabet[g.r.IntN(len(alphabet))])
	}
	return b.String()
}

func (g *mainGen) doc() docModel {
	floats := [][2]string{{"1.5", "1.5"}, {"0.25", "0.25"}, {"3.0", "3"}, {"-2.0", "-2"}, {"0.0", "0"}, {"100.125", "100.125"}, {"1e21", "1e+21"}, {"1e-7", "1e-7"}}
	d := docModel{name: g.text(), circle: g.r.IntN(2) == 0, r: floats[g.r.IntN(len(floats))], side: g.r.IntN(100) - 20, tags: map[string]int{},
		pair: g.r.IntN(1000) - 500, pairB: g.r.IntN(2) == 0, id: g.r.IntN(10000), code: 200 + g.r.IntN(300)}
	for range g.r.IntN(3) {
		d.pts = append(d.pts, ptModel{x: strconv.Itoa(g.r.IntN(256) - 128), label: g.text()})
	}
	for range g.r.IntN(3) {
		d.tags[[]string{"a", "b", "c"}[g.r.IntN(3)]] = g.r.IntN(256)
	}
	// A fixed key, and the literal below written in descending order: a
	// walk that does not sort a Map's keys writes them out of order every
	// time a document has three (oracle-o18-codec-keys-unsorted), not by the
	// luck of Go's map iteration.
	d.tags["z"] = 7
	if g.r.IntN(2) == 0 {
		s := g.text()
		d.note = &s
	}
	if g.r.IntN(2) == 0 {
		n := g.r.IntN(100) - 50
		d.level = &n
	}
	return d
}

func (d docModel) tagKeys() []string {
	var keys []string
	for k := range d.tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonText is s as std json writes it: these texts escape only '"' and
// '\'.
func jsonText(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// members are the canonical members, in declaration order.
func (d docModel) members() []member {
	var pts []string
	for _, p := range d.pts {
		if p.dropLabel {
			pts = append(pts, `{"x":`+p.x+`}`)
			continue
		}
		pts = append(pts, `{"x":`+p.x+`,"label":`+jsonText(p.label)+`}`)
	}
	shape := fmt.Sprintf(`{"_tag":"Square","side":%d}`, d.side)
	if d.circle {
		shape = `{"_tag":"Circle","r":` + d.r[1] + `}`
	}
	var tags []string
	for _, k := range d.tagKeys() {
		tags = append(tags, fmt.Sprintf("%q:%d", k, d.tags[k]))
	}
	ms := []member{
		{"name", jsonText(d.name)},
		{"pts", "[" + strings.Join(pts, ",") + "]"},
		{"shape", shape},
		{"tags", "{" + strings.Join(tags, ",") + "}"},
		{"pair", fmt.Sprintf("[%d,%v]", d.pair, d.pairB)},
	}
	if d.note != nil {
		ms = append(ms, member{"note", jsonText(*d.note)})
	}
	level := "null"
	if d.level != nil {
		level = strconv.Itoa(*d.level)
	}
	return append(ms, member{"level", level}, member{"doc_id", strconv.Itoa(d.id)}, member{"httpCode", strconv.Itoa(d.code)})
}

func (d docModel) canonical() string { return object(d.members()) }

// literal is the Ergo value of d.
func (d docModel) literal() string {
	var pts []string
	for _, p := range d.pts {
		pts = append(pts, fmt.Sprintf("Pt{X: %s, Label: %s}", p.x, strconv.Quote(p.label)))
	}
	shape := fmt.Sprintf("Square{Side: %d}", d.side)
	if d.circle {
		shape = "Circle{R: " + d.r[0] + "}"
	}
	var tags []string
	keys := d.tagKeys()
	for i := len(keys) - 1; i >= 0; i-- {
		tags = append(tags, fmt.Sprintf("%q: %d", keys[i], d.tags[keys[i]]))
	}
	note := "json.Absent"
	if d.note != nil {
		note = "json.Present[string]{Value: " + strconv.Quote(*d.note) + "}"
	}
	level := "json.Null"
	if d.level != nil {
		level = fmt.Sprintf("json.NonNull[int]{Value: %d}", *d.level)
	}
	return fmt.Sprintf("Doc{Name: %s, Pts: []Pt{%s}, Shape: %s, Tags: Map[string, uint8]{%s}, Pair: (%d, %v), Note: %s, Level: %s, ID: %d, HTTPCode: %d}",
		strconv.Quote(d.name), strings.Join(pts, ", "), shape, strings.Join(tags, ", "), d.pair, d.pairB, note, level, d.id, d.code)
}

// anyValue is a JSON text with white space and the compact text
// json.Value writes back: numbers as spelled, members in order, a
// repeated key kept.
func (g *mainGen) anyValue(depth int) (in, out string) {
	ws := func() string { return []string{"", " ", "\n"}[g.r.IntN(3)] }
	k := g.r.IntN(7)
	if depth > 2 {
		k %= 4
	}
	switch k {
	case 0:
		w := []string{"null", "true", "false"}[g.r.IntN(3)]
		return w, w
	case 1:
		n := []string{"0", "-0", "1.50", "2e3", "-7", "1E-2", "123456789012345678901234567890"}[g.r.IntN(7)]
		return n, n
	case 2, 3:
		s := jsonText(strings.NewReplacer(`"`, "", `\`, "", "é", "e").Replace(g.text()))
		return s, s
	case 4, 5:
		var ins, outs []string
		for range g.r.IntN(4) {
			key := []string{"a", "a", "b", "_tag"}[g.r.IntN(4)]
			i, o := g.anyValue(depth + 1)
			ins = append(ins, ws()+strconv.Quote(key)+ws()+":"+ws()+i+ws())
			outs = append(outs, strconv.Quote(key)+":"+o)
		}
		return "{" + strings.Join(ins, ",") + "}", "{" + strings.Join(outs, ",") + "}"
	}
	var ins, outs []string
	for range g.r.IntN(4) {
		i, o := g.anyValue(depth + 1)
		ins = append(ins, ws()+i+ws())
		outs = append(outs, o)
	}
	return "[" + strings.Join(ins, ",") + "]", "[" + strings.Join(outs, ",") + "]"
}

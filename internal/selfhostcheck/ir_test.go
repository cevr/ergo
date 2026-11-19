package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWriteAgreesWithGo holds the IR of the self-hosted compiler
// (compiler/ir, capstone S4: rows of one-byte kinds with their slots, and
// Write; linked as internal/linked/compiler) to the Go IR builder's file:
// encoding/json's Marshal of the builder's irFile. It packs an IR file
// into compiler/ir's rows (irPack, by the kind table compiler/ir exports)
// and compares compiler/ir's text with Go's, byte for byte, on two
// domains:
//
//   - every IR file ergo build writes (lower.LowerIRSplit) for the cmd
//     package of each examples/ module. The file is read into the mirror
//     (irFile, a copy of the builder's types with their tags), which must
//     first write the same bytes again; so a field the builder adds turns
//     this test red until compiler/ir and the mirror have it. The packer
//     refuses a node that sets a field outside its kind's slots and a kind
//     the table lacks: a kind or field the builder starts to write turns
//     it red too (capstone §2's freeze);
//   - seeded generated files (irGen) built from the kind table: every
//     kind, each slot set or not, nil and empty lists, nil elements,
//     negative ints, and texts with the bytes encoding/json escapes
//     (controls, " \ < > &, invalid UTF-8, U+2028, U+2029).
//
// IR the writer cannot read must fail; the facade never repairs. A failing
// case prints the seed, both texts from their first difference and the
// replay command.
//
// Mutants it alone catches: testdata/mutants.json, rows ir-*.
func TestIRWriteAgreesWithGo(t *testing.T) {
	seed := selfhostSeed(t)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestIRWriteAgreesWithGo$' ./internal/selfhostcheck"
	kinds := irKindTable(t)
	failed := 0
	check := func(name string, want []byte, f *irFile) {
		t.Helper()
		p, err := irPack(kinds, f)
		if err != nil {
			t.Fatalf("%s: pack: %v", name, err)
		}
		got, err := linked.IRWrite(context.Background(), p.words, p.strs)
		if err != nil {
			t.Fatalf("%s: IRWrite: %v", name, err)
		}
		text, ok := strings.CutPrefix(got, "ok\n")
		if ok && text == string(want) {
			return
		}
		failed++
		if failed > 3 {
			return
		}
		at := 0
		for at < len(text) && at < len(want) && text[at] == want[at] {
			at++
		}
		from := max(0, at-80)
		t.Errorf("%s: compiler/ir writes another IR file at byte %d\n ergo: %.240q\n   go: %.240q\n%s", name, at, text[min(len(text), from):], want[from:], replay)
	}

	r := rand.New(rand.NewPCG(seed, 0x5a4a))
	reached := map[string]bool{}
	for i := range 300 {
		f := irGen(r, kinds, reached)
		want, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		check(fmt.Sprintf("generated %d", i), want, f)
	}
	for _, k := range kinds.list {
		if !reached[k.key()] {
			t.Errorf("no generated file reaches kind %q", k.key())
		}
	}

	files := 0
	for _, dir := range irPrograms(t) {
		u, err := irspike.Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		out, _, _, err := lower.LowerIRSplit(u, false)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, file := range out {
			if filepath.Base(file.Path) != lower.IRFileName {
				continue
			}
			name := dir + ": " + file.Path
			var f irFile
			dec := json.NewDecoder(bytes.NewReader(file.Data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&f); err != nil {
				t.Fatalf("%s: the mirror cannot read the builder's IR file (a field the mirror lacks?): %v", name, err)
			}
			again, err := json.Marshal(&f)
			if err != nil || !bytes.Equal(again, file.Data) {
				t.Fatalf("%s: the mirror does not write the builder's IR file again: the mirror lags lower's irFile", name)
			}
			check(name, file.Data, &f)
			files++
		}
	}
	if files == 0 {
		t.Fatal("no IR file was compared")
	}
	t.Logf("compared 300 generated IR files and %d IR files of the examples", files)
	if failed > 0 {
		t.Fatalf("%d IR files differ", failed)
	}

	// IR is never repaired: a statement kind where an expression is
	// wanted, a kind past the table, and cut words fail.
	var p irPacked
	for p.firstKind == 0 {
		var err error
		if p, err = irPack(kinds, irGen(r, kinds, reached)); err != nil {
			t.Fatal(err)
		}
	}
	var stmtKind, exprAt uint32
	for _, k := range kinds.list {
		if k.class == irTStmt {
			stmtKind = k.code
		}
	}
	for i, c := range p.firstKinds {
		if kinds.byCode[c].class == irTExpr {
			exprAt = uint32(p.firstKind + i)
		}
	}
	var bad [][]uint32
	if exprAt != 0 {
		w := append([]uint32(nil), p.words...)
		w[exprAt] = stmtKind
		bad = append(bad, w)
	}
	w := append([]uint32(nil), p.words...)
	w[p.firstKind] = uint32(len(kinds.list) + 1)
	bad = append(bad, w, p.words[:len(p.words)-1], p.words[:1])
	for _, w := range bad {
		got, err := linked.IRWrite(context.Background(), w, p.strs)
		if err != nil || !strings.HasPrefix(got, "malformed ") {
			t.Errorf("malformed IR of %d words is written: %.80q", len(w), got)
		}
	}
}

// irPrograms is the cmd package of every examples/ module, as ergo build
// lowers them.
func irPrograms(t *testing.T) []string {
	t.Helper()
	root := repoRoot()
	mods, err := filepath.Glob(filepath.Join(root, "examples", "*", "ergo.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, m := range mods {
		cmds, _ := filepath.Glob(filepath.Join(filepath.Dir(m), "cmd", "*"))
		for _, d := range cmds {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				dirs = append(dirs, d)
			}
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no example program")
	}
	return dirs
}

// The mirror of internal/sketch/lower's IR types (ir.go, ir_fail.go): the
// same fields, in the same order, with the same tags. The corpus leg
// checks that it writes the builder's files again. The field order is
// also compiler/ir's field index (compiler/ir/ir.ergo, stmtFields...).
type irFile struct {
	V      int      `json:"v"`
	Files  []string `json:"files"`
	Funcs  []irFunc `json:"funcs"`
	Shared []string `json:"shared,omitempty"`
}

type irFunc struct {
	Name   string    `json:"name"`
	Pos    irPos     `json:"pos"`
	End    irPos     `json:"end"`
	Locals []irLocal `json:"locals,omitempty"`
	Body   []*irStmt `json:"body"`
}

type irLocal struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Pos  irPos  `json:"pos"`
}

type irPos [3]int

type irStmt struct {
	K       string    `json:"k"`
	Pos     irPos     `json:"pos"`
	Local   int       `json:"local,omitempty"`
	Key     *int      `json:"key,omitempty"`
	Val     *int      `json:"val,omitempty"`
	X       *irExpr   `json:"x,omitempty"`
	Then    []*irStmt `json:"then,omitempty"`
	Else    []*irStmt `json:"else,omitempty"`
	Body    []*irStmt `json:"body,omitempty"`
	End     irPos     `json:"end"`
	ElsePos irPos     `json:"epos"`
	ElseEnd irPos     `json:"eend"`
	Op      string    `json:"op,omitempty"`
	Cases   []*irCase `json:"cases,omitempty"`
	Label   string    `json:"label,omitempty"`
	Y       *irExpr   `json:"y,omitempty"`
	Ls      []int     `json:"ls,omitempty"`
}

type irExpr struct {
	K      string    `json:"k"`
	Pos    irPos     `json:"pos"`
	I      int       `json:"i,omitempty"`
	T      string    `json:"t,omitempty"`
	V      string    `json:"v,omitempty"`
	Op     string    `json:"op,omitempty"`
	X      *irExpr   `json:"x,omitempty"`
	Y      *irExpr   `json:"y,omitempty"`
	Name   string    `json:"name,omitempty"`
	Args   []*irExpr `json:"args,omitempty"`
	Elems  []*irExpr `json:"elems,omitempty"`
	Keys   []string  `json:"keys,omitempty"`
	Params []int     `json:"params,omitempty"`
	Body   []*irStmt `json:"body,omitempty"`
	End    *irPos    `json:"end,omitempty"`
}

type irCase struct {
	Pos   irPos     `json:"pos"`
	Types []string  `json:"types"`
	Body  []*irStmt `json:"body,omitempty"`
	Vals  []*irExpr `json:"vals,omitempty"`
	Def   bool      `json:"def,omitempty"`
}

// The struct types of compiler/ir's rows (ir.ergo TStmt, TExpr, TCase).
const (
	irTStmt = 4
	irTExpr = 5
	irTCase = 6
)

// irKind is one row of compiler/ir's kind table (ir.Table).
type irKind struct {
	code, class uint32
	name        string
	slots       []int
}

func (k irKind) key() string { return strconv.Itoa(int(k.class)) + " " + k.name }

type irKinds struct {
	list   []irKind
	byKey  map[string]irKind
	byCode map[uint32]irKind
}

// irKindTable reads compiler/ir's kinds through the linked export, so the
// packer and the writer share one table.
func irKindTable(t *testing.T) irKinds {
	t.Helper()
	text, err := linked.IRKinds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ks := irKinds{byKey: map[string]irKind{}, byCode: map[uint32]irKind{}}
	for line := range strings.Lines(text) {
		head, slots, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "|")
		f := strings.SplitN(head, " ", 3)
		if !ok || len(f) != 3 {
			t.Fatalf("kind table line %q", line)
		}
		code, _ := strconv.Atoi(f[0])
		class, _ := strconv.Atoi(f[1])
		k := irKind{code: uint32(code), class: uint32(class), name: f[2]}
		if slots != "" {
			for s := range strings.SplitSeq(slots, ",") {
				n, _ := strconv.Atoi(s)
				k.slots = append(k.slots, n)
			}
		}
		ks.list = append(ks.list, k)
		ks.byKey[k.key()] = k
		ks.byCode[k.code] = k
	}
	if len(ks.list) == 0 {
		t.Fatal("empty kind table")
	}
	return ks
}

// irPacked is a file packed for compiler/api.IRWrite: its words, its
// texts, and where the first function with rows has its kinds column.
type irPacked struct {
	words      []uint32
	strs       []string
	firstKind  int
	firstKinds []uint32
}

// irBody is one function's rows under construction.
type irBody struct {
	kind, a, b, file, line, col []uint32
	extra                       []uint32
}

type irPacker struct {
	kinds irKinds
	strs  []string
	ids   map[string]uint32
}

func (p *irPacker) str(s string) uint32 {
	if id, ok := p.ids[s]; ok {
		return id
	}
	id := uint32(len(p.strs))
	p.strs = append(p.strs, s)
	p.ids[s] = id
	return id
}

func irPack(kinds irKinds, f *irFile) (irPacked, error) {
	p := &irPacker{kinds: kinds, strs: []string{""}, ids: map[string]uint32{"": 0}}
	w := []uint32{2, uint32(uint64(f.V)), uint32(uint64(f.V) >> 32)}
	texts := func(s []string) {
		w = append(w, uint32(len(s)))
		for _, x := range s {
			w = append(w, p.str(x))
		}
	}
	w = append(w, flag(f.Files != nil))
	texts(f.Files)
	texts(f.Shared)
	w = append(w, flag(f.Funcs != nil), uint32(len(f.Funcs)))
	out := irPacked{}
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		w = append(w, p.str(fn.Name))
		for _, pos := range []irPos{fn.Pos, fn.End} {
			ws, err := posWords(pos)
			if err != nil {
				return out, err
			}
			w = append(w, ws...)
		}
		w = append(w, uint32(len(fn.Locals)))
		for _, l := range fn.Locals {
			ws, err := posWords(l.Pos)
			if err != nil {
				return out, err
			}
			w = append(w, p.str(l.Name), p.str(l.Type))
			w = append(w, ws...)
		}
		b := &irBody{extra: []uint32{0}}
		roots, err := p.list(b, reflect.ValueOf(fn.Body))
		if err != nil {
			return out, err
		}
		n := len(b.kind)
		w = append(w, uint32(n))
		if n > 0 && out.firstKind == 0 {
			out.firstKind, out.firstKinds = len(w), b.kind
		}
		for _, col := range [][]uint32{b.kind, b.a, b.b, b.file, b.line, b.col} {
			w = append(w, col...)
		}
		w = append(w, uint32(len(b.extra)))
		w = append(w, b.extra...)
		w = append(w, roots)
	}
	out.words, out.strs = w, p.strs
	return out, nil
}

func flag(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func posWords(p irPos) ([]uint32, error) {
	for _, v := range p {
		if v < 0 || v > math.MaxUint32 {
			return nil, fmt.Errorf("position %v outside a word", p)
		}
	}
	return []uint32{uint32(p[0]), uint32(p[1]), uint32(p[2])}, nil
}

// list packs a slice of rows (a nil slice is 0, an empty one the offset
// of a 0 length; a nil element is id 0).
func (p *irPacker) list(b *irBody, v reflect.Value) (uint32, error) {
	if v.IsNil() {
		return 0, nil
	}
	ids := make([]uint32, v.Len())
	for i := range ids {
		if v.Index(i).IsNil() {
			continue
		}
		id, err := p.row(b, v.Index(i))
		if err != nil {
			return 0, err
		}
		ids[i] = id
	}
	q := uint32(len(b.extra))
	b.extra = append(b.extra, uint32(len(ids)))
	b.extra = append(b.extra, ids...)
	return q, nil
}

// row packs the node *v (an *irStmt, *irExpr or *irCase), its children
// first, and gives its id. A field Go set outside the kind's slots is an
// error: the kind table lags the builder.
func (p *irPacker) row(b *irBody, v reflect.Value) (uint32, error) {
	e := v.Elem()
	class, first, name := uint32(irTCase), 1, ""
	switch e.Interface().(type) {
	case irStmt:
		class, first, name = irTStmt, 2, e.Field(0).String()
	case irExpr:
		class, first, name = irTExpr, 2, e.Field(0).String()
	}
	k, ok := p.kinds.byKey[strconv.Itoa(int(class))+" "+name]
	if !ok {
		return 0, fmt.Errorf("kind %q of struct type %d is not in compiler/ir's table", name, class)
	}
	pos := e.Field(first - 1).Interface().(irPos)
	posw, err := posWords(pos)
	if err != nil {
		return 0, err
	}
	words := map[int]uint32{}
	for j := first; j < e.NumField(); j++ {
		word, err := p.field(b, e.Field(j))
		if err != nil {
			return 0, err
		}
		if word != 0 {
			words[j] = word
		}
	}
	slotted := map[int]bool{}
	for _, s := range k.slots {
		slotted[s] = true
	}
	for j := range words {
		if !slotted[j] {
			return 0, fmt.Errorf("kind %q sets field %s outside its slots", name, e.Type().Field(j).Name)
		}
	}
	var a, bw uint32
	switch n := len(k.slots); {
	case n == 0:
	case n <= 2:
		a = words[k.slots[0]]
		if n == 2 {
			bw = words[k.slots[1]]
		}
	default:
		a = words[k.slots[0]]
		bw = uint32(len(b.extra))
		for _, s := range k.slots[1:] {
			b.extra = append(b.extra, words[s])
		}
	}
	b.kind = append(b.kind, k.code)
	b.a = append(b.a, a)
	b.b = append(b.b, bw)
	b.file = append(b.file, posw[0])
	b.line = append(b.line, posw[1])
	b.col = append(b.col, posw[2])
	return uint32(len(b.kind)), nil
}

// field is the slot word of one field (compiler/ir/ir.ergo's encodings);
// 0 is Go's zero value.
func (p *irPacker) field(b *irBody, v reflect.Value) (uint32, error) {
	switch x := v.Interface().(type) {
	case string:
		return p.str(x), nil
	case int:
		if x < math.MinInt32 || x > math.MaxInt32 {
			return 0, fmt.Errorf("int %d outside int32", x)
		}
		return uint32(int32(x)), nil
	case *int:
		if x == nil {
			return 0, nil
		}
		if *x < 0 || *x >= math.MaxUint32 {
			return 0, fmt.Errorf("pointed int %d outside a word", *x)
		}
		return uint32(*x) + 1, nil
	case bool:
		return flag(x), nil
	case irPos:
		if x == (irPos{}) {
			return 0, nil
		}
		return p.pos(b, x)
	case *irPos:
		if x == nil {
			return 0, nil
		}
		return p.pos(b, *x)
	case *irExpr:
		if x == nil {
			return 0, nil
		}
		return p.row(b, v)
	case []string:
		if x == nil {
			return 0, nil
		}
		q := uint32(len(b.extra))
		b.extra = append(b.extra, uint32(len(x)))
		for _, s := range x {
			b.extra = append(b.extra, p.str(s))
		}
		return q, nil
	case []int:
		if x == nil {
			return 0, nil
		}
		q := uint32(len(b.extra))
		b.extra = append(b.extra, uint32(len(x)))
		for _, n := range x {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return 0, fmt.Errorf("int %d outside int32", n)
			}
			b.extra = append(b.extra, uint32(int32(n)))
		}
		return q, nil
	case []*irStmt, []*irExpr, []*irCase:
		return p.list(b, v)
	}
	return 0, fmt.Errorf("no slot encoding for %s", v.Type())
}

func (p *irPacker) pos(b *irBody, x irPos) (uint32, error) {
	ws, err := posWords(x)
	if err != nil {
		return 0, err
	}
	q := uint32(len(b.extra))
	b.extra = append(b.extra, ws...)
	return q, nil
}

// irGen is a seeded IR file built from the kind table: each node is of a
// kind of the table, each of the kind's slots set or not, every field
// outside them Go's zero, so each branch of the writer is reached. reached
// records the kinds it built.
func irGen(r *rand.Rand, kinds irKinds, reached map[string]bool) *irFile {
	g := &irGenerator{r: r, kinds: kinds, reached: reached}
	f := &irFile{V: g.int(), Files: g.strList(), Shared: g.strList()}
	if r.IntN(6) > 0 {
		f.Funcs = []irFunc{}
		for range r.IntN(4) {
			fn := irFunc{Name: g.text(), Pos: g.pos(), End: g.pos(), Body: g.stmts(3)}
			if r.IntN(2) == 0 {
				fn.Locals = []irLocal{}
				for range r.IntN(3) {
					fn.Locals = append(fn.Locals, irLocal{Name: g.text(), Type: g.text(), Pos: g.pos()})
				}
			}
			f.Funcs = append(f.Funcs, fn)
		}
	}
	return f
}

type irGenerator struct {
	r       *rand.Rand
	kinds   irKinds
	reached map[string]bool
}

func (g *irGenerator) int() int {
	switch g.r.IntN(4) {
	case 0:
		return 0
	case 1:
		return -g.r.IntN(1 << 20)
	case 2:
		return math.MaxInt32 - g.r.IntN(3)
	}
	return g.r.IntN(300)
}

func (g *irGenerator) pos() irPos {
	if g.r.IntN(4) == 0 {
		return irPos{}
	}
	return irPos{g.r.IntN(8), g.r.IntN(5000), g.r.IntN(200)}
}

// irTextParts are the pieces of a generated text: plain text and every
// byte or sequence encoding/json treats on its own.
var irTextParts = []string{"", "a", "call", "x1", " ", "\"", "\\", "<", ">", "&", "\b", "\f", "\n", "\r", "\t", "\x00", "\x01", "\x1f", "\x7f", "é", "日本", "\u2028", "\u2029", "\U0001F600", "\xff", "\xc3", "\xe2\x80", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\xaf", "\ufffd"}

func (g *irGenerator) text() string {
	var b strings.Builder
	for range g.r.IntN(5) {
		b.WriteString(irTextParts[g.r.IntN(len(irTextParts))])
	}
	return b.String()
}

func (g *irGenerator) strList() []string {
	switch g.r.IntN(3) {
	case 0:
		return nil
	case 1:
		return []string{}
	}
	var s []string
	for range 1 + g.r.IntN(3) {
		s = append(s, g.text())
	}
	return s
}

func (g *irGenerator) stmts(depth int) []*irStmt {
	var s []*irStmt
	g.nodes(reflect.ValueOf(&s).Elem(), depth)
	return s
}

// nodes fills the slice *v (of *irStmt, *irExpr or *irCase): nil, empty,
// or a few nodes, now and then a nil element.
func (g *irGenerator) nodes(v reflect.Value, depth int) bool {
	switch g.r.IntN(4) {
	case 0:
		return false
	case 1:
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
		return true
	}
	n := 1 + g.r.IntN(3)
	s := reflect.MakeSlice(v.Type(), n, n)
	for i := range n {
		if g.r.IntN(7) == 0 {
			continue
		}
		s.Index(i).Set(g.node(v.Type().Elem(), depth))
	}
	v.Set(s)
	return true
}

// node is a node of pointer type pt of a kind of the table for its
// struct type, its slots filled at random.
func (g *irGenerator) node(pt reflect.Type, depth int) reflect.Value {
	class, first := uint32(irTCase), 1
	switch pt.Elem() {
	case reflect.TypeFor[irStmt]():
		class, first = irTStmt, 2
	case reflect.TypeFor[irExpr]():
		class, first = irTExpr, 2
	}
	var of []irKind
	for _, k := range g.kinds.list {
		if k.class == class {
			of = append(of, k)
		}
	}
	k := of[g.r.IntN(len(of))]
	g.reached[k.key()] = true
	v := reflect.New(pt.Elem())
	e := v.Elem()
	if first == 2 {
		e.Field(0).SetString(k.name)
	}
	e.Field(first - 1).Set(reflect.ValueOf(g.pos()))
	for _, s := range k.slots {
		if g.r.IntN(5) == 0 {
			continue // the slot left unset
		}
		g.fill(e.Field(s), depth-1)
	}
	return v
}

// fill sets the slot field f at random; a slot that holds rows is filled
// only above depth 0, which bounds the tree.
func (g *irGenerator) fill(f reflect.Value, depth int) {
	switch f.Interface().(type) {
	case string:
		f.SetString(g.text())
	case int:
		f.SetInt(int64(g.int()))
	case *int:
		n := g.r.IntN(400)
		f.Set(reflect.ValueOf(&n))
	case bool:
		f.SetBool(true)
	case irPos:
		f.Set(reflect.ValueOf(g.pos()))
	case *irPos:
		p := g.pos()
		f.Set(reflect.ValueOf(&p))
	case *irExpr:
		if depth > 0 {
			f.Set(g.node(f.Type(), depth))
		}
	case []string:
		f.Set(reflect.ValueOf(g.strList()))
	case []int:
		switch g.r.IntN(3) {
		case 0:
		case 1:
			f.Set(reflect.ValueOf([]int{}))
		default:
			f.Set(reflect.ValueOf([]int{g.int(), g.int()}))
		}
	default:
		if depth > 0 {
			g.nodes(f, depth)
		}
	}
}

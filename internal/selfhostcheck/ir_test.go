package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWriteAgreesWithGo holds the IR file writer written in Ergo
// (compiler/ir, capstone S4a; linked as internal/linked/compiler) to the
// Go IR builder's: encoding/json's Marshal of the builder's irFile. It
// packs an IR file into compiler/ir's pool (irPack) and compares
// compiler/ir's text with Go's, byte for byte, on two domains:
//
//   - every IR file ergo build writes (lower.LowerIRSplit) for the cmd
//     package of each examples/ module: the file is read into irMirror, a
//     copy of the builder's types with their tags, which must first write
//     the same bytes again (so a field the builder adds turns this test red
//     until compiler/ir and the mirror have it), then packed;
//   - seeded generated files (irGen): every field set or not, nil and empty
//     lists, negative and 64-bit ints, and texts with the bytes encoding/json
//     escapes (controls, " \ < > &, invalid UTF-8, U+2028, U+2029).
//
// A packed pool the writer cannot read must fail; the facade never repairs.
// A failing case prints the seed, both texts from their first difference
// and the replay command.
//
// Mutants it alone catches: testdata/mutants.json, rows ir-*.
func TestIRWriteAgreesWithGo(t *testing.T) {
	seed := selfhostSeed(t)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestIRWriteAgreesWithGo$' ./internal/selfhostcheck"
	failed := 0
	check := func(name string, want []byte, f *irFile) {
		t.Helper()
		words, strs, err := irPack(f)
		if err != nil {
			t.Fatalf("%s: pack: %v", name, err)
		}
		got, err := linked.IRWrite(context.Background(), words, strs)
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
		t.Errorf("%s: compiler/ir writes another IR file at byte %d\n ergo: %.200q\n   go: %.200q\n%s", name, at, got[min(len(got), from):], want[from:], replay)
	}

	r := rand.New(rand.NewPCG(seed, 0x5a4a))
	for i := range 300 {
		f := irGen(r)
		want, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		check(fmt.Sprintf("generated %d", i), want, f)
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
				t.Fatalf("%s: the mirror does not write the builder's IR file again: irMirror lags lower's irFile", name)
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

	// A pool is never repaired: a wrong type byte and a cut pool fail.
	words, strs, err := irPack(irGen(r))
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]uint32(nil), words...)
	bad[words[1]] = 99
	for _, w := range [][]uint32{bad, words[:len(words)-1], words[:1]} {
		got, err := linked.IRWrite(context.Background(), w, strs)
		if err != nil || !strings.HasPrefix(got, "malformed ") {
			t.Errorf("a malformed pool of %d words is written: %.80q", len(w), got)
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
// checks that it writes the builder's files again.
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

// irPacker writes compiler/ir's pool (compiler/ir/ir.ergo): a record is
// its struct type, its mask, then its set fields' words. A field's bit is
// set when Go's value is not the zero value (a nil pointer or list, "", 0,
// false), so the writer, not the packer, applies omitempty.
type irPacker struct {
	words []uint32
	strs  []string
	ids   map[string]uint32
}

const (
	irTFile = 1 + iota
	irTFunc
	irTLocal
	irTStmt
	irTExpr
	irTCase
)

func irPack(f *irFile) ([]uint32, []string, error) {
	p := &irPacker{words: []uint32{1, 0}, ids: map[string]uint32{}}
	root, err := p.file(f)
	if err != nil {
		return nil, nil, err
	}
	p.words[1] = root
	return p.words, p.strs, nil
}

// rec is one record under construction: the fields' words are appended
// to body and the bits to mask, field by field in the type's order.
type rec struct {
	p    *irPacker
	mask uint32
	bit  uint
	body []uint32
	err  error
}

func (p *irPacker) rec() *rec { return &rec{p: p} }

func (r *rec) field(set bool, words ...uint32) {
	if set {
		r.mask |= 1 << r.bit
		r.body = append(r.body, words...)
	}
	r.bit++
}

func (r *rec) str(s string) { r.field(s != "", r.p.str(s)) }

func (r *rec) int(n int) { r.field(n != 0, uint32(uint64(n)), uint32(uint64(n)>>32)) }

func (r *rec) intPtr(n *int) {
	if n == nil {
		r.field(false)
		return
	}
	r.field(true, uint32(uint64(*n)), uint32(uint64(*n)>>32))
}

func (r *rec) pos(p irPos) {
	if r.err == nil {
		for _, v := range p {
			if v < 0 || v > 1<<32-1 {
				r.err = fmt.Errorf("position %v outside a word", p)
			}
		}
	}
	r.field(p != irPos{}, uint32(p[0]), uint32(p[1]), uint32(p[2]))
}

func (r *rec) posPtr(p *irPos) {
	if p == nil {
		r.field(false)
		return
	}
	r.pos(*p)
	r.mask |= 1 << (r.bit - 1)
	if *p == (irPos{}) {
		r.body = append(r.body, 0, 0, 0)
	}
}

func (r *rec) node(o uint32, set bool, err error) {
	if err != nil && r.err == nil {
		r.err = err
	}
	r.field(set, o)
}

func (r *rec) list(nonNil bool, elems []uint32) {
	if !nonNil {
		r.field(false)
		return
	}
	q := uint32(len(r.p.words))
	r.p.words = append(r.p.words, uint32(len(elems)))
	r.p.words = append(r.p.words, elems...)
	r.field(true, q)
}

func (r *rec) strs(s []string) {
	ids := make([]uint32, len(s))
	for i, x := range s {
		ids[i] = r.p.str(x)
	}
	r.list(s != nil, ids)
}

func (r *rec) ints(s []int) {
	var w []uint32
	for _, n := range s {
		w = append(w, uint32(uint64(n)), uint32(uint64(n)>>32))
	}
	if s != nil {
		q := uint32(len(r.p.words))
		r.p.words = append(r.p.words, uint32(len(s)))
		r.p.words = append(r.p.words, w...)
		r.field(true, q)
		return
	}
	r.field(false)
}

func (r *rec) end(t uint32) (uint32, error) {
	if r.err != nil {
		return 0, r.err
	}
	o := uint32(len(r.p.words))
	r.p.words = append(r.p.words, t, r.mask)
	r.p.words = append(r.p.words, r.body...)
	return o, nil
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

func (p *irPacker) file(f *irFile) (uint32, error) {
	r := p.rec()
	r.int(f.V)
	r.strs(f.Files)
	funcs := make([]uint32, len(f.Funcs))
	for i := range f.Funcs {
		o, err := p.fn(&f.Funcs[i])
		if err != nil {
			return 0, err
		}
		funcs[i] = o
	}
	r.list(f.Funcs != nil, funcs)
	r.strs(f.Shared)
	return r.end(irTFile)
}

func (p *irPacker) fn(f *irFunc) (uint32, error) {
	r := p.rec()
	r.str(f.Name)
	r.pos(f.Pos)
	r.pos(f.End)
	locals := make([]uint32, len(f.Locals))
	for i := range f.Locals {
		l := p.rec()
		l.str(f.Locals[i].Name)
		l.str(f.Locals[i].Type)
		l.pos(f.Locals[i].Pos)
		o, err := l.end(irTLocal)
		if err != nil {
			return 0, err
		}
		locals[i] = o
	}
	r.list(f.Locals != nil, locals)
	body, err := p.stmts(f.Body)
	if err != nil {
		return 0, err
	}
	r.list(f.Body != nil, body)
	return r.end(irTFunc)
}

func (p *irPacker) stmts(s []*irStmt) ([]uint32, error) {
	out := make([]uint32, len(s))
	for i, x := range s {
		if x == nil {
			continue // 0: a null element
		}
		o, err := p.stmt(x)
		if err != nil {
			return nil, err
		}
		out[i] = o
	}
	return out, nil
}

func (p *irPacker) exprs(s []*irExpr) ([]uint32, error) {
	out := make([]uint32, len(s))
	for i, x := range s {
		if x == nil {
			continue // 0: a null element
		}
		o, err := p.expr(x)
		if err != nil {
			return nil, err
		}
		out[i] = o
	}
	return out, nil
}

func (p *irPacker) stmtList(r *rec, s []*irStmt) {
	o, err := p.stmts(s)
	if err != nil && r.err == nil {
		r.err = err
	}
	r.list(s != nil, o)
}

func (p *irPacker) exprList(r *rec, s []*irExpr) {
	o, err := p.exprs(s)
	if err != nil && r.err == nil {
		r.err = err
	}
	r.list(s != nil, o)
}

func (p *irPacker) optExpr(r *rec, x *irExpr) {
	if x == nil {
		r.field(false)
		return
	}
	o, err := p.expr(x)
	r.node(o, true, err)
}

func (p *irPacker) stmt(s *irStmt) (uint32, error) {
	if s == nil {
		return 0, fmt.Errorf("a nil statement in a list")
	}
	r := p.rec()
	r.str(s.K)
	r.pos(s.Pos)
	r.int(s.Local)
	r.intPtr(s.Key)
	r.intPtr(s.Val)
	p.optExpr(r, s.X)
	p.stmtList(r, s.Then)
	p.stmtList(r, s.Else)
	p.stmtList(r, s.Body)
	r.pos(s.End)
	r.pos(s.ElsePos)
	r.pos(s.ElseEnd)
	r.str(s.Op)
	cases := make([]uint32, len(s.Cases))
	for i, c := range s.Cases {
		if c == nil {
			continue // 0: a null element
		}
		o, err := p.irCase(c)
		if err != nil {
			return 0, err
		}
		cases[i] = o
	}
	r.list(s.Cases != nil, cases)
	r.str(s.Label)
	p.optExpr(r, s.Y)
	r.ints(s.Ls)
	return r.end(irTStmt)
}

func (p *irPacker) expr(x *irExpr) (uint32, error) {
	if x == nil {
		return 0, fmt.Errorf("a nil expression in a list")
	}
	r := p.rec()
	r.str(x.K)
	r.pos(x.Pos)
	r.int(x.I)
	r.str(x.T)
	r.str(x.V)
	r.str(x.Op)
	p.optExpr(r, x.X)
	p.optExpr(r, x.Y)
	r.str(x.Name)
	p.exprList(r, x.Args)
	p.exprList(r, x.Elems)
	r.strs(x.Keys)
	r.ints(x.Params)
	p.stmtList(r, x.Body)
	r.posPtr(x.End)
	return r.end(irTExpr)
}

func (p *irPacker) irCase(c *irCase) (uint32, error) {
	if c == nil {
		return 0, fmt.Errorf("a nil case")
	}
	r := p.rec()
	r.pos(c.Pos)
	r.strs(c.Types)
	p.stmtList(r, c.Body)
	p.exprList(r, c.Vals)
	r.field(c.Def)
	return r.end(irTCase)
}

// irGen is a seeded IR file: shapes the builder writes and the values it
// never does, so each branch of the writer is reached.
func irGen(r *rand.Rand) *irFile {
	g := &irGenerator{r: r}
	f := &irFile{V: g.int(), Files: g.strList(), Shared: g.strList()}
	if r.IntN(5) > 0 {
		f.Funcs = []irFunc{}
		for range r.IntN(4) {
			fn := irFunc{Name: g.text(), Pos: g.pos(), End: g.pos(), Body: g.stmts(2)}
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

type irGenerator struct{ r *rand.Rand }

func (g *irGenerator) int() int {
	switch g.r.IntN(5) {
	case 0:
		return 0
	case 1:
		return -g.r.IntN(1000)
	case 2:
		return int(g.r.Uint64())
	}
	return g.r.IntN(100)
}

func (g *irGenerator) intPtr() *int {
	if g.r.IntN(2) == 0 {
		return nil
	}
	n := g.int()
	return &n
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

func (g *irGenerator) intList() []int {
	switch g.r.IntN(3) {
	case 0:
		return nil
	case 1:
		return []int{}
	}
	var s []int
	for range 1 + g.r.IntN(3) {
		s = append(s, g.int())
	}
	return s
}

func (g *irGenerator) stmts(depth int) []*irStmt {
	switch g.r.IntN(4) {
	case 0:
		return nil
	case 1:
		return []*irStmt{}
	}
	var s []*irStmt
	for range 1 + g.r.IntN(3) {
		if g.r.IntN(8) == 0 {
			s = append(s, nil)
			continue
		}
		s = append(s, g.stmt(depth))
	}
	return s
}

func (g *irGenerator) exprs(depth int) []*irExpr {
	switch g.r.IntN(4) {
	case 0:
		return nil
	case 1:
		return []*irExpr{}
	}
	var s []*irExpr
	for range 1 + g.r.IntN(3) {
		if g.r.IntN(6) == 0 {
			s = append(s, nil) // the builder writes null elements
			continue
		}
		s = append(s, g.expr(depth))
	}
	return s
}

func (g *irGenerator) optExpr(depth int) *irExpr {
	if depth <= 0 || g.r.IntN(2) == 0 {
		return nil
	}
	return g.expr(depth - 1)
}

func (g *irGenerator) stmt(depth int) *irStmt {
	s := &irStmt{K: g.text(), Pos: g.pos(), Local: g.int(), Key: g.intPtr(), Val: g.intPtr(), X: g.optExpr(depth), End: g.pos(), ElsePos: g.pos(), ElseEnd: g.pos(), Op: g.text(), Label: g.text(), Y: g.optExpr(depth), Ls: g.intList()}
	if depth > 0 {
		s.Then, s.Else, s.Body = g.stmts(depth-1), g.stmts(depth-1), g.stmts(depth-1)
		switch g.r.IntN(3) {
		case 0:
		case 1:
			s.Cases = []*irCase{}
		default:
			for range 1 + g.r.IntN(2) {
				if g.r.IntN(6) == 0 {
					s.Cases = append(s.Cases, nil)
					continue
				}
				s.Cases = append(s.Cases, &irCase{Pos: g.pos(), Types: g.strList(), Body: g.stmts(depth - 1), Vals: g.exprs(depth - 1), Def: g.r.IntN(2) == 0})
			}
		}
	}
	return s
}

func (g *irGenerator) expr(depth int) *irExpr {
	x := &irExpr{K: g.text(), Pos: g.pos(), I: g.int(), T: g.text(), V: g.text(), Op: g.text(), Name: g.text(), Keys: g.strList(), Params: g.intList()}
	if depth > 0 {
		x.X, x.Y = g.optExpr(depth), g.optExpr(depth)
		x.Args, x.Elems, x.Body = g.exprs(depth-1), g.exprs(depth-1), g.stmts(depth-1)
	}
	if g.r.IntN(2) == 0 {
		p := g.pos()
		x.End = &p
	}
	return x
}

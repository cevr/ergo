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
// encoding/json's Marshal of the builder's irFile. It sends an IR file's
// nodes, children first, as the steps compiler/api.IRWrite replays into
// ir.Builder (irPack, by the kind table compiler/ir exports), and compares
// the text ir.Write writes from the rows built with Go's, byte for byte,
// on two domains:
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
	// The statement kind has the expression kind's slot count, so only
	// Write's struct type check refuses it.
	var p irPacked
	exprAt, stmtKind := 0, uint32(0)
	for exprAt == 0 {
		var err error
		if p, err = irPack(kinds, irGen(r, kinds, reached)); err != nil {
			t.Fatal(err)
		}
		for _, at := range p.rows {
			k := kinds.byCode[p.words[at]]
			if k.class != irTExpr {
				continue
			}
			for _, s := range kinds.list {
				if s.class == irTStmt && len(s.slots) == len(k.slots) {
					exprAt, stmtKind = at, s.code
				}
			}
		}
	}
	var bad [][]uint32
	w := append([]uint32(nil), p.words...)
	w[exprAt] = stmtKind
	bad = append(bad, w)
	w = append([]uint32(nil), p.words...)
	w[p.rows[0]] = uint32(len(kinds.list) + 1)
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

// irPacked is a file as the steps compiler/api.IRWrite replays into
// ir.Builder: its words, its texts, and the index in words of each row
// step's kind.
type irPacked struct {
	words []uint32
	strs  []string
	rows  []int
}

// The steps of compiler/api.IRWrite (compiler/api/ir.ergo).
const (
	irOpText = 1 + iota
	irOpWord
	irOpPos
	irOpList
	irOpRow
	irOpFunc
	irOpLocal
	irOpEnd
	irOpFile
	irOpShared
	irOpNoFiles
	irOpNoFuncs
)

type irPacker struct {
	kinds irKinds
	out   irPacked
	ids   map[string]uint32
}

// str is the host's index of s in strs (its own table; ir.Builder interns
// again).
func (p *irPacker) str(s string) uint32 {
	if id, ok := p.ids[s]; ok {
		return id
	}
	id := uint32(len(p.out.strs))
	p.out.strs = append(p.out.strs, s)
	p.ids[s] = id
	return id
}

func (p *irPacker) emit(ws ...uint32) { p.out.words = append(p.out.words, ws...) }

// pos pushes a position's slot word.
func (p *irPacker) pos(x irPos) error {
	ws, err := posWords(x)
	if err != nil {
		return err
	}
	p.emit(irOpPos)
	p.emit(ws...)
	return nil
}

func posWords(x irPos) ([]uint32, error) {
	for _, v := range x {
		if v < 0 || v > math.MaxUint32 {
			return nil, fmt.Errorf("position %v outside a word", x)
		}
	}
	return []uint32{uint32(x[0]), uint32(x[1]), uint32(x[2])}, nil
}

// irPack is the steps that build f: the files in File.Files' order (so
// ir.FileOf gives each its index), the shared texts, then each function's
// nodes children first.
func irPack(kinds irKinds, f *irFile) (irPacked, error) {
	p := &irPacker{kinds: kinds, out: irPacked{strs: []string{""}}, ids: map[string]uint32{"": 0}}
	p.emit(3, uint32(uint64(f.V)), uint32(uint64(f.V)>>32))
	seen := map[string]bool{}
	for _, path := range f.Files {
		if seen[path] {
			return p.out, fmt.Errorf("file %q twice", path)
		}
		seen[path] = true
		p.emit(irOpFile, p.str(path))
	}
	// The builder makes a list non-nil when it adds to it; only an empty
	// non-nil one needs a step.
	if f.Files != nil && len(f.Files) == 0 {
		p.emit(irOpNoFiles)
	}
	for _, s := range f.Shared {
		p.emit(irOpShared, p.str(s))
	}
	if f.Funcs != nil && len(f.Funcs) == 0 {
		p.emit(irOpNoFuncs)
	}
	for i := range f.Funcs {
		fn := &f.Funcs[i]
		pw, err := posWords(fn.Pos)
		if err != nil {
			return p.out, err
		}
		ew, err := posWords(fn.End)
		if err != nil {
			return p.out, err
		}
		p.emit(irOpFunc, p.str(fn.Name))
		p.emit(pw...)
		p.emit(ew...)
		for _, l := range fn.Locals {
			lw, err := posWords(l.Pos)
			if err != nil {
				return p.out, err
			}
			p.emit(irOpLocal, p.str(l.Name), p.str(l.Type))
			p.emit(lw...)
		}
		if err := p.list(reflect.ValueOf(fn.Body)); err != nil {
			return p.out, err
		}
		p.emit(irOpEnd)
	}
	return p.out, nil
}

// list pushes a slice of nodes: 0 for nil, else each element (0 for a nil
// one) and a list step.
func (p *irPacker) list(v reflect.Value) error {
	if v.IsNil() {
		p.emit(irOpWord, 0)
		return nil
	}
	for i := range v.Len() {
		if v.Index(i).IsNil() {
			p.emit(irOpWord, 0)
			continue
		}
		if err := p.row(v.Index(i)); err != nil {
			return err
		}
	}
	p.emit(irOpList, uint32(v.Len()))
	return nil
}

// row pushes the node *v (an *irStmt, *irExpr or *irCase): its slots'
// words in the kind's order, then a row step. A field Go set outside the
// kind's slots is an error: the kind table lags the builder.
func (p *irPacker) row(v reflect.Value) error {
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
		return fmt.Errorf("kind %q of struct type %d is not in compiler/ir's table", name, class)
	}
	slotted := map[int]bool{}
	for _, s := range k.slots {
		slotted[s] = true
	}
	for j := first; j < e.NumField(); j++ {
		if !slotted[j] && !e.Field(j).IsZero() {
			return fmt.Errorf("kind %q sets field %s outside its slots", name, e.Type().Field(j).Name)
		}
	}
	for _, s := range k.slots {
		if err := p.field(e.Field(s)); err != nil {
			return err
		}
	}
	pw, err := posWords(e.Field(first - 1).Interface().(irPos))
	if err != nil {
		return err
	}
	p.out.rows = append(p.out.rows, len(p.out.words)+1)
	p.emit(irOpRow, k.code)
	p.emit(pw...)
	return nil
}

// field pushes the slot word of one field (compiler/ir/ir.ergo's
// encodings); Go's zero value is the word 0.
func (p *irPacker) field(v reflect.Value) error {
	switch x := v.Interface().(type) {
	case string:
		p.emit(irOpText, p.str(x))
	case int:
		if x < math.MinInt32 || x > math.MaxInt32 {
			return fmt.Errorf("int %d outside int32", x)
		}
		p.emit(irOpWord, uint32(int32(x)))
	case *int:
		if x == nil {
			p.emit(irOpWord, 0)
			return nil
		}
		if *x < 0 || *x >= math.MaxUint32 {
			return fmt.Errorf("pointed int %d outside a word", *x)
		}
		p.emit(irOpWord, uint32(*x)+1)
	case bool:
		p.emit(irOpWord, flag(x))
	case irPos:
		if x == (irPos{}) {
			p.emit(irOpWord, 0)
			return nil
		}
		return p.pos(x)
	case *irPos:
		if x == nil {
			p.emit(irOpWord, 0)
			return nil
		}
		return p.pos(*x)
	case *irExpr:
		if x == nil {
			p.emit(irOpWord, 0)
			return nil
		}
		return p.row(v)
	case []string:
		if x == nil {
			p.emit(irOpWord, 0)
			return nil
		}
		for _, s := range x {
			p.emit(irOpText, p.str(s))
		}
		p.emit(irOpList, uint32(len(x)))
	case []int:
		if x == nil {
			p.emit(irOpWord, 0)
			return nil
		}
		for _, n := range x {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return fmt.Errorf("int %d outside int32", n)
			}
			p.emit(irOpWord, uint32(int32(n)))
		}
		p.emit(irOpList, uint32(len(x)))
	case []*irStmt, []*irExpr, []*irCase:
		return p.list(v)
	default:
		return fmt.Errorf("no slot encoding for %s", v.Type())
	}
	return nil
}

func flag(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// irGen is a seeded IR file built from the kind table: each node is of a
// kind of the table, each of the kind's slots set or not, every field
// outside them Go's zero, so each branch of the writer is reached. reached
// records the kinds it built.
func irGen(r *rand.Rand, kinds irKinds, reached map[string]bool) *irFile {
	g := &irGenerator{r: r, kinds: kinds, reached: reached}
	// Files are distinct, as the builder's are (ir.FileOf gives a path one
	// index).
	f := &irFile{V: g.int(), Files: g.strList(), Shared: g.strList()}
	for i, path := range f.Files {
		f.Files[i] = strconv.Itoa(i) + path
	}
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

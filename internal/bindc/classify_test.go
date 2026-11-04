package bindc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// This file holds TestClassificationTotality and its own rule table. The
// table restates each rule of the package doc from the C types alone; it
// calls no classifier code, so the property compares two separate readings
// of the rules.

// tEnv is one generated model and contract, as the test's table sees them.
type tEnv struct {
	types   map[string]TypeDecl
	c       *Contract
	handles map[string]HandleRule
	creates map[string]HandleRule
	exc     map[string]map[int]Exception
	// claimed holds "function/count" for the count of an out_buffer
	// exception: the generator gives it no exception of its own.
	claimed map[string]bool
}

func (e *tEnv) scalar(t Type) (Scalar, bool) {
	if t.Kind == TypeScalar {
		return t.Scalar, true
	}
	if d, ok := e.types[t.Name]; t.Kind == TypeNamed && ok && d.Kind == DeclScalar {
		return d.Scalar, true
	}
	return "", false
}

func (e *tEnv) unsigned(t Type) bool {
	s, ok := e.scalar(t)
	return ok && strings.HasPrefix(string(s), "uint")
}

// noPointer: a scalar, a scalar typedef, a struct whose fields are all
// noPointer, or an array of those.
func (e *tEnv) noPointer(t Type) bool {
	if _, ok := e.scalar(t); ok {
		return true
	}
	if t.Kind == TypeArray {
		return e.noPointer(*t.Elem)
	}
	d, ok := e.types[t.Name]
	if t.Kind != TypeNamed || !ok || d.Kind != DeclStruct {
		return false
	}
	for _, f := range d.Fields {
		if !e.noPointer(f.Type) {
			return false
		}
	}
	return true
}

// item: a type a buffer holds or an out-parameter receives.
func (e *tEnv) item(t Type) bool { return t.Kind != TypeArray && e.noPointer(t) }

func (e *tEnv) isStruct(t Type) bool {
	d, ok := e.types[t.Name]
	return t.Kind == TypeNamed && ok && d.Kind == DeclStruct
}

func (e *tEnv) pointerPos(t Type) bool {
	d, ok := e.types[t.Name]
	return t.Kind == TypePointer || t.Kind == TypeNamed && ok && d.Kind == DeclCallback
}

// fill puts the buffer name into a length template.
func fill(templ, name string) (string, bool) {
	if strings.Contains(templ, "{") && name == "" {
		return "", false
	}
	stem := name
	if len(name) >= 2 && name[len(name)-1] == 's' {
		stem = name[:len(name)-1]
	}
	return strings.NewReplacer("{name}", name, "{stem}", stem).Replace(templ), true
}

// templRE matches every name a template gives for some buffer name.
func templRE(templ string) *regexp.Regexp {
	q := regexp.QuoteMeta(templ)
	q = strings.NewReplacer(`\{name\}`, `[A-Za-z0-9_]+`, `\{stem\}`, `[A-Za-z0-9_]+`).Replace(q)
	return regexp.MustCompile("^" + q + "$")
}

// length: q after the buffer p is its length by a length_names template
// (elements) and by a byte_length_names template (bytes). Both can hold;
// then two rules match.
func (e *tEnv) length(p, q Param) (elements, bytes bool) {
	if q.Name == "" || !e.unsigned(q.Type) {
		return false, false
	}
	hit := func(list []string) bool {
		for _, templ := range list {
			if n, ok := fill(templ, p.Name); ok && n == q.Name {
				return true
			}
		}
		return false
	}
	return hit(e.c.Buffers.LengthNames), hit(e.c.Buffers.ByteLengthNames)
}

func (e *tEnv) capName(q Param) bool {
	return q.Name != "" && e.unsigned(q.Type) && slices.Contains(e.c.Buffers.CapacityNames, q.Name)
}

func (e *tEnv) lengthish(name string) bool {
	for _, templ := range append(slices.Clone(e.c.Buffers.LengthNames), e.c.Buffers.ByteLengthNames...) {
		if name != "" && templRE(templ).MatchString(name) {
			return true
		}
	}
	return false
}

// alone: not before a capacity or a length-like name, not after a capacity.
func (e *tEnv) alone(ps []Param, i int) bool {
	if i+1 < len(ps) && (e.capName(ps[i+1]) || e.unsigned(ps[i+1].Type) && e.lengthish(ps[i+1].Name)) {
		return false
	}
	return i == 0 || !e.capName(ps[i-1])
}

// counter: c is a capacity and p a `U *` count of the same U with a count name.
func (e *tEnv) counter(c, p Param) bool {
	if p.Type.Kind != TypePointer || p.Type.Elem.Const || p.Type.LenExpr != "" || p.Name == "" {
		return false
	}
	a, ok1 := e.scalar(*p.Type.Elem)
	b, ok2 := e.scalar(c.Type)
	return ok1 && ok2 && a == b && e.unsigned(c.Type) && slices.Contains(e.c.Buffers.CountNames, p.Name)
}

func isMutPtr(t Type) bool   { return t.Kind == TypePointer && !t.Elem.Const && t.LenExpr == "" }
func isConstPtr(t Type) bool { return t.Kind == TypePointer && t.Elem.Const && t.LenExpr == "" }

// tRule is one rule of the table.
type tRule struct {
	name  string
	class Class
	match func(e *tEnv, f *Function, i int) bool
}

var tRules = []tRule{
	{"handle", ClassHandle, func(e *tEnv, f *Function, i int) bool {
		t := f.Params[i].Type
		_, ok := e.handles[t.Elem.Name]
		return t.Kind == TypePointer && t.LenExpr == "" && t.Elem.Kind == TypeNamed && ok
	}},
	{"create", ClassOut, func(e *tEnv, f *Function, i int) bool {
		h, ok := e.creates[f.Name]
		t := f.Params[i].Type
		return ok && f.Params[i].Name == h.Out && isMutPtr(t) && t.Elem.Kind == TypePointer &&
			!t.Elem.Elem.Const && t.Elem.Elem.Kind == TypeNamed && t.Elem.Elem.Name == h.Type
	}},
	{"out", ClassOut, func(e *tEnv, f *Function, i int) bool {
		p := f.Params[i]
		return isMutPtr(p.Type) && p.Name != "" && strings.HasPrefix(p.Name, e.c.Buffers.OutPrefix) &&
			e.item(*p.Type.Elem) && e.alone(f.Params, i)
	}},
	{"value", ClassValue, func(e *tEnv, f *Function, i int) bool {
		t := f.Params[i].Type
		if t.Kind != TypePointer || !t.Elem.Const || !e.alone(f.Params, i) {
			return false
		}
		if t.LenExpr != "" {
			return e.item(*t.Elem)
		}
		return e.isStruct(*t.Elem) && e.noPointer(*t.Elem)
	}},
	{"in_buffer", ClassInBuffer, func(e *tEnv, f *Function, i int) bool {
		p := f.Params[i]
		if !isConstPtr(p.Type) || !e.item(*p.Type.Elem) || i+1 >= len(f.Params) {
			return false
		}
		elements, _ := e.length(p, f.Params[i+1])
		return elements
	}},
	{"in_buffer_bytes", ClassInBuffer, func(e *tEnv, f *Function, i int) bool {
		p := f.Params[i]
		if !isConstPtr(p.Type) || !e.item(*p.Type.Elem) || i+1 >= len(f.Params) {
			return false
		}
		_, bytes := e.length(p, f.Params[i+1])
		return bytes
	}},
	{"out_buffer", ClassOutBuffer, func(e *tEnv, f *Function, i int) bool {
		p := f.Params[i]
		return isMutPtr(p.Type) && e.item(*p.Type.Elem) && i+2 < len(f.Params) &&
			e.capName(f.Params[i+1]) && e.counter(f.Params[i+1], f.Params[i+2])
	}},
	{"out_count", ClassOutCount, func(e *tEnv, f *Function, i int) bool {
		return i >= 2 && e.capName(f.Params[i-1]) && e.counter(f.Params[i-1], f.Params[i]) &&
			isMutPtr(f.Params[i-2].Type) && e.item(*f.Params[i-2].Type.Elem)
	}},
}

// tPos is the table's class of one position.
type tPos struct {
	param         int
	class         Class
	length, count int
	bytes         bool
	why           string // "none", "two", "exception" for a raw position
}

// expect classifies every pointer position of f by the table, and says
// whether f is typed.
func (e *tEnv) expect(f *Function) ([]tPos, bool, []string) {
	var out []tPos
	at := map[int]int{}
	for i, p := range f.Params {
		if !e.pointerPos(p.Type) {
			continue
		}
		at[i] = len(out)
		if x, ok := e.exc[f.Name][i]; ok {
			q := tPos{param: i, class: x.Class, length: -1, count: -1, why: "exception"}
			if x.Class == ClassInBuffer || x.Class == ClassOutBuffer {
				q.length = e.ref(f, x.Length)
				q.bytes = x.Unit == "bytes"
			}
			if x.Class == ClassOutBuffer {
				q.count = e.ref(f, x.Count)
			}
			out = append(out, q)
			continue
		}
		var hits []tRule
		if p.Type.Kind == TypePointer {
			for _, r := range tRules {
				if r.match(e, f, i) {
					hits = append(hits, r)
				}
			}
		}
		q := tPos{param: i, length: -1, count: -1}
		switch len(hits) {
		case 0:
			q.class, q.why = ClassRaw, "none"
		case 1:
			q.class = hits[0].class
			switch hits[0].name {
			case "in_buffer", "in_buffer_bytes":
				q.length = i + 1
				q.bytes = hits[0].name == "in_buffer_bytes"
			case "out_buffer":
				q.length, q.count = i+1, i+2
			case "out_count":
				q.length, q.count = i-1, i-2
			}
		default:
			q.class, q.why = ClassRaw, "two"
			names := map[string]bool{}
			for _, h := range hits {
				names[h.name] = true
			}
			if names["in_buffer"] && names["in_buffer_bytes"] {
				q.why = "units"
			}
		}
		out = append(out, q)
	}
	for _, q := range slices.Clone(out) {
		if q.class == ClassOutBuffer && q.why == "exception" {
			out[at[q.count]] = tPos{param: q.count, class: ClassOutCount, length: q.length, count: q.param, why: "exception"}
		}
	}
	if e.pointerPos(f.Result) {
		out = append(out, tPos{param: -1, class: ClassRaw, length: -1, count: -1, why: "result"})
	}
	var whys []string
	for _, q := range out {
		if q.class == ClassRaw {
			whys = append(whys, q.why)
		}
	}
	if len(whys) > 0 {
		return out, false, whys
	}
	// The parts of each buffer fit together.
	lengths := map[int]bool{}
	for _, q := range out {
		if q.class == ClassInBuffer || q.class == ClassOutBuffer {
			if lengths[q.length] {
				return out, false, []string{"length twice"}
			}
			lengths[q.length] = true
		}
		if q.class == ClassOutBuffer {
			j, ok := at[q.count]
			if !ok || out[j].class != ClassOutCount || out[j].count != q.param {
				return out, false, []string{"count"}
			}
		}
		if q.class == ClassOutCount {
			j, ok := at[q.count]
			if !ok || out[j].class != ClassOutBuffer || out[j].count != q.param {
				return out, false, []string{"orphan count"}
			}
		}
	}
	for i, p := range f.Params {
		if _, isPos := at[i]; !isPos && !lengths[i] && !e.noPointer(p.Type) {
			return out, false, []string{"by value"}
		}
	}
	if f.Result.Kind != TypeVoid && !e.pointerPos(f.Result) && !e.noPointer(f.Result) {
		return out, false, []string{"result by value"}
	}
	return out, true, nil
}

func (e *tEnv) ref(f *Function, r string) int {
	if strings.HasPrefix(r, "#") {
		var n int
		fmt.Sscanf(r, "#%d", &n)
		return n
	}
	for i, p := range f.Params {
		if p.Name == r {
			return i
		}
	}
	return -1
}

// The generator. It builds a model and a contract from fixed pools of
// names and types, and puts the shapes of the rules (buffers, triples,
// chained triples, out-parameters, values) next to each other often.

var (
	// gLengthPool overlaps itself on purpose: {name}_X and {stem}_X give one
	// name for a buffer without a final "s", "byte_count" is {stem}_count of
	// "bytes", and "data_count" and "a_len" are {name}_count and {name}_len
	// of two pool names. The contract puts each template in one list, so
	// an overlap across the lists is a length with two units.
	gLengthPool = []string{"{name}_count", "{stem}_count", "{name}_len", "{stem}_len", "count", "n", "{name}_bytes",
		"{stem}_bytes", "byte_count", "size", "data_count", "a_len"}
	gCapPool   = []string{"capacity", "cap", "n", "size"}
	gCountPool = []string{"out_count", "out_n", "count", "o_len"}
	gOutPool   = []string{"out_", "o_", "ret_"}
	gNamePool  = []string{"a", "bytes", "items", "data", "out_x", "o_y", "ret_v", "out_count", "out_n", "count", "capacity",
		"cap", "n", "size", "a_count", "item_count", "a_len", "a_bytes", "byte_count", "data_len", "items_count", "o_len", "out_items"}
	gPointees = []string{"uint8_t", "uint32_t", "uint64_t", "float", "g_u", "g_s0", "g_s1", "g_s2", "g_s3", "g_h", "g_o", "void"}
	gScalars  = []string{"uint32_t", "uint64_t", "int32_t", "float", "g_u", "uint8_t"}
)

// gModelSrc is the fixed part of every generated header: a status type,
// the ABI pair, structs with and without pointers, a handle, another
// opaque type and a callback.
const gModelSrc = `#include <stdint.h>
#include <stdbool.h>

#define G_OK INT32_C(0)
#define G_FAIL (-INT32_C(1))
#define G_ABI UINT32_C(1)

typedef int32_t g_status;
typedef uint32_t g_u;
typedef struct g_s0 { uint32_t a; float b; } g_s0;
typedef struct g_s1 { const uint8_t *p; uint32_t n; } g_s1;
typedef struct g_s2 { g_s1 inner; uint32_t k; } g_s2;
typedef struct g_s3 { g_s0 list[2]; g_u u; } g_s3;
typedef struct g_h g_h;
typedef struct g_o g_o;
typedef void (*g_cb)(void *user_data);

uint32_t g_abi(void);
g_status g_create(const g_s0 *options, g_h **out_h);
g_status g_destroy(g_h *h);
`

type gen2 struct {
	r *rand.Rand
}

func (g *gen2) pick(xs []string) string { return xs[g.r.IntN(len(xs))] }

func (g *gen2) subset(xs []string, min int) []string {
	var out []string
	for _, x := range xs {
		if g.r.IntN(2) == 0 {
			out = append(out, x)
		}
	}
	for len(out) < min {
		if x := g.pick(xs); !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// param is one C parameter declaration; used keeps names unique.
func (g *gen2) name(used map[string]bool) string {
	if g.r.IntN(5) == 0 {
		return ""
	}
	n := g.pick(gNamePool)
	if used[n] {
		return ""
	}
	used[n] = true
	return n
}

func decl(typ, name string) string {
	if name == "" {
		return typ
	}
	if strings.HasSuffix(typ, "*") {
		return typ + name
	}
	return typ + " " + name
}

func (g *gen2) ptr(constant bool) string {
	t := g.pick(gPointees)
	if constant {
		return "const " + t + " *"
	}
	return t + " *"
}

// shape is a run of parameters: one of the rule shapes, or a single one.
func (g *gen2) shape(used map[string]bool, c *Contract) []string {
	named := func(pool []string) string {
		n := g.pick(pool)
		if used[n] {
			return g.name(used)
		}
		used[n] = true
		return n
	}
	bufName := func() string {
		n := g.name(used)
		return n
	}
	switch g.r.IntN(9) {
	case 0: // an input buffer and a length
		b := bufName()
		l := ""
		templ := g.pick(append(slices.Clone(c.Buffers.LengthNames), c.Buffers.ByteLengthNames...))
		if n, ok := fill(templ, b); ok && !used[n] {
			l, used[n] = n, true
		}
		// Take a length that templates of both lists give, when one exists:
		// a length with two units.
		for _, te := range c.Buffers.LengthNames {
			for _, tb := range c.Buffers.ByteLengthNames {
				ne, ok1 := fill(te, b)
				nb, ok2 := fill(tb, b)
				if ok1 && ok2 && ne == nb && !used[ne] {
					if l != "" {
						delete(used, l)
					}
					l, used[ne] = ne, true
				}
			}
		}
		if l == "" {
			l = named(gNamePool)
		}
		return []string{decl(g.ptr(g.r.IntN(4) != 0), b), decl(g.pick(gScalars), l)}
	case 1, 2: // an output triple
		return []string{decl(g.ptr(g.r.IntN(4) == 0), bufName()), decl(g.pick([]string{"uint32_t", "uint64_t", "g_u"}), named(c.Buffers.CapacityNames)),
			decl(g.pick([]string{"uint32_t *", "uint64_t *", "g_u *"}), named(c.Buffers.CountNames))}
	case 3: // a chained triple: the count of one is the buffer of the next
		return []string{decl(g.ptr(false), bufName()), decl("uint32_t", named(c.Buffers.CapacityNames)),
			decl("uint32_t *", named(c.Buffers.CountNames)), decl("uint32_t", named(c.Buffers.CapacityNames)),
			decl("uint32_t *", named(c.Buffers.CountNames))}
	case 4: // an out-parameter
		n := c.Buffers.OutPrefix + g.pick([]string{"x", "y", "items", "count"})
		if used[n] {
			n = ""
		}
		used[n] = true
		return []string{decl(g.ptr(false), n)}
	case 5: // a value or an array parameter
		if g.r.IntN(3) == 0 {
			n := named(gNamePool)
			if n == "" {
				n = "arr"
				if used[n] {
					return nil
				}
				used[n] = true
			}
			return []string{g.pick([]string{"const uint16_t ", "uint16_t ", "const g_s0 ", "const g_s1 "}) + n + "[4]"}
		}
		return []string{decl(g.pick([]string{"const g_s0 *", "const g_s1 *", "const g_s3 *", "const g_h *", "g_h *"}), g.name(used))}
	case 6: // a callback, a handle out or a pointer to a pointer
		n := g.pick([]string{"", "out_h", "h", "user_data"})
		if used[n] {
			n = ""
		}
		used[n] = true
		return []string{decl(g.pick([]string{"g_cb", "g_h **", "uint8_t **", "void *"}), n)}
	case 7: // a by-value parameter
		return []string{decl(g.pick(append(slices.Clone(gScalars), "g_s0", "g_s1")), g.name(used))}
	}
	return []string{decl(g.ptr(g.r.IntN(2) == 0), g.name(used))}
}

// header generates the functions of one case.
func (g *gen2) header(c *Contract, n int) string {
	var b strings.Builder
	b.WriteString(gModelSrc)
	results := []string{"g_status", "g_status", "void", "uint32_t", "uint8_t *", "g_s0", "g_s1", "g_cb", "g_u"}
	for i := 0; i < n; i++ {
		used := map[string]bool{}
		var ps []string
		for len(ps) < 1+g.r.IntN(5) {
			ps = append(ps, g.shape(used, c)...)
		}
		params := "void"
		if len(ps) > 0 {
			params = strings.Join(ps, ", ")
		}
		res := g.pick(results)
		sep := " "
		if strings.HasSuffix(res, "*") {
			sep = ""
		}
		fmt.Fprintf(&b, "%s%sg_f%d(%s);\n", res, sep, i, params)
	}
	return b.String()
}

func (g *gen2) contract() *Contract {
	c := &Contract{
		Header: "gen.h",
		Go:     GoTarget{Package: "gen", ImportPath: "ergo.local/bind/gen"},
		Status: StatusRule{Type: "g_status", MacroPrefix: "G_", Success: "G_OK", Exclude: []Skip{}},
		ABI:    ABIRule{Function: "g_abi", Macro: "G_ABI"},
		Handles: []HandleRule{{Type: "g_h", Create: "g_create", Out: "out_h", Destroy: "g_destroy",
			Thread: g.pick([]string{"owner", "any"})}},
		Autofill:   []AutofillRule{},
		Callbacks:  []CallbackRule{},
		Exceptions: []Exception{},
		Skips:      []Skip{},
	}
	lens := g.subset(gLengthPool, 1)
	for _, l := range lens {
		if g.r.IntN(3) == 0 {
			c.Buffers.ByteLengthNames = append(c.Buffers.ByteLengthNames, l)
		} else {
			c.Buffers.LengthNames = append(c.Buffers.LengthNames, l)
		}
	}
	if len(c.Buffers.LengthNames) == 0 {
		c.Buffers.LengthNames = []string{"{name}_count"}
		c.Buffers.ByteLengthNames = slices.DeleteFunc(c.Buffers.ByteLengthNames, func(s string) bool { return s == "{name}_count" })
	}
	if c.Buffers.ByteLengthNames == nil {
		c.Buffers.ByteLengthNames = []string{}
	}
	c.Buffers.CapacityNames = g.subset(gCapPool, 1)
	c.Buffers.CountNames = g.subset(gCountPool, 1)
	c.Buffers.OutPrefix = g.pick(gOutPool)
	return c
}

// exceptions adds exceptions that fit their positions by the table, and
// differ from what the rules give.
func (g *gen2) exceptions(e *tEnv, m *Model) {
	for fi := range m.Functions {
		f := &m.Functions[fi]
		if !strings.HasPrefix(f.Name, "g_f") {
			continue
		}
		for i, p := range f.Params {
			if p.Type.Kind != TypePointer && !e.pointerPos(p.Type) || g.r.IntN(6) != 0 {
				continue
			}
			pos := p.Name
			if pos == "" {
				pos = fmt.Sprintf("#%d", i)
			}
			x := Exception{Function: f.Name, Position: pos, Why: "generated"}
			t := p.Type
			var lens, counts []string
			for j, q := range f.Params {
				if j != i && q.Name != "" && e.unsigned(q.Type) {
					lens = append(lens, q.Name)
				}
				if j != i && q.Name != "" && isMutPtr(q.Type) && e.unsigned(*q.Type.Elem) {
					counts = append(counts, q.Name)
				}
			}
			switch {
			case t.Kind != TypePointer:
				x.Class = ClassRaw
			case isConstPtr(t) && e.isStruct(*t.Elem) && e.noPointer(*t.Elem) || t.LenExpr != "" && t.Elem.Const && e.item(*t.Elem):
				x.Class = ClassValue
				if len(lens) > 0 && isConstPtr(t) && g.r.IntN(2) == 0 {
					x.Class, x.Length, x.Unit = ClassInBuffer, g.pick(lens), g.pick([]string{"elements", "bytes"})
				}
			case isConstPtr(t) && e.item(*t.Elem) && len(lens) > 0:
				x.Class, x.Length, x.Unit = ClassInBuffer, g.pick(lens), g.pick([]string{"elements", "bytes"})
			case isMutPtr(t) && e.item(*t.Elem):
				x.Class = ClassOut
				if len(lens) > 0 && len(counts) > 0 && g.r.IntN(2) == 0 {
					capName, countName := g.pick(lens), g.pick(counts)
					cj, kj := e.ref(f, capName), e.ref(f, countName)
					ca, _ := e.scalar(f.Params[cj].Type)
					cb, _ := e.scalar(*f.Params[kj].Type.Elem)
					if _, has := e.exc[f.Name][kj]; ca == cb && !has {
						x.Class, x.Length, x.Count = ClassOutBuffer, capName, countName
					}
				}
			default:
				x.Class = ClassRaw
			}
			if x.Class == ClassOutBuffer {
				if _, has := e.exc[f.Name][e.ref(f, x.Count)]; has {
					continue
				}
				// The count may not have an exception, now or later.
				if e.claimed == nil {
					e.claimed = map[string]bool{}
				}
				e.claimed[f.Name+"/"+x.Count] = true
			}
			if e.claimed[f.Name+"/"+p.Name] && p.Name != "" {
				continue
			}
			// Leave out an exception that repeats the rule: Bind refuses it.
			if x.Class != ClassRaw {
				var hits []tRule
				for _, r := range tRules {
					if r.match(e, f, i) {
						hits = append(hits, r)
					}
				}
				if len(hits) == 1 && hits[0].class == x.Class {
					continue
				}
			}
			if e.exc[f.Name] == nil {
				e.exc[f.Name] = map[int]Exception{}
			}
			e.exc[f.Name][i] = x
			e.c.Exceptions = append(e.c.Exceptions, x)
		}
	}
}

// TestClassificationTotality: for generated prototypes and contracts,
// every pointer position gets exactly one class, the class equals the one
// the table above computes, a position that zero or two rules match is
// raw, and a function is typed exactly when the table says so.
func TestClassificationTotality(t *testing.T) {
	stats := map[string]int{}
	defer func() { t.Logf("%d cases: %v", *propCases, stats) }()
	for n := 0; n < *propCases; n++ {
		seed := *propSeed + uint64(n)
		g := &gen2{r: rand.New(rand.NewPCG(seed, 0x6b1d))}
		c := g.contract()
		src := g.header(c, 3+n%12)
		m, err := Parse("gen.h", []byte(src))
		if err != nil {
			t.Fatalf("seed %d: the generated header does not parse:\n%v\n%s", seed, err, src)
		}
		e := &tEnv{types: map[string]TypeDecl{}, c: c, handles: map[string]HandleRule{}, creates: map[string]HandleRule{},
			exc: map[string]map[int]Exception{}}
		for _, d := range m.Types {
			e.types[d.Name] = d
		}
		for _, h := range c.Handles {
			e.handles[h.Type], e.creates[h.Create] = h, h
		}
		g.exceptions(e, m)
		sum := sha256.Sum256([]byte(src))
		c.HeaderSHA256 = hex.EncodeToString(sum[:])
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Bind(m, []byte(src), data)
		if err != nil {
			// A function whose call purego cannot make as C does on every
			// target (abi.go, A6) is refused for its call plan, whatever
			// its classes; the contract skips it, as its author would.
			if d2, skipped := skipCallPlans(t, err, data); len(skipped) > 0 {
				stats["skipped for the call plan"] += len(skipped)
				data = d2
				b, err = Bind(m, []byte(src), data)
			}
		}
		if err != nil {
			t.Fatalf("seed %d: Bind: %v\ncontract %s\n%s", seed, err, data, src)
		}
		for _, bf := range b.Funcs {
			want, typed, whys := e.expect(bf.C)
			got := bf.Positions
			fail := func(format string, args ...any) {
				t.Fatalf("seed %d: %s: %s\ncontract %s\n%s", seed, bf.C.Name, fmt.Sprintf(format, args...), data, src)
			}
			if len(got) != len(want) {
				fail("%d positions, the table has %d", len(got), len(want))
			}
			for i, q := range got {
				w := want[i]
				if q.Param != w.param || q.Class != w.class {
					fail("position %d: class %s, the table says %s (%s)", q.Param, q.Class, w.class, w.why)
				}
				if q.Class != ClassRaw && (q.Length != w.length || q.Count != w.count || q.Bytes != w.bytes) {
					fail("position %d: length %d count %d bytes %v, the table says %d %d %v", q.Param, q.Length, q.Count, q.Bytes, w.length, w.count, w.bytes)
				}
				stats["class "+string(q.Class)]++
				if w.why != "" {
					stats["by "+w.why]++
				}
			}
			if typed != (bf.RawOnly == "") {
				fail("raw-only %q, the table says typed=%v (%v)", bf.RawOnly, typed, whys)
			}
			for _, why := range whys {
				switch why {
				case "two":
					if !strings.Contains(bf.RawOnly, "both cover it") {
						fail("two rules match, but the reason is %q", bf.RawOnly)
					}
				case "units":
					for _, s := range []string{"in_buffer (", "in_buffer_bytes (", "counts elements", "counts bytes", "both cover it"} {
						if !strings.Contains(bf.RawOnly, s) {
							fail("a length counts elements and bytes, but the reason %q does not say %q", bf.RawOnly, s)
						}
					}
				case "none":
					if !strings.Contains(bf.RawOnly, "no rule covers it") && !strings.Contains(bf.RawOnly, "callback") &&
						!strings.Contains(bf.RawOnly, "holds a pointer") {
						fail("no rule matches, but the reason is %q", bf.RawOnly)
					}
				}
			}
			if typed {
				stats["typed"]++
			} else {
				stats["raw-only"]++
			}
		}
		stats["exceptions"] += len(c.Exceptions)
	}
	if !testingShort() {
		for _, k := range []string{"by two", "by units", "by none", "by exception", "class in_buffer", "class out_buffer",
			"class out_count", "class out", "class value", "class handle", "typed"} {
			if stats[k] < *propCases/20 {
				t.Errorf("the generator makes %d positions of %q in %d cases; want at least %d", stats[k], k, *propCases, *propCases/20)
			}
		}
	}
}

func testingShort() bool { return testing.Short() }

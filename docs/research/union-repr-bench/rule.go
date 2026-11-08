package unionreprbench

import (
	"sort"
	"strconv"
	"strings"
)

// Rule G (round 2): the Go form of each union of a program, chosen by one
// function over the union shapes. The R1 seam (`unionRepr`) is to port
// this function; rule_test.go builds every outcome as real Go.
//
// The steps, in order, for each union (a generic union per instance):
//
//  1. A variant is an error type → A (a row value is a Go error, and
//     ConvRow and catch test it by type: `err.(U)`).
//  2. The union reaches itself through any field (directly, through a
//     record, through another union, through a generic instance or
//     through a slice) → A. Go rejects a struct that contains itself, and
//     A is the box that breaks the cycle (Swift `indirect`, OCaml blocks).
//  3. All variants empty → B.
//  4. Exactly two variants, one with fields and one empty → C.
//  5. Otherwise → D.
//  6. A C or D struct must be one that Go keeps in registers
//     (cmd/compile/internal/ssa CanSSA: at most 32 bytes, at most 4 fields
//     at each struct level, no array) → else A. Measured: above it a
//     value carried through a call costs 16 to 19 ns (a store-to-load
//     forwarding stall on the copy), for C and D alike.
//  7. A C or D union that an A union holds by value (in a variant field,
//     directly or through a record) → A. Measured: a D state boxed in an
//     A Decision is slower than all-A (+70%, round-1 findings M2), because
//     the box gets larger and Go's static boxes for small integers no
//     longer apply. A slice element is not held by value (the slice is
//     its own heap object).
//
// Steps 6 and 7 repeat until no union changes (a union that becomes A
// changes the size of the unions that hold it). A union only ever moves
// to A, so the loop ends.

// Form is the Go form of a union.
type Form uint8

const (
	FormA Form = iota + 1 // sealed interface
	FormB                 // uint8 enum
	FormC                 // struct{ v T; ok bool }
	FormD                 // struct{ tag uint8; slots }
)

func (f Form) String() string {
	switch f {
	case FormA:
		return "A"
	case FormB:
		return "B"
	case FormC:
		return "C"
	case FormD:
		return "D"
	}
	return "?"
}

// Kind is the kind of a field type in the shape model.
type Kind uint8

const (
	KInt    Kind = iota + 1 // int64
	KString                 // string
	KBool                   // bool
	KSlice                  // []Elem
	KRecord                 // a record of the program, by Name
	KUnion                  // a union (instance) of the program, by Name
)

// Type is a field type. A generic union appears by its instance: the
// shape model holds `Box[Rec]` as its own union (named Box_Rec), with the
// type argument substituted into the fields.
type Type struct {
	Kind Kind
	Name string
	Elem *Type
}

type Field struct {
	Name string
	Type Type
}

type Variant struct {
	Name   string
	Fields []Field
	Error  bool // the variant is an error type (named in an error row)
}

type Union struct {
	Name     string
	Variants []Variant
}

type Record struct {
	Name   string
	Fields []Field
}

type Program struct {
	Unions  []Union
	Records []Record
}

// Why names the step that decided a union's form.
type Why string

const (
	WhyError     Why = "a variant is an error type"
	WhyRecursive Why = "recursive"
	WhyEnum      Why = "all variants empty"
	WhyOption    Why = "option shape"
	WhyFlat      Why = "flat slots"
	WhySize      Why = "above the SSA limit"
	WhyHeld      Why = "held by value in an A union"
)

// Choice is the rule's result for one union.
type Choice struct {
	Form Form
	Why  Why
	// Size is the Go size in bytes of the C or D struct (amd64).
	Size int64
	// Slots (form D) are the slot types; FieldSlot[v][f] is the slot of
	// field f of variant v. Two fields share a slot only when they have
	// the same Go type and are in different variants.
	Slots     []Type
	FieldSlot [][]int
}

// Go's SSA limit (cmd/compile/internal/ssa/value.go CanSSA, amd64).
const (
	ssaBytes  = 32 // 4 × PtrSize
	ssaFields = 4  // MaxStruct
)

// ruleOptions select the steps; RuleG uses every step. The round-1 rule
// is checksLast and no bound on C and no hold step (rule_test.go keeps
// it to show that it fails to build).
type ruleOptions struct {
	checksLast bool // apply steps 1 and 2 only to D (round 1)
	boundC     bool // apply step 6 to C
	held       bool // apply step 7
}

// RuleG chooses the form of every union of p.
func RuleG(p Program) map[string]Choice {
	return ruleWith(p, ruleOptions{boundC: true, held: true})
}

type ruler struct {
	p       Program
	unions  map[string]*Union
	records map[string]*Record
	out     map[string]Choice
	// visiting holds the unions whose layout is being computed.
	visiting map[string]bool
}

func ruleWith(p Program, o ruleOptions) map[string]Choice {
	r := &ruler{p: p, unions: map[string]*Union{}, records: map[string]*Record{}, out: map[string]Choice{}, visiting: map[string]bool{}}
	for i := range p.Unions {
		r.unions[p.Unions[i].Name] = &p.Unions[i]
	}
	for i := range p.Records {
		r.records[p.Records[i].Name] = &p.Records[i]
	}
	for _, u := range p.Unions {
		err, rec := hasError(u), r.recursive(u.Name)
		var c Choice
		switch {
		case !o.checksLast && err:
			c = Choice{Form: FormA, Why: WhyError}
		case !o.checksLast && rec:
			c = Choice{Form: FormA, Why: WhyRecursive}
		case allEmpty(u):
			c = Choice{Form: FormB, Why: WhyEnum}
		case optionShape(u):
			c = Choice{Form: FormC, Why: WhyOption}
		case o.checksLast && err:
			c = Choice{Form: FormA, Why: WhyError}
		case o.checksLast && rec:
			c = Choice{Form: FormA, Why: WhyRecursive}
		default:
			c = Choice{Form: FormD, Why: WhyFlat}
		}
		r.out[u.Name] = c
	}
	for changed := true; changed; {
		changed = false
		for _, u := range p.Unions {
			c := r.out[u.Name]
			if c.Form != FormC && c.Form != FormD {
				continue
			}
			l := r.unionLayout(u, c.Form)
			bounded := c.Form == FormD || o.boundC
			switch {
			case bounded && !l.ssa:
				r.out[u.Name] = Choice{Form: FormA, Why: WhySize}
				changed = true
			case o.held && r.heldByA(u.Name):
				r.out[u.Name] = Choice{Form: FormA, Why: WhyHeld}
				changed = true
			default:
				c.Size = l.size
				c.Slots, c.FieldSlot = nil, nil
				if c.Form == FormD {
					c.Slots, c.FieldSlot = slotsOf(u)
				}
				r.out[u.Name] = c
			}
		}
	}
	return r.out
}

func hasError(u Union) bool {
	for _, v := range u.Variants {
		if v.Error {
			return true
		}
	}
	return false
}

func allEmpty(u Union) bool {
	for _, v := range u.Variants {
		if len(v.Fields) > 0 {
			return false
		}
	}
	return true
}

func optionShape(u Union) bool {
	return len(u.Variants) == 2 && (len(u.Variants[0].Fields) == 0) != (len(u.Variants[1].Fields) == 0)
}

// payload is the variant with fields of an option-shaped union.
func payload(u Union) Variant {
	if len(u.Variants[0].Fields) > 0 {
		return u.Variants[0]
	}
	return u.Variants[1]
}

// recursive reports whether union name reaches itself through any field
// type (records, unions and slice elements included).
func (r *ruler) recursive(name string) bool {
	seen := map[string]bool{}
	var reach func(t Type) bool
	var fields func(fs []Field) bool
	fields = func(fs []Field) bool {
		for _, f := range fs {
			if reach(f.Type) {
				return true
			}
		}
		return false
	}
	reach = func(t Type) bool {
		switch t.Kind {
		case KSlice:
			return reach(*t.Elem)
		case KRecord:
			if seen["r:"+t.Name] {
				return false
			}
			seen["r:"+t.Name] = true
			return fields(r.records[t.Name].Fields)
		case KUnion:
			if t.Name == name {
				return true
			}
			if seen["u:"+t.Name] {
				return false
			}
			seen["u:"+t.Name] = true
			for _, v := range r.unions[t.Name].Variants {
				if fields(v.Fields) {
					return true
				}
			}
		}
		return false
	}
	for _, v := range r.unions[name].Variants {
		if fields(v.Fields) {
			return true
		}
	}
	return false
}

// heldByA reports whether an A union holds union name by value: in a
// variant field, directly or through records (not through a slice).
func (r *ruler) heldByA(name string) bool {
	var byValue func(t Type, seen map[string]bool) bool
	byValue = func(t Type, seen map[string]bool) bool {
		switch t.Kind {
		case KUnion:
			return t.Name == name
		case KRecord:
			if seen[t.Name] {
				return false
			}
			seen[t.Name] = true
			for _, f := range r.records[t.Name].Fields {
				if byValue(f.Type, seen) {
					return true
				}
			}
		}
		return false
	}
	for _, h := range r.p.Unions {
		if r.out[h.Name].Form != FormA {
			continue
		}
		for _, v := range h.Variants {
			for _, f := range v.Fields {
				if byValue(f.Type, map[string]bool{}) {
					return true
				}
			}
		}
	}
	return false
}

// slotsOf assigns form D slots: each field takes the first slot of its Go
// type that its variant does not use yet, else a new slot.
func slotsOf(u Union) ([]Type, [][]int) {
	var slots []Type
	fieldSlot := make([][]int, len(u.Variants))
	for vi, v := range u.Variants {
		used := map[int]bool{}
		for _, f := range v.Fields {
			s := -1
			for i, t := range slots {
				if !used[i] && typeKey(t) == typeKey(f.Type) {
					s = i
					break
				}
			}
			if s < 0 {
				slots = append(slots, f.Type)
				s = len(slots) - 1
			}
			used[s] = true
			fieldSlot[vi] = append(fieldSlot[vi], s)
		}
	}
	return slots, fieldSlot
}

func typeKey(t Type) string {
	switch t.Kind {
	case KSlice:
		return "[]" + typeKey(*t.Elem)
	case KRecord:
		return "r:" + t.Name
	case KUnion:
		return "u:" + t.Name
	}
	return [...]string{KInt: "int64", KString: "string", KBool: "bool"}[t.Kind]
}

// layout is a Go type's size, alignment and CanSSA (gc, amd64).
type layout struct {
	size, align int64
	ssa         bool
}

func (r *ruler) typeLayout(t Type) layout {
	switch t.Kind {
	case KInt:
		return layout{8, 8, true}
	case KString:
		return layout{16, 8, true}
	case KBool:
		return layout{1, 1, true}
	case KSlice:
		return layout{24, 8, true}
	case KRecord:
		var ls []layout
		for _, f := range r.records[t.Name].Fields {
			ls = append(ls, r.typeLayout(f.Type))
		}
		return structLayout(ls)
	case KUnion:
		u := r.unions[t.Name]
		switch f := r.out[t.Name].Form; f {
		case FormB:
			return layout{1, 1, true}
		case FormC, FormD:
			if r.visiting[t.Name] {
				// A C or D union inside itself has no Go size (only the
				// round-1 rule reaches this; RuleG made it A at step 2).
				return layout{1 << 40, 8, false}
			}
			r.visiting[t.Name] = true
			defer delete(r.visiting, t.Name)
			return r.unionLayout(*u, f)
		}
		return layout{16, 8, true} // A: an interface
	}
	panic("unknown kind")
}

func (r *ruler) unionLayout(u Union, f Form) layout {
	if f == FormC {
		var ls []layout
		for _, fl := range payload(u).Fields {
			ls = append(ls, r.typeLayout(fl.Type))
		}
		return structLayout([]layout{structLayout(ls), {1, 1, true}})
	}
	slots, _ := slotsOf(u)
	ls := []layout{{1, 1, true}}
	for _, t := range slots {
		ls = append(ls, r.typeLayout(t))
	}
	return structLayout(ls)
}

// structLayout is cmd/compile's struct layout: fields in order, each at
// its alignment; a struct whose last field has size 0 gets one byte of
// padding (so a pointer to it stays inside the object); the size rounds
// up to the alignment. CanSSA: at most 32 bytes, at most 4 fields, every
// field CanSSA.
func structLayout(fields []layout) layout {
	off, align, ssa := int64(0), int64(1), len(fields) <= ssaFields
	for _, f := range fields {
		off = (off + f.align - 1) / f.align * f.align
		off += f.size
		align = max(align, f.align)
		ssa = ssa && f.ssa
	}
	if n := len(fields); n > 0 && fields[n-1].size == 0 && off > 0 {
		off++
	}
	size := (off + align - 1) / align * align
	return layout{size, align, ssa && size <= ssaBytes}
}

// FormCounts is the number of unions per form and step, for reports.
func FormCounts(c map[string]Choice) string {
	n := map[string]int{}
	for _, ch := range c {
		n[ch.Form.String()+" ("+string(ch.Why)+")"]++
	}
	var keys []string
	for k := range n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(strconv.Itoa(n[k]))
		b.WriteString("\n")
	}
	return b.String()
}

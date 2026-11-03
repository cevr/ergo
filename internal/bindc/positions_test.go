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

// This file holds the properties of two parts of the classification that
// TestClassificationTotality does not reach: a length that templates of
// both length lists give (TestLengthUnits), and the pointer fields of
// structs (TestFieldTotality). Each computes its expectation without the
// classifier code. TestReviewConflictingUnits and TestReviewFieldTotality
// are the verifier's probes of 1b round 1, kept as regression tests.

// genContract is the contract of a header that gModelSrc starts.
func genContract() *Contract {
	return &Contract{Header: "gen.h",
		Go:       GoTarget{Package: "gen", ImportPath: "ergo.local/gen"},
		Prefixes: Prefixes{Function: "g_", Macro: "G_", Type: "g_"},
		Status:   StatusRule{Type: "g_status", MacroPrefix: "G_", Success: "G_OK", Exclude: []Skip{}},
		ABI:      ABIRule{Function: "g_abi", Macro: "G_ABI"},
		Handles:  []HandleRule{{Type: "g_h", Create: "g_create", Out: "out_h", Destroy: "g_destroy", Thread: "owner"}},
		Autofill: []AutofillRule{}, Exceptions: []Exception{}, Skips: []Skip{},
		Buffers: BufferRules{LengthNames: []string{"{name}_count"}, ByteLengthNames: []string{},
			CapacityNames: []string{"capacity"}, CountNames: []string{"out_count"}, OutPrefix: "out_"}}
}

// genBind parses gModelSrc and extra, and binds it with genContract after
// edit.
func genBind(extra string, edit func(*Contract)) (*Binding, string, error) {
	src := gModelSrc + extra
	m, err := Parse("gen.h", []byte(src))
	if err != nil {
		return nil, src, err
	}
	c := genContract()
	sum := sha256.Sum256([]byte(src))
	c.HeaderSHA256 = hex.EncodeToString(sum[:])
	if edit != nil {
		edit(c)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return nil, src, err
	}
	b, err := Bind(m, []byte(src), data)
	return b, src + "\ncontract " + string(data), err
}

func reviewBind(t *testing.T, extra string, edit func(*Contract)) *Binding {
	t.Helper()
	b, src, err := genBind(extra, edit)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return b
}

// TestReviewConflictingUnits: a length that `{name}_count` (elements) and
// the literal `data_count` (bytes) both give makes the position raw.
func TestReviewConflictingUnits(t *testing.T) {
	b := reviewBind(t, "g_status g_take(const uint32_t *data, uint32_t data_count);\n", func(c *Contract) {
		c.Buffers.ByteLengthNames = []string{"data_count"}
	})
	f := b.Funcs[len(b.Funcs)-1]
	t.Logf("raw=%q positions=%+v", f.RawOnly, f.Positions)
	if f.RawOnly == "" {
		t.Fatal("same pointer/length matched element and byte units; binding must reject the contradiction or mark function raw")
	}
}

// TestReviewFieldTotality: the pointer field of an unused struct has a
// class and a reason in the report.
func TestReviewFieldTotality(t *testing.T) {
	b := reviewBind(t, "typedef struct g_unused { const uint8_t *mystery; uint32_t count; } g_unused;\n", nil)
	report := string(Report(b))
	t.Logf("report:\n%s", report)
	if !strings.Contains(report, "mystery") {
		t.Fatal("pointer field g_unused.mystery receives no class or raw reason")
	}
}

// uName makes a C name for the length-unit property: "v" and up to three
// characters of "as_", so that names often end in "s" and templates with
// {name} and {stem} often give one name.
func uName(r *rand.Rand) string {
	n := "v"
	for range r.IntN(4) {
		n += string("as_"[r.IntN(3)])
	}
	return n
}

// uGives reports whether template templ gives the length name l for the
// buffer name b. It reads the template as a pattern, a different reading
// from the generator's expansion: {name} is b, {stem} is b without one
// final "s" (a one-letter name keeps it), and a placeholder never matches
// an unnamed buffer.
func uGives(templ, b, l string) bool {
	if b == "" && strings.Contains(templ, "{") {
		return false
	}
	stem := b
	if s, ok := strings.CutSuffix(b, "s"); ok && s != "" {
		stem = s
	}
	re := regexp.QuoteMeta(templ)
	re = strings.ReplaceAll(re, regexp.QuoteMeta("{name}"), regexp.QuoteMeta(b))
	re = strings.ReplaceAll(re, regexp.QuoteMeta("{stem}"), regexp.QuoteMeta(stem))
	return regexp.MustCompile("^" + re + "$").MatchString(l)
}

// TestLengthUnits: for a generated buffer name, length name and templates
// (with {name}, with {stem} and literal) spread over length_names and
// byte_length_names, the buffer is an in_buffer exactly when templates of
// one list give the length, with that list's unit; when templates of both
// lists give it, the position is raw and the reason names both rules and
// both units. The order of the lists decides nothing.
func TestLengthUnits(t *testing.T) {
	stats := map[string]int{}
	defer func() { t.Logf("%d cases: %v", *propCases, stats) }()
	for n := 0; n < *propCases; n++ {
		seed := *propSeed + uint64(n)
		r := rand.New(rand.NewPCG(seed, 0x0b17e5))
		b := uName(r)
		if r.IntN(8) == 0 {
			b = ""
		}
		stem := strings.TrimSuffix(b, "s")
		if stem == "" {
			stem = b
		}
		var l string
		switch r.IntN(8) {
		case 0:
			l = b + "_count"
		case 1:
			l = stem + "_count"
		case 2:
			l = b + "_bytes"
		case 3:
			l = stem + "_bytes"
		case 4:
			l = b + "_len"
		case 5:
			l = []string{"count", "n", "size"}[r.IntN(3)]
		default:
			l = uName(r) + "_n"
		}
		if l == "" || l[0] == '_' {
			l = "n"
		}
		cands := []string{"{name}_count", "{stem}_count", "{name}_bytes", "{stem}_bytes", "{name}_len", "{stem}_len",
			"{stem}s_count", "count", "n", "size", l, uName(r) + "_n"}
		var elems, bytesList []string
		for _, c := range cands {
			if slices.Contains(elems, c) || slices.Contains(bytesList, c) {
				continue
			}
			switch r.IntN(10) {
			case 0, 1, 2, 3:
				elems = append(elems, c)
			case 4, 5, 6:
				bytesList = append(bytesList, c)
			}
		}
		if r.IntN(2) == 0 { // the order of a list decides nothing
			slices.Reverse(elems)
			slices.Reverse(bytesList)
		}
		if elems == nil {
			elems = []string{}
		}
		if bytesList == nil {
			bytesList = []string{}
		}
		elem := []string{"uint8_t", "uint32_t", "g_s0", "double"}[r.IntN(4)]
		lenType := []string{"uint32_t", "uint64_t", "g_u", "int32_t"}[r.IntN(4)]
		decl := "const " + elem + " *" + b
		proto := fmt.Sprintf("g_status g_take(%s, %s %s);\n", decl, lenType, l)

		unsigned := lenType != "int32_t"
		inElems, inBytes := false, false
		for _, c := range elems {
			inElems = inElems || unsigned && uGives(c, b, l)
		}
		for _, c := range bytesList {
			inBytes = inBytes || unsigned && uGives(c, b, l)
		}

		bd, src, err := genBind(proto, func(c *Contract) {
			c.Buffers.LengthNames, c.Buffers.ByteLengthNames = elems, bytesList
		})
		if err != nil {
			t.Fatalf("seed %d: Bind: %v\n%s", seed, err, src)
		}
		f := bd.Funcs[len(bd.Funcs)-1]
		q := f.Positions[0]
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d: elements %v, bytes %v: %s\nraw-only %q, position %+v\n%s", seed, inElems, inBytes,
				fmt.Sprintf(format, args...), f.RawOnly, q, src)
		}
		switch {
		case inElems && inBytes:
			stats["both"]++
			if q.Class != ClassRaw || f.RawOnly == "" {
				fail("a length with two units must make the position raw")
			}
			for _, s := range []string{"in_buffer (" + l + " counts elements)", "in_buffer_bytes (" + l + " counts bytes)", "both cover it"} {
				if !strings.Contains(f.RawOnly, s) {
					fail("the reason does not say %q", s)
				}
			}
		case inElems || inBytes:
			if inBytes {
				stats["bytes"]++
			} else {
				stats["elements"]++
			}
			if q.Class != ClassInBuffer || q.Length != 1 || q.Bytes != inBytes || f.RawOnly != "" {
				fail("want a typed in_buffer with bytes %v", inBytes)
			}
		default:
			stats["none"]++
			if q.Class == ClassInBuffer {
				fail("no template gives the length, but the position is an in_buffer")
			}
		}
	}
	if !testingShort() {
		for _, k := range []string{"both", "bytes", "elements", "none"} {
			if stats[k] < *propCases/20 {
				t.Errorf("the generator makes %d cases of %q in %d; want at least %d", stats[k], k, *propCases, *propCases/20)
			}
		}
	}
}

// fKind is a field type of the field property: its spelling with %s for
// the name, the kind of pointer position it is ("" for none), and whether
// a struct with the field holds a pointer.
type fKind struct {
	spell, what string
	holds       bool
}

var fKinds = []fKind{
	{"uint32_t %s", "", false},
	{"float %s", "", false},
	{"g_u %s", "", false},
	{"uint8_t %s[3]", "", false},
	{"g_s0 %s", "", false},
	{"g_s0 %s[2]", "", false},
	{"g_s1 %s", "", true}, // a nested struct with a pointer is not a position itself
	{"g_s1 %s[2]", "", true},
	{"g_s2 %s", "", true},
	{"const uint8_t *%s", "pointer field", true},
	{"void *%s", "pointer field", true},
	{"g_h *%s", "pointer field", true},
	{"const g_s0 *%s", "pointer field", true},
	{"uint8_t **%s", "pointer field", true},
	{"g_cb %s", "callback field", true},
	{"const uint8_t *%s[2]", "array of pointers", true},
	{"g_h *%s[4]", "array of pointers", true},
	{"g_cb %s[2]", "array of callbacks", true},
}

// wantField is one pointer field as the generator wrote it.
type wantField struct {
	strct, name, what string
	index             int
}

// TestFieldTotality: for generated structs (used by a function or not,
// with nested structs, pointers, arrays of pointers and callbacks), the
// binding lists exactly the pointer fields the generator wrote, each once,
// raw with the reason of its kind; the report names each and counts them
// with the function positions; and a function that takes a struct that
// holds a pointer, by `const *` or by value, is raw-only.
func TestFieldTotality(t *testing.T) {
	stats := map[string]int{}
	defer func() { t.Logf("%d cases: %v", *propCases, stats) }()
	for n := 0; n < *propCases; n++ {
		seed := *propSeed + uint64(n)
		r := rand.New(rand.NewPCG(seed, 0xf1e1d5))
		// gModelSrc has one pointer field: g_s1.p.
		want := []wantField{{"g_s1", "p", "pointer field", 0}}
		var src strings.Builder
		var holds []bool
		type use struct {
			fn, strct string
			holds     bool
			byValue   bool
			position  bool
		}
		var uses []use
		for s := range 1 + r.IntN(5) {
			name := fmt.Sprintf("g_t%d", s)
			kinds := slices.Clone(fKinds)
			for j := range s { // the structs before this one, by value and in arrays
				kinds = append(kinds, fKind{fmt.Sprintf("g_t%d %%s", j), "", holds[j]},
					fKind{fmt.Sprintf("g_t%d %%s[2]", j), "", holds[j]})
			}
			fmt.Fprintf(&src, "typedef struct %s {", name)
			h, index := false, 0
			for range 1 + r.IntN(5) {
				if r.IntN(6) == 0 { // one declaration, three declarators
					fmt.Fprintf(&src, " uint8_t f%d, *f%d, *f%d[2];", index, index+1, index+2)
					want = append(want, wantField{name, fmt.Sprintf("f%d", index+1), "pointer field", index + 1},
						wantField{name, fmt.Sprintf("f%d", index+2), "array of pointers", index + 2})
					index += 3
					h = true
					continue
				}
				k := kinds[r.IntN(len(kinds))]
				fmt.Fprintf(&src, " "+k.spell+";", fmt.Sprintf("f%d", index))
				if k.what != "" {
					want = append(want, wantField{name, fmt.Sprintf("f%d", index), k.what, index})
				}
				h = h || k.holds
				index++
			}
			fmt.Fprintf(&src, " } %s;\n", name)
			holds = append(holds, h)
			if r.IntN(2) == 0 {
				fmt.Fprintf(&src, "g_status g_use_t%d(const %s *v);\n", s, name)
				uses = append(uses, use{fmt.Sprintf("g_use_t%d", s), name, h, false, true})
			}
			if r.IntN(3) == 0 {
				fmt.Fprintf(&src, "g_status g_put_t%d(%s v);\n", s, name)
				uses = append(uses, use{fmt.Sprintf("g_put_t%d", s), name, h, true, false})
			}
		}
		b, all, err := genBind(src.String(), nil)
		if err != nil {
			t.Fatalf("seed %d: Bind: %v\n%s", seed, err, all)
		}
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d: %s\n%s", seed, fmt.Sprintf(format, args...), all)
		}
		if len(b.Fields) != len(want) {
			fail("%d pointer fields, the generator wrote %d: %+v", len(b.Fields), len(want), b.Fields)
		}
		report := string(Report(b))
		for i, w := range want {
			q := b.Fields[i]
			if q.Struct != w.strct || q.Name != w.name || q.Field != w.index {
				fail("pointer field %d is %s.%s (#%d), want %s.%s (#%d)", i, q.Struct, q.Name, q.Field, w.strct, w.name, w.index)
			}
			if q.Class != ClassRaw || !strings.HasPrefix(q.Reason, w.what+" (") {
				fail("%s.%s: class %s, reason %q; want raw, %s", w.strct, w.name, q.Class, q.Reason, w.what)
			}
			if line := fmt.Sprintf("\n  %s.%s: raw: %s (", w.strct, w.name, w.what); !strings.Contains(report, line) {
				fail("the report has no line %q:\n%s", line, report)
			}
			stats[w.what]++
		}
		// Function positions: g_create 2, g_destroy 1, each g_use 1.
		funcPos, raw := 3, len(want)
		for _, u := range uses {
			if u.position {
				funcPos++
				if u.holds {
					raw++
				}
			}
		}
		counts := fmt.Sprintf("pointer positions %d (parameters and results %d, fields %d):", funcPos+len(want), funcPos, len(want))
		if !strings.Contains(report, fmt.Sprintf("\npointer fields (%d):\n", len(want))) ||
			!strings.Contains(report, counts) || !strings.Contains(report, fmt.Sprintf(", raw %d\n", raw)) {
			fail("the report does not count %d fields, %q and raw %d:\n%s", len(want), counts, raw, report)
		}
		for _, u := range uses {
			i := slices.IndexFunc(b.Funcs, func(f *BoundFunc) bool { return f.C.Name == u.fn })
			if i < 0 {
				fail("%s is not bound", u.fn)
			}
			f := b.Funcs[i]
			switch {
			case !u.holds && f.RawOnly != "":
				fail("%s takes a struct without pointers, but it is raw-only: %s", u.fn, f.RawOnly)
			case u.holds && !u.byValue && !strings.Contains(f.RawOnly, "holds a pointer"):
				fail("%s takes `const S *` of a struct that holds a pointer, but raw-only is %q", u.fn, f.RawOnly)
			case u.holds && u.byValue && !strings.Contains(f.RawOnly, "a struct by value that holds a pointer"):
				fail("%s takes a struct that holds a pointer by value, but raw-only is %q", u.fn, f.RawOnly)
			}
			if u.holds {
				stats["raw-only use"]++
			} else {
				stats["typed use"]++
			}
		}
		used := map[string]bool{}
		for _, u := range uses {
			used[u.strct] = true
		}
		for _, w := range want[1:] {
			if !used[w.strct] {
				stats["unused struct field"]++
			}
		}
	}
	if !testingShort() {
		for _, k := range []string{"pointer field", "callback field", "array of pointers", "array of callbacks",
			"raw-only use", "typed use", "unused struct field"} {
			if stats[k] < *propCases/20 {
				t.Errorf("the generator makes %d of %q in %d cases; want at least %d", stats[k], k, *propCases, *propCases/20)
			}
		}
	}
}

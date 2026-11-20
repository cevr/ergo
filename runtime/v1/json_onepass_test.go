package v1

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestJSONOnePassAgreesWithOpen holds Decode's one pass (JSONOpenChecked,
// JSONSettle) to the two phases it replaces (JSON-VALUE-DECODE): a decode
// walk over a checked Cursor, then JSONSettle, gives JSONOpen's Malformed,
// offset and reason, for a text that is not JSON, and for one that is the
// walk the Cursor JSONOpen made gives, read for read, with no break found
// (so a JSON text never takes the slow path). The texts are generated
// values (every escape, surrogates, raw and invalid UTF-8, numbers of
// every spelling, "_tag" members, white space) under one damage or none:
// a piece spliced in, a byte deleted, changed or the text cut short; the
// limits are drawn. The walk reads values by their kind, skips some,
// reads some as another kind, ends lists and objects early and looks for
// "_tag". A failing seed prints the text, both answers and the replay
// command.
//
// Mutants only it kills (testdata/mutants.json): the jvd-* rows.
func TestJSONOnePassAgreesWithOpen(t *testing.T) {
	n := 4000
	if testing.Short() {
		n = 1500
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_JSON_ONEPASS_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x6a7664))
		text := onePassText(r)
		maxBytes, maxDepth := 0, 0
		if r.IntN(4) == 0 {
			maxBytes = r.IntN(len(text) + 2)
		}
		if r.IntN(2) == 0 {
			maxDepth = 1 + r.IntN(5)
		}
		walk := r.Uint64()
		want := onePassReference(text, maxBytes, maxDepth, walk)
		got, bad := onePassChecked(text, maxBytes, maxDepth, walk)
		if got != want {
			t.Fatalf("seed %d: text %q (MaxBytes %d, MaxDepth %d)\none pass:   %s\ntwo phases: %s\nreplay: ERGO_JSON_ONEPASS_SEED=%d go test ./runtime/v1 -run TestJSONOnePassAgreesWithOpen", seed, text, maxBytes, maxDepth, got, want, seed)
		}
		if bad && !strings.HasPrefix(want, "malformed") {
			t.Fatalf("seed %d: text %q is JSON, but the checked Cursor found a break (the slow path)\nreplay: ERGO_JSON_ONEPASS_SEED=%d go test ./runtime/v1 -run TestJSONOnePassAgreesWithOpen", seed, text, seed)
		}
	}
}

// TestJSONIsNumberIsOpensNumber holds ParseNum's fast path (JVD-DIRECT) to
// the path it skips: JSONIsNumber accepts a text exactly when JSONOpen
// accepts it, its one value is a number, and no white space is around it.
// The texts are numbers of every spelling, cut, damaged or padded, and
// pieces (mostly not numbers). A failing seed prints the text and the
// replay command.
//
// Mutants only it kills (testdata/mutants.json): the jvd-isnumber-* rows.
func TestJSONIsNumberIsOpensNumber(t *testing.T) {
	n := 20000
	first := uint64(0)
	if s := os.Getenv("ERGO_JSON_ISNUMBER_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	alphabet := []string{"-", "+", "0", "1", "9", ".", "e", "E", " ", "\n", "x", "00", "12", ".5", "e+", "e-"}
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x6e756d))
		var b strings.Builder
		k := r.IntN(7)
		for i := 0; i < k; i++ {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		text := b.String()
		c, err := JSONOpen(text, 0, 0)
		want := err == nil && c.Kind() == JSONKindNumber && strings.TrimSpace(text) == text
		if got := JSONIsNumber(text); got != want {
			t.Fatalf("seed %d: JSONIsNumber(%q) = %v, JSONOpen says %v\nreplay: ERGO_JSON_ISNUMBER_SEED=%d go test ./runtime/v1 -run TestJSONIsNumberIsOpensNumber", seed, text, got, want, seed)
		}
	}
}

// onePassReference is the two phases: JSONOpen's verdict, then the walk.
func onePassReference(text string, maxBytes, maxDepth int, walk uint64) string {
	c, err := JSONOpen(text, maxBytes, maxDepth)
	if err != nil {
		return onePassMalformed(err)
	}
	return onePassWalk(c, walk)
}

// onePassChecked is the one pass: the walk over a checked Cursor, then
// JSONSettle, whose Malformed outranks the walk's outcome. bad is the
// Cursor's break flag after the walk.
func onePassChecked(text string, maxBytes, maxDepth int, walk uint64) (out string, bad bool) {
	c, err := JSONOpenChecked(text, maxBytes, maxDepth)
	if err != nil {
		return onePassMalformed(err), false
	}
	defer func() {
		if p := recover(); p != nil {
			out = fmt.Sprintf("panic %v", p)
		}
	}()
	got := onePassWalk(c, walk)
	bad = c.bad
	if err := JSONSettle(c); err != nil {
		return onePassMalformed(err), bad
	}
	return got, bad
}

func onePassMalformed(err error) string {
	m := err.(JSONMalformed)
	return "malformed " + strconv.Itoa(m.Offset) + " " + m.Reason
}

// onePassWalk reads one value as a decode does, its choices drawn from
// walk, and logs each answer; a read of the wrong kind ends it, as a
// decode's failure does.
func onePassWalk(c *Cursor, walk uint64) string {
	w := onePassWalker{c: c, r: rand.New(rand.NewPCG(walk, 1)), budget: 20000}
	w.value()
	if w.stop == "" {
		w.stop = "ok"
	}
	return w.stop + " |" + w.log.String()
}

type onePassWalker struct {
	c      *Cursor
	r      *rand.Rand
	log    strings.Builder
	stop   string
	budget int
}

func (w *onePassWalker) say(s string) { w.log.WriteString(" " + s) }

func (w *onePassWalker) wrong(what string) {
	w.stop = "kind " + w.c.Path() + " " + what
}

func (w *onePassWalker) step() bool {
	w.budget--
	if w.budget < 0 {
		w.stop = "hung"
		return false
	}
	return w.stop == ""
}

func (w *onePassWalker) value() {
	if !w.step() {
		return
	}
	switch w.r.IntN(12) {
	case 0:
		w.c.Skip()
		w.say("skip")
		return
	case 1:
		w.other()
		return
	}
	c := w.c
	switch k := c.Kind(); k {
	case JSONKindNull:
		w.say("null " + strconv.FormatBool(c.TakeNull()))
	case JSONKindBool:
		v := c.Bool()
		if c.Wrong() {
			w.wrong("bool")
			return
		}
		w.say(strconv.FormatBool(v))
	case JSONKindNumber:
		v := c.Number()
		if c.Wrong() {
			w.wrong("number")
			return
		}
		w.say("#" + v)
	case JSONKindString:
		var v string
		if w.r.IntN(2) == 0 {
			v = string(c.StringBytes([]byte("pre:")))
		} else {
			v = c.String()
		}
		if c.Wrong() {
			w.wrong("string")
			return
		}
		w.say(strconv.Quote(v))
	case JSONKindList:
		if !c.BeginList() {
			w.wrong("list")
			return
		}
		w.say("[")
		w.items(false)
	case JSONKindObject:
		if !c.BeginObject() {
			w.wrong("object")
			return
		}
		w.say("{")
		if w.r.IntN(3) == 0 {
			tag := c.Tag()
			w.say("tag " + strconv.Quote(tag) + " " + strconv.FormatBool(c.Wrong()))
		}
		w.items(true)
	default:
		w.say("end " + strconv.Itoa(k))
		w.wrong("value")
	}
}

// items reads the elements of the list or object just begun, ending it
// early now and then, as a decode that has what it needs does.
func (w *onePassWalker) items(object bool) {
	c := w.c
	early := -1
	if w.r.IntN(4) == 0 {
		early = w.r.IntN(3)
	}
	// A decode ends early before More, never after it: once More has
	// reached an element, the decode reads or skips it (derived decode's
	// protocol). An End after More finds a break and takes the slow path.
	for i := 0; i != early && c.More(); i++ {
		if !w.step() {
			return
		}
		if object {
			w.say("key " + strconv.Quote(c.Key()))
		}
		w.value()
		if w.stop != "" {
			return
		}
	}
	c.End()
	w.say("end " + c.Path())
}

// other reads the next value as a kind drawn at random, as a decode of a
// record field of another type does.
func (w *onePassWalker) other() {
	c := w.c
	var v string
	switch w.r.IntN(6) {
	case 0:
		v = strconv.FormatInt(c.Int(), 10)
	case 1:
		v = strconv.FormatUint(c.Uint(), 10)
	case 2:
		v = strconv.FormatFloat(c.Float(), 'g', -1, 64)
	case 3:
		v = strconv.FormatBool(c.Bool())
	case 4:
		v = strconv.Quote(c.String())
	default:
		// An Option's decode: null, or else the value itself.
		took := c.TakeNull()
		w.say("null? " + strconv.FormatBool(took))
		if !took {
			w.value()
		}
		return
	}
	if c.Wrong() {
		w.wrong("other")
		return
	}
	w.say("as " + v)
}

// onePassPieces are what a damaged text is made of: every token, tokens
// cut short, escapes good and bad, bytes no value starts with.
var onePassPieces = []string{"{", "}", "[", "]", ",", ":", " ", "\n", `"a"`, `"_tag"`, `"\u00e9\n"`, `"\ud83d\ude00"`, `"\udcff\ud800"`, `"\x"`, `"\u12"`, `"\u12g4"`, "\"\x01\"", `"`, `\`, "1", "-", "0", "01", "0.", "1e", "1.5e+3", "-0.0E-2", "18446744073709551616", "tru", "true", "false", "null", "nul", "é", "\xff", `"NaN"`, `"Infinity"`}

func onePassText(r *rand.Rand) string {
	var b strings.Builder
	onePassSpace(r, &b)
	onePassValue(r, &b, 0)
	onePassSpace(r, &b)
	text := b.String()
	switch r.IntN(6) {
	case 0: // a piece spliced in
		k := r.IntN(len(text) + 1)
		text = text[:k] + onePassPieces[r.IntN(len(onePassPieces))] + text[k:]
	case 1: // a byte deleted
		if len(text) > 0 {
			k := r.IntN(len(text))
			text = text[:k] + text[k+1:]
		}
	case 2: // a byte changed
		if len(text) > 0 {
			k := r.IntN(len(text))
			p := onePassPieces[r.IntN(len(onePassPieces))]
			text = text[:k] + p[:1] + text[k+1:]
		}
	case 3: // cut short
		text = text[:r.IntN(len(text)+1)]
	}
	return text
}

func onePassSpace(r *rand.Rand, b *strings.Builder) {
	b.WriteString([]string{"", "", "", " ", "\n\t", " \r\n "}[r.IntN(6)])
}

func onePassValue(r *rand.Rand, b *strings.Builder, depth int) {
	n := 9
	if depth >= 4 {
		n = 7
	}
	switch r.IntN(n) {
	case 0:
		b.WriteString("null")
	case 1:
		b.WriteString([]string{"true", "false"}[r.IntN(2)])
	case 2, 3:
		b.WriteString([]string{"0", "-0", "7", "-12", "3.25", "-0.5e+3", "1E-2", "123456789012345678901234567890", "18446744073709551615", "9223372036854775808", "1.5"}[r.IntN(11)])
	case 4, 5, 6:
		b.WriteString(onePassString(r))
	case 7:
		b.WriteByte('[')
		k := r.IntN(4)
		for i := 0; i < k; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			onePassSpace(r, b)
			onePassValue(r, b, depth+1)
			onePassSpace(r, b)
		}
		b.WriteByte(']')
	default:
		b.WriteByte('{')
		k := r.IntN(4)
		for i := 0; i < k; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			onePassSpace(r, b)
			b.WriteString([]string{`"_tag"`, `"a"`, `"host"`, `"k\"ey"`, `"\u00e9"`}[r.IntN(5)])
			onePassSpace(r, b)
			b.WriteByte(':')
			onePassSpace(r, b)
			onePassValue(r, b, depth+1)
			onePassSpace(r, b)
		}
		b.WriteByte('}')
	}
}

func onePassString(r *rand.Rand) string {
	// "\x01" and "\t" are raw control bytes, which no JSON string holds.
	parts := []string{"a", "up", "é", "\xff", "\x01", "\t", `\n`, `\"`, `\\`, `\/`, `\b\f\r\t`, `\u00e9`, `\ud83d\ude00`, `\udcff`, `\ud800`, `\u0041`, " "}
	var b strings.Builder
	b.WriteByte('"')
	k := r.IntN(4)
	for i := 0; i < k; i++ {
		b.WriteString(parts[r.IntN(len(parts))])
	}
	b.WriteByte('"')
	return b.String()
}

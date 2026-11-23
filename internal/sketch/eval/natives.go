package eval

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Natives are the std functions with no Ergo body (`extern go`) that the
// evaluator runs with its own Go code, keyed by the std package path and
// the Ergo name. They are written from the std's spec text (each extern's
// doc comment: "strings.Split is Go's strings.Split", "Seeded(seed) is
// rand.NewPCG(seed, RandSeedHi)"), with Go's standard library, and never
// call runtime/v1: the evaluator is the reference the built program is
// held to, so it shares no body with it (ORACLE O1). TestNativesAgree
// holds each one to runtime/v1's on generated inputs. Any other extern,
// and every host extern, is skipped where it is called.
//
// A native pays a step per byte or element it reads and the bytes it
// builds, before it runs. A Go panic of the function (strings.Repeat of a
// negative count) is a defect with Go's text, as the built program
// panics.
type native func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt)

// nativeTables are the evaluator's natives: std's, by import path and
// name (std), Go's library functions a std body or a program reaches
// through import go (goLib), and the natives a template run passes a hole
// (holes, holeNative).
//
// reads is what each native of std and goLib reads at each argument
// position, by its key, set beside the native's registration: one word per
// position, space-separated, "" for none. A word is the evaluator's Go type
// of the value it takes there (string, int64, uint64, float64, bool, int32,
// uint8, sliceV, mapV, *record), func/N for a function it calls with N
// arguments, T for a value it only passes on or reads whatever its type,
// host:<pkg>.<Type> for a std host type's value (an actor, a listener),
// and mut:<word> for a borrow. TestEveryNativeReadsItsDeclaration holds it
// to the std declaration the native runs for, so a declaration that adds,
// drops or moves a parameter fails the fast tier, not a run (EVAL-RAWPATH:
// serve passed 8 arguments to a 9-parameter handler, send read a list
// where the declaration had come to put a string).
type nativeTables struct {
	std   map[string]native
	goLib map[string]native
	holes map[string]bool
	reads map[string]string
}

// loadedNatives is the only way to the tables: built by its first call,
// not at process start, since the registrations make hundreds of closures,
// every ergo command's init cost (TestStartBuildsNoTables). No package map
// holds them, so no reader can see them before they are built (a preflight
// read goNatives before any native ran, and skipped std math.Abs as a Go
// function; EVAL-NATIVE-LOAD). A test may set it to a fresh one. It is set
// in init, since buildNatives reaches the interpreter, which reads it.
var loadedNatives func() nativeTables

func init() { loadedNatives = sync.OnceValue(buildNatives) }

// buildNatives runs the registrations, in the order of their files, as Go
// ran them when each was an init (process.go wraps natives the earlier
// files made).
func buildNatives() nativeTables {
	t := nativeTables{std: map[string]native{}, goLib: goNatives(), holes: map[string]bool{}, reads: goNativeReads()}
	registerNatives(t)
	registerNativesbits(t)
	registerNativesconfig(t)
	registerNativescsv(t)
	registerNativesembed(t)
	registerNativesgo(t)
	registerNativeshmac(t)
	registerNativeshttp(t)
	registerNativesjson(t)
	registerNativesmore(t)
	registerNativesnet(t)
	registerNativespure(t)
	registerNativestime(t)
	registerNativestimeout(t)
	registerNativestoml(t)
	registerProcess(t)
	registerRunner(t)
	registerRunnerdurable(t)
	registerRunnerkids(t)
	registerTasks(t)
	return t
}

func registerNatives(t nativeTables) {
	natives := t.std
	goNatives := t.goLib
	str := func(v Value) string { return v.(string) }
	i64 := func(v Value) int { return int(v.(int64)) }
	strs := func(v Value) []string {
		s := v.(sliceV)
		out := make([]string, s.n)
		for i, e := range s.elems() {
			out[i] = e.(string)
		}
		return out
	}
	list := func(xs []string) Value {
		vs := make([]Value, len(xs))
		for i, x := range xs {
			vs[i] = x
		}
		return newSlice(vs)
	}
	// pure registers a function of its arguments: cost reads them, or
	// what Go's function reads of them when reads names it (readsOf).
	// goPure registers one of Go's functions by Go path and name, which a
	// std body or a program reaches through import go (goNatives).
	var pureIn func(into map[string]native, name string, f func(args []Value) Value)
	pure := func(name string, f func(args []Value) Value) { pureIn(natives, name, f) }
	goPure := func(name string, f func(args []Value) Value) { pureIn(goNatives, name, f) }
	pureIn = func(into map[string]native, name string, f func(args []Value) Value) {
		reads := readsOf()[name]
		into[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			work := argWork(args)
			if reads != nil {
				work = reads(args)
			}
			if ab := in.charge(fr, at, 1+work); ab != nil {
				return nil, ab
			}
			out, why := guard(func() Value { return f(args) })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if shares()[name] {
				return out, nil
			}
			if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
				return nil, ab
			}
			return out, nil
		}
	}
	t.reads["strings.Compare"] = "string string"
	goPure("strings.Compare", func(a []Value) Value { return int64(strings.Compare(str(a[0]), str(a[1]))) })
	t.reads["strings.Split"] = "string string"
	goPure("strings.Split", func(a []Value) Value { return list(strings.Split(str(a[0]), str(a[1]))) })
	t.reads["strings.Join"] = "sliceV string"
	pure("strings.Join", func(a []Value) Value { return strings.Join(strs(a[0]), str(a[1])) })
	t.reads["strings.HasPrefix"] = "string string"
	goPure("strings.HasPrefix", func(a []Value) Value { return strings.HasPrefix(str(a[0]), str(a[1])) })
	t.reads["strings.HasSuffix"] = "string string"
	goPure("strings.HasSuffix", func(a []Value) Value { return strings.HasSuffix(str(a[0]), str(a[1])) })
	t.reads["strings.TrimSpace"] = "string"
	goPure("strings.TrimSpace", func(a []Value) Value { return strings.TrimSpace(str(a[0])) })
	t.reads["strings.TrimPrefix"] = "string string"
	goPure("strings.TrimPrefix", func(a []Value) Value { return strings.TrimPrefix(str(a[0]), str(a[1])) })
	t.reads["strings.TrimSuffix"] = "string string"
	goPure("strings.TrimSuffix", func(a []Value) Value { return strings.TrimSuffix(str(a[0]), str(a[1])) })
	t.reads["strings.Fields"] = "string"
	goPure("strings.Fields", func(a []Value) Value { return list(strings.Fields(str(a[0]))) })
	t.reads["strings.Repeat"] = "string int64"
	goPure("strings.Repeat", func(a []Value) Value { return strings.Repeat(str(a[0]), i64(a[1])) })
	t.reads["strings.ToLower"] = "string"
	goPure("strings.ToLower", func(a []Value) Value { return strings.ToLower(str(a[0])) })
	t.reads["strings.ToUpper"] = "string"
	goPure("strings.ToUpper", func(a []Value) Value { return strings.ToUpper(str(a[0])) })
	t.reads["strings.Index"] = "string string"
	goPure("strings.Index", func(a []Value) Value { return int64(strings.Index(str(a[0]), str(a[1]))) })
	t.reads["strings.Contains"] = "string string"
	goPure("strings.Contains", func(a []Value) Value { return strings.Contains(str(a[0]), str(a[1])) })
	t.reads["strings.Slice"] = "string int64 int64"
	pure("strings.Slice", func(a []Value) Value { return str(a[0])[i64(a[1]):i64(a[2])] })
	t.reads["strings.ReplaceAll"] = "string string string"
	goPure("strings.ReplaceAll", func(a []Value) Value { return strings.ReplaceAll(str(a[0]), str(a[1]), str(a[2])) })
	t.reads["strings.LastIndex"] = "string string"
	goPure("strings.LastIndex", func(a []Value) Value { return int64(strings.LastIndex(str(a[0]), str(a[1]))) })
	t.reads["strings.Count"] = "string string"
	goPure("strings.Count", func(a []Value) Value { return int64(strings.Count(str(a[0]), str(a[1]))) })
	t.reads["strings.SplitN"] = "string string int64"
	goPure("strings.SplitN", func(a []Value) Value {
		// Go's nil for n == 0 is Ergo's empty slice: Ergo has no nil slice.
		return list(strings.SplitN(str(a[0]), str(a[1]), i64(a[2])))
	})
	t.reads["strings.EqualFold"] = "string string"
	goPure("strings.EqualFold", func(a []Value) Value { return strings.EqualFold(str(a[0]), str(a[1])) })
	t.reads["strings.ContainsRune"] = "string int32"
	goPure("strings.ContainsRune", func(a []Value) Value { return strings.ContainsRune(str(a[0]), rune(a[1].(int32))) })
	t.reads["strings.ToValidUTF8"] = "string string"
	goPure("strings.ToValidUTF8", func(a []Value) Value { return strings.ToValidUTF8(str(a[0]), str(a[1])) })

	t.reads["unicode/utf8.RuneCountInString"] = "string"
	goPure("unicode/utf8.RuneCountInString", func(a []Value) Value { return int64(utf8.RuneCountInString(str(a[0]))) })
	t.reads["utf8.RuneText"] = "int32"
	pure("utf8.RuneText", func(a []Value) Value { return string(rune(a[0].(int32))) })
	t.reads["utf8.AppendRune"] = "sliceV int32"
	pure("utf8.AppendRune", func(a []Value) Value {
		p := a[0].(sliceV)
		b := make([]byte, 0, p.n+utf8.UTFMax)
		for _, e := range p.elems() {
			b = append(b, e.(uint8))
		}
		b = utf8.AppendRune(b, rune(a[1].(int32)))
		vs := make([]Value, len(b))
		for i, c := range b {
			vs[i] = c
		}
		return newSlice(vs)
	})
	t.reads["unicode/utf8.ValidString"] = "string"
	goPure("unicode/utf8.ValidString", func(a []Value) Value { return utf8.ValidString(str(a[0])) })
	t.reads["unicode/utf8.ValidRune"] = "int32"
	goPure("unicode/utf8.ValidRune", func(a []Value) Value { return utf8.ValidRune(rune(a[0].(int32))) })
	t.reads["unicode/utf8.RuneLen"] = "int32"
	goPure("unicode/utf8.RuneLen", func(a []Value) Value { return int64(utf8.RuneLen(rune(a[0].(int32)))) })
	t.reads["utf8.DecodeRuneInString"] = "string"
	natives["utf8.DecodeRuneInString"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 2); ab != nil {
			return nil, ab
		}
		r, n := utf8.DecodeRuneInString(args[0].(string))
		return stdRecord(fn, "Decoded", map[string]Value{"Rune": int32(r), "Size": int64(n)}), nil
	}

	t.reads["strconv.Itoa"] = "int64"
	goPure("strconv.Itoa", func(a []Value) Value { return strconv.Itoa(i64(a[0])) })
	t.reads["strconv.FormatInt"] = "int64 int64"
	goPure("strconv.FormatInt", func(a []Value) Value { return strconv.FormatInt(a[0].(int64), i64(a[1])) })
	t.reads["strconv.FormatBool"] = "bool"
	goPure("strconv.FormatBool", func(a []Value) Value { return strconv.FormatBool(a[0].(bool)) })
	t.reads["strconv.FormatFloat"] = "float64 uint8 int64 int64"
	goPure("strconv.FormatFloat", func(a []Value) Value {
		return strconv.FormatFloat(a[0].(float64), a[1].(uint8), i64(a[2]), i64(a[3]))
	})
	t.reads["strconv.Quote"] = "string"
	goPure("strconv.Quote", func(a []Value) Value { return strconv.Quote(str(a[0])) })
	// The parses fail as their rows say: Go's ErrRange is OutOfRange, its
	// ErrSyntax Malformed; any other error (a bad base or bit size) is a
	// precondition of the call, a defect.
	parse := func(name string, f func(s string, a []Value) (Value, error)) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			s := args[0].(string)
			var v Value
			var err error
			_, why := guard(func() Value { v, err = f(s, args); return nil })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if err == nil {
				return v, nil
			}
			ne, ok := err.(*strconv.NumError)
			switch {
			case ok && ne.Err == strconv.ErrRange:
				return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "OutOfRange", map[string]Value{"Text": s}), "")
			case ok && ne.Err == strconv.ErrSyntax, name == "strconv.Unquote",
				name == "strconv.ParseFloat":
				return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "Malformed", map[string]Value{"Text": s}), "")
			}
			return nil, fr.defect(at, "%s: %v", name, err)
		}
	}
	t.reads["strconv.Atoi"] = "string"
	parse("strconv.Atoi", func(s string, _ []Value) (Value, error) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err
	})
	t.reads["strconv.ParseInt"] = "string int64 int64"
	parse("strconv.ParseInt", func(s string, a []Value) (Value, error) {
		n, err := strconv.ParseInt(s, i64(a[1]), i64(a[2]))
		return n, err
	})
	t.reads["strconv.ParseFloat"] = "string"
	parse("strconv.ParseFloat", func(s string, _ []Value) (Value, error) {
		f, err := strconv.ParseFloat(s, 64)
		return f, err
	})
	t.reads["strconv.Unquote"] = "string"
	parse("strconv.Unquote", func(s string, _ []Value) (Value, error) {
		u, err := strconv.Unquote(s)
		return u, err
	})

	t.reads["filepath.Join"] = "sliceV"
	pure("filepath.Join", func(a []Value) Value { return filepath.Join(strs(a[0])...) })
	t.reads["path/filepath.Base"] = "string"
	goPure("path/filepath.Base", func(a []Value) Value { return filepath.Base(str(a[0])) })
	t.reads["path/filepath.Dir"] = "string"
	goPure("path/filepath.Dir", func(a []Value) Value { return filepath.Dir(str(a[0])) })
	t.reads["filepath.Ext"] = "string"
	pure("filepath.Ext", func(a []Value) Value { return filepath.Ext(str(a[0])) })
	t.reads["path/filepath.Clean"] = "string"
	goPure("path/filepath.Clean", func(a []Value) Value { return filepath.Clean(str(a[0])) })
	t.reads["path/filepath.IsAbs"] = "string"
	goPure("path/filepath.IsAbs", func(a []Value) Value { return filepath.IsAbs(str(a[0])) })
	t.reads["hex.Encode"] = "string"
	pure("hex.Encode", func(a []Value) Value { return hex.EncodeToString([]byte(str(a[0]))) })
	t.reads["time.DurationText"] = "int64"
	pure("time.DurationText", func(a []Value) Value { return time.Duration(a[0].(int64)).String() })

	// The slices and maps functions copy: an Ergo collection is a value,
	// so a result never shares a store a caller can write (coll.go).
	elems := func(v Value) []Value { return v.(sliceV).elems() }
	t.reads["slices.Clone"] = "sliceV"
	pure("slices.Clone", func(a []Value) Value { return newSlice(append([]Value{}, elems(a[0])...)) })
	t.reads["slices.Concat"] = "sliceV sliceV"
	pure("slices.Concat", func(a []Value) Value {
		return newSlice(append(append([]Value{}, elems(a[0])...), elems(a[1])...))
	})
	t.reads["slices.Reverse"] = "sliceV"
	pure("slices.Reverse", func(a []Value) Value {
		xs := append([]Value{}, elems(a[0])...)
		for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
			xs[i], xs[j] = xs[j], xs[i]
		}
		return newSlice(xs)
	})
	t.reads["slices.Sort"] = "sliceV"
	pure("slices.Sort", func(a []Value) Value { return sortValues(elems(a[0])) })
	t.reads["slices.sortOwned"] = "sliceV"
	pure("slices.sortOwned", func(a []Value) Value { return sortValues(elems(a[0])) })
	// cmp.Max and Min are Go's builtin max and min: a float's NaN gives
	// NaN, and max(-0, +0) is +0, min(-0, +0) is -0 (EVAL-BUILDER).
	pure("cmp.Max", func(a []Value) Value { return pick(a[0], a[1], true) })
	pure("cmp.Min", func(a []Value) Value { return pick(a[0], a[1], false) })
	t.reads["slices.Slice"] = "sliceV int64 int64"
	natives["slices.Slice"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV)
		i, j := args[1].(int64), args[2].(int64)
		if ab := in.build(fr, at, 1+max(j-i, 0), max(j-i, 0), 0); ab != nil {
			return nil, ab
		}
		if j > int64(xs.n) && i >= 0 && i <= j {
			// xs[i:j:j] reads up to the Go target's capacity, which its
			// growth policy decides (expr.go sliceExpr).
			return nil, fr.unsupported(at, "a slice bound past the length, which reads the Go target's capacity")
		}
		if i < 0 || j < i || j > int64(xs.n) {
			return nil, fr.defect(at, "runtime error: slice bounds out of range [%d:%d] with length %d", i, j, xs.n)
		}
		return newSlice(append([]Value{}, xs.elems()[i:j]...)), nil
	}
	// bytes.grow is bs.Grow(n) (CollGrow; runtime/v1.StringsBuilderGrow):
	// the same bytes with room for n more, which the evaluator does not
	// hold apart from its length, so the value itself; a negative n is
	// Go's defect.
	t.reads["bytes.grow"] = "sliceV int64"
	natives["bytes.grow"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if n := args[1].(int64); n < 0 {
			return nil, fr.defect(at, "strings.Builder.Grow: negative count")
		}
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return args[0], nil
	}
	t.reads["slices.Take"] = "sliceV int64"
	natives["slices.Take"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs, i := args[0].(sliceV).elems(), args[1].(int64)
		if i < 0 || i >= int64(len(xs)) {
			return nil, fr.defect(at, "runtime error: index out of range [%d] with length %d", i, len(xs))
		}
		if ab := in.build(fr, at, int64(len(xs)), int64(len(xs)), 0); ab != nil {
			return nil, ab
		}
		rest := make([]Value, 0, len(xs)-1)
		rest = append(append(rest, xs[:i]...), xs[i+1:]...)
		return tuple{elems: []Value{xs[i], newSlice(rest)}}, nil
	}
	t.reads["slices.SortFunc"] = "sliceV func/2"
	natives["slices.SortFunc"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV).elems()
		if ab := in.build(fr, at, int64(len(xs)), int64(len(xs)), 0); ab != nil {
			return nil, ab
		}
		out := slices.Clone(xs)
		// Go's own sort, so the order it leaves equal elements in is the
		// order the built program's leaves them in; the first stop of a
		// comparison ends the sort.
		var stop *abrupt
		slices.SortFunc(out, func(a, b Value) int {
			if stop != nil {
				return 0
			}
			v, ab := in.callValue(fr, at, args[1], []Value{a, b})
			if ab != nil {
				stop = ab
				return 0
			}
			return cmp.Compare(v.(int64), 0)
		})
		if stop != nil {
			return nil, stop
		}
		return newSlice(out), nil
	}
	t.reads["time.ParseDuration"] = "string"
	natives["time.ParseDuration"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		s := args[0].(string)
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "ParseError", map[string]Value{"Layout": "", "Value": s, "Message": err.Error()}), "")
		}
		return int64(d), nil
	}
	t.reads["maps.Clone"] = "mapV"
	natives["maps.Clone"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		m, ab := in.ownMap(fr, at, args[0].(mapV), &token{})
		if ab != nil {
			return nil, ab
		}
		m.b.owner = nil
		return m, nil
	}

	// The task's context values (log, trace): the reading package is
	// the namespace, as a with of a value of its type binds it.
	for _, p := range []string{"log", "trace"} {
		t.reads[p+".contextBound"] = "string"
		t.reads[p+".contextValue"] = "string"
		natives[p+".contextBound"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			_, ok := in.contextLookup(fn.Pkg.Path, args[0].(string))
			return ok, nil
		}
		natives[p+".contextValue"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			// "" when no binding is in force (stdlib: the innermost
			// binding's text, or the empty text).
			text, _ := in.contextLookup(fn.Pkg.Path, args[0].(string))
			return text, nil
		}
	}
	t.reads["sync.NewRef"] = "T"
	natives["sync.NewRef"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 1, 0); ab != nil {
			return nil, ab
		}
		return &refV{v: args[0]}, nil
	}
	t.reads["sync.Modify"] = "host:sync.Ref func/1"
	natives["sync.Modify"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		r, ok := args[0].(*refV)
		if !ok {
			return nil, fr.unsupported(at, "a Ref that is not the std's own")
		}
		out, ab := in.callValue(fr, at, args[1], []Value{r.v})
		if ab != nil {
			return nil, ab
		}
		t := out.(tuple)
		r.v = t.elems[1]
		return t.elems[0], nil
	}
	t.reads["rand.newStream"] = "uint64"
	natives["rand.newStream"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.build(fr, at, 1, 4, 0); ab != nil {
			return nil, ab
		}
		return &streamV{r: rand.New(rand.NewPCG(args[0].(uint64), randSeedHi))}, nil
	}
}

// randSeedHi is the second word of every seeded PCG, the text "ErgoRand"
// (stdlib.md, rand: Seeded(seed) is rand.NewPCG(seed, "ErgoRand")).
const randSeedHi = 0x45_72_67_6f_52_61_6e_64

// collNatives are the natives of the operations of the built-in types
// (check.CollOpOf, PRIM-4), by operation, never by a declaration's name:
// each runs as the std function it was before it became an operation.
// The builder's appends run as std strings' Builder writes did before
// PRIM-4 made them operations (b25: without them every fold that writes
// through a Builder, std html's, was refused); TestEveryPureStdExternHasANative
// holds each pure extern std calls to a native, wherever it is declared.
var collNatives = sync.OnceValue(func() map[check.CollOp]string {
	return map[check.CollOp]string{
		check.CollTakeAt:       "slices.Take",
		check.CollSlice:        "slices.Slice",
		check.CollClone:        "maps.Clone",
		check.CollSliceClone:   "slices.Clone",
		check.CollConcat:       "slices.Concat",
		check.CollRepeat:       "slices.Repeat",
		check.CollSubstring:    "strings.Slice",
		check.CollText:         "strings.builderText",
		check.CollRuneText:     "utf8.RuneText",
		check.CollMakeSlice:    "slices.WithCapacity",
		check.CollBytes:        "strings.stringsBytes",
		check.CollGrow:         "bytes.grow",
		check.CollAppendString: "strings.builderAppend",
		check.CollAppendBytes:  "strings.builderAppendBytes",
		check.CollAppendFrom:   "strings.builderAppendFrom",
	}
})

// nativeOf is fn's native, nil when the evaluator has none.
func nativeOf(fn *check.Func) native {
	if fn.Pkg == nil || !fn.Pkg.Std() {
		return nil
	}
	t := loadedNatives()
	if key, ok := collNatives()[check.CollOpOf(fn)]; ok {
		return t.std[key]
	}
	return t.std[fn.Pkg.Path+"."+fn.Name]
}

// guard runs f and turns a Go panic into its text, as the built program's
// panic is a defect.
func guard(f func() Value) (v Value, why string) {
	defer func() {
		if r := recover(); r != nil {
			why = fmt.Sprint(r)
		}
	}()
	return f(), ""
}

// readsOf is the reading work of the pure natives that read less than
// their whole arguments in Go: a substring is a view of its text, and a
// prefix or suffix test reads the prefix's bytes. Charged as argWork, a
// parser that slices or tests the rest of its input at each token would
// pay its input's length per token: steps quadratic in the input where
// the Go build's work is linear (ORACLE O22).
var readsOf = sync.OnceValue(func() map[string]func(a []Value) int64 {
	return map[string]func(a []Value) int64{
		"strings.Slice":      func(a []Value) int64 { return 0 },
		"strings.HasPrefix":  func(a []Value) int64 { return int64(len(a[1].(string))) },
		"strings.HasSuffix":  func(a []Value) int64 { return int64(len(a[1].(string))) },
		"strings.TrimPrefix": func(a []Value) int64 { return int64(len(a[1].(string))) },
		"strings.TrimSuffix": func(a []Value) int64 { return int64(len(a[1].(string))) },
		// IndexByte reads up to the byte it finds (std io's lines: one call
		// per line on the rest of the text).
		"strings.IndexByte": func(a []Value) int64 {
			if i := strings.IndexByte(a[0].(string), a[1].(uint8)); i >= 0 {
				return int64(i) + 1
			}
			return int64(len(a[0].(string)))
		},
	}
})

// shares names the pure natives whose result is a view of an argument's
// bytes (Go's substring), which allocates no value bytes.
var shares = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"strings.Slice": true, "strings.TrimPrefix": true, "strings.TrimSuffix": true}
})

// argWork is the reading work of a native's arguments: a step per byte of
// each text and per element of each slice.
func argWork(args []Value) int64 {
	var n int64
	for _, a := range args {
		switch a := a.(type) {
		case string:
			n += int64(len(a))
		case sliceV:
			n += int64(a.n)
			for _, e := range a.elems() {
				if s, ok := e.(string); ok {
					n += int64(len(s))
				}
			}
		}
	}
	return n
}

// valueSize is the bytes a native's result builds.
func valueSize(v Value) int64 {
	switch v := v.(type) {
	case string:
		return int64(len(v))
	case sliceV:
		n := int64(v.n) * valueBytes
		for _, e := range v.elems() {
			if s, ok := e.(string); ok {
				n += int64(len(s))
			}
		}
		return n
	}
	return 0
}

// sortValues is a sorted copy of xs, a slice of one ordered basic type,
// sorted by Go's own slices.Sort on that Go type, so equal keys (-0 and
// +0) and NaNs land where the built program puts them.
func sortValues(xs []Value) Value {
	if len(xs) == 0 {
		return newSlice([]Value{})
	}
	switch xs[0].(type) {
	case int8:
		return sortAs[int8](xs)
	case int16:
		return sortAs[int16](xs)
	case int32:
		return sortAs[int32](xs)
	case int64:
		return sortAs[int64](xs)
	case uint8:
		return sortAs[uint8](xs)
	case uint16:
		return sortAs[uint16](xs)
	case uint32:
		return sortAs[uint32](xs)
	case uint64:
		return sortAs[uint64](xs)
	case float32:
		return sortAs[float32](xs)
	case float64:
		return sortAs[float64](xs)
	case string:
		return sortAs[string](xs)
	}
	panic(fmt.Sprintf("a sort of %T", xs[0]))
}

// pick is Go's builtin max(a, b) when larger, else min(a, b), over the
// ordered value types.
func pick(a, b Value, larger bool) Value {
	switch a := a.(type) {
	case float64:
		return pickOf(a, b.(float64), larger)
	case float32:
		return pickOf(a, b.(float32), larger)
	case int64:
		return pickOf(a, b.(int64), larger)
	case int32:
		return pickOf(a, b.(int32), larger)
	case int16:
		return pickOf(a, b.(int16), larger)
	case int8:
		return pickOf(a, b.(int8), larger)
	case uint64:
		return pickOf(a, b.(uint64), larger)
	case uint32:
		return pickOf(a, b.(uint32), larger)
	case uint16:
		return pickOf(a, b.(uint16), larger)
	case uint8:
		return pickOf(a, b.(uint8), larger)
	case string:
		return pickOf(a, b.(string), larger)
	}
	ice("cmp.Max or Min of %T", a)
	return nil
}

func pickOf[T cmp.Ordered](a, b T, larger bool) Value {
	if larger {
		return max(a, b)
	}
	return min(a, b)
}

func sortAs[T cmp.Ordered](xs []Value) Value {
	ts := make([]T, len(xs))
	for i, x := range xs {
		ts[i] = x.(T)
	}
	slices.Sort(ts)
	out := make([]Value, len(ts))
	for i, t := range ts {
		out[i] = t
	}
	return newSlice(out)
}

// stdRecord is a value of the record type name of fn's std package.
func stdRecord(fn *check.Func, name string, fields map[string]Value) *record {
	for _, tn := range fn.Pkg.Types {
		if tn.Name != name {
			continue
		}
		n, ok := tn.Type.(*check.Named)
		if !ok || structOf(n) == nil {
			break
		}
		r := &record{t: n, fields: make([]Value, len(structOf(n).Fields))}
		for k, v := range fields {
			r.fields[fieldIndex(n, k)] = v
		}
		return r
	}
	ice("the std package %s has no record %s", fn.Pkg.Path, name)
	return nil
}

// opObject is a value of a sealed std operation type whose operations the
// evaluator runs natively: a Ref, a rand stream.
type opObject interface {
	opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt)
	describe() string
}

// refV is a sync.Ref: one cell every copy shares (sync.ergo: "every copy
// of it is the same cell"). The evaluator runs one task at a time, so an
// operation is atomic by construction.
type refV struct{ v Value }

func (r *refV) describe() string { return "a sync.Ref" }

func (r *refV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Get":
		return r.v, nil
	case "Set":
		r.v = args[0]
		return unit{}, nil
	case "Update":
		v, ab := in.callValue(fr, at, args[0], []Value{r.v})
		if ab != nil {
			return nil, ab
		}
		r.v = v
		return unit{}, nil
	}
	ice("the Ref operation %s", name)
	return nil, nil
}

// streamV is a rand.Stream: Go's rand.Rand over a PCG source.
type streamV struct{ r *rand.Rand }

func (s *streamV) describe() string { return "a rand.Stream" }

func (s *streamV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Uint64":
		return s.r.Uint64(), nil
	case "IntN":
		n := args[0].(int64)
		if n <= 0 {
			return nil, fr.defect(at, "invalid argument to IntN")
		}
		return int64(s.r.IntN(int(n))), nil
	case "Float64":
		return s.r.Float64(), nil
	}
	ice("the rand.Stream operation %s", name)
	return nil, nil
}

package lower

import (
	"cmp"
	"math"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// stdGo3Cases are NP4f-3's moved functions, each against Go's own: the
// filepath functions that bind Go's path/filepath, and the Ergo bodies of
// cmp.Compare and Less, slices' Contains, Index, Equal, Max, Min and
// BinarySearch, and strings.PadLeft and PadRight. Edges are the empty
// path and runs of separators and dots; NaN, ±0 and ±Inf among floats;
// empty and one-element slices; widths at, under and over the rune count,
// invalid bytes and a pad that is not a valid rune.
func stdGo3Cases(r *rand.Rand, n int) []bodyCase {
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo3Decls
		return c
	}
	text := np4fErgoString
	fpImp := []string{"filepath", "strings", "strconv"}
	paths := []string{"", ".", "..", "/", "//", "a", "a/", "/a/b/../c", "a//b/./c/", "../../x", ".d", "b.c.", "/x.y/z", "é/\xff.t"}
	genPath := func(r *rand.Rand) string {
		segs := []string{"", ".", "..", "a", "b.c", ".d", "e.", "é", "\xff"}
		var b strings.Builder
		for range r.IntN(6) {
			b.WriteString(segs[r.IntN(len(segs))])
			b.WriteString([]string{"/", "", "//"}[r.IntN(3)])
		}
		return b.String()
	}
	fp := func(name, show string, ref func(string) string) bodyCase {
		return with(bodyRows("filepath."+name, fpImp, "s string", show, paths, n, r, genPath, text, ref))
	}
	cases := []bodyCase{
		fp("Base", `bs(filepath.Base(s))`, func(s string) string { return stdGo2Bytes(filepath.Base(s)) }),
		fp("Dir", `bs(filepath.Dir(s))`, func(s string) string { return stdGo2Bytes(filepath.Dir(s)) }),
		fp("Clean", `bs(filepath.Clean(s))`, func(s string) string { return stdGo2Bytes(filepath.Clean(s)) }),
		fp("IsAbs", `"\(filepath.IsAbs(s))"`, func(s string) string { return strconv.FormatBool(filepath.IsAbs(s)) }),
		fp("Split", `bs(filepath.Split(s).Dir) + "|" + bs(filepath.Split(s).File)`, func(s string) string {
			d, f := filepath.Split(s)
			return stdGo2Bytes(d) + "|" + stdGo2Bytes(f)
		}),
	}
	cases = append(cases, stdGo3CmpCases(r, n, with)...)
	cases = append(cases, stdGo3SliceCases(r, n, with)...)
	type pad struct {
		s     string
		width int
		pad   rune
	}
	padEdges := []pad{{"", 0, ' '}, {"", 3, '*'}, {"ab", 2, ' '}, {"ab", 1, ' '}, {"ab", -1, ' '}, {"é世", 4, 'é'}, {"\xff\xe4\xb8", 4, '-'}, {"x", 3, -1}, {"x", 3, 0xd800}, {"x", 3, 0x110000}}
	genPad := func(r *rand.Rand) pad { return pad{stdGo2Text(r), r.IntN(12) - 2, np4fGenRune(r)} }
	padArg := func(v pad) string { return text(v.s) + ", " + strconv.Itoa(v.width) + ", " + strconv.Itoa(int(v.pad)) }
	// goPad is the pad Go's fmt width gives ("%*s"), with any rune for the
	// space: string(r) of a rune that is not valid is U+FFFD.
	goPad := func(v pad, left bool) string {
		k := v.width - utf8.RuneCountInString(v.s)
		if k <= 0 {
			return stdGo2Bytes(v.s)
		}
		fill := strings.Repeat(string(v.pad), k)
		if left {
			return stdGo2Bytes(fill + v.s)
		}
		return stdGo2Bytes(v.s + fill)
	}
	padImp := []string{"strings", "strconv"}
	cases = append(cases,
		with(bodyRows("strings.PadLeft", padImp, "s string, w int, p rune", `bs(strings.PadLeft(s, w, p))`,
			padEdges, n, r, genPad, padArg, func(v pad) string { return goPad(v, true) })),
		with(bodyRows("strings.PadRight", padImp, "s string, w int, p rune", `bs(strings.PadRight(s, w, p))`,
			padEdges, n, r, genPad, padArg, func(v pad) string { return goPad(v, false) })),
	)
	return cases
}

// stdGo3Decls are stdGo2Decls with the Option printers of slices' Max, Min
// and BinarySearch.
const stdGo3Decls = stdGo2Decls + `

func oi(o Option[int]) string {
	match o {
	case Some[int] as v:
		"\(v.Value)"
	case None:
		"none"
	}
}

func of(o Option[float64]) string {
	match o {
	case Some[float64] as v:
		strconv.FormatFloat(v.Value, 'x', -1)
	case None:
		"none"
	}
}`

// np4fFloats is xs as an Ergo []float64 literal of np4fFl calls.
func np4fFloats(xs []float64) string {
	parts := make([]string, len(xs))
	for i, f := range xs {
		parts[i] = np4fFloatArg(f)
	}
	return "[]float64{" + strings.Join(parts, ", ") + "}"
}

func np4fInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return "[]int{" + strings.Join(parts, ", ") + "}"
}

// stdGo3Floats are the float edges of an order: NaN, ±0, ±Inf and the
// extremes.
var stdGo3Floats = []float64{math.NaN(), math.Copysign(0, -1), 0, 1, -1, 0.5, math.Inf(1), math.Inf(-1), math.MaxFloat64, -math.SmallestNonzeroFloat64}

func stdGo3Float(r *rand.Rand) float64 {
	if r.IntN(3) == 0 {
		return stdGo3Floats[r.IntN(len(stdGo3Floats))]
	}
	return float64(r.IntN(9) - 4)
}

func stdGo3CmpCases(r *rand.Rand, n int, with func(bodyCase) bodyCase) []bodyCase {
	imp := []string{"cmp", "strings", "strconv"}
	type ints struct{ a, b int }
	type floats struct{ a, b float64 }
	type texts struct{ a, b string }
	intEdges := []ints{{0, 0}, {-1, 1}, {1, -1}, {math.MinInt, math.MaxInt}, {math.MaxInt, math.MaxInt}}
	genInts := func(r *rand.Rand) ints { return ints{r.IntN(5) - 2, int(r.Uint64()) >> r.UintN(64)} }
	intArg := func(v ints) string { return strconv.Itoa(v.a) + ", " + strconv.Itoa(v.b) }
	var floatEdges []floats
	for _, a := range stdGo3Floats {
		for _, b := range stdGo3Floats[:6] {
			floatEdges = append(floatEdges, floats{a, b})
		}
	}
	genFloats := func(r *rand.Rand) floats { return floats{stdGo3Float(r), stdGo3Float(r)} }
	floatArg := func(v floats) string { return np4fFloatArg(v.a) + ", " + np4fFloatArg(v.b) }
	textEdges := []texts{{"", ""}, {"", "a"}, {"a", ""}, {"ab", "a"}, {"\xff", "é"}, {"B", "a"}}
	genTexts := func(r *rand.Rand) texts { return texts{stdGo2Text(r), stdGo2Text(r)} }
	textArg := func(v texts) string { return np4fErgoString(v.a) + ", " + np4fErgoString(v.b) }
	d := strconv.Itoa
	b := strconv.FormatBool
	return []bodyCase{
		with(bodyRows("cmp.Compare", imp, "x int, y int", `"\(cmp.Compare(x, y))"`, intEdges, n, r, genInts, intArg,
			func(v ints) string { return d(cmp.Compare(v.a, v.b)) })),
		with(bodyRows("cmp.Compare", imp, "x float64, y float64", `"\(cmp.Compare(x, y))"`, floatEdges, n, r, genFloats, floatArg,
			func(v floats) string { return d(cmp.Compare(v.a, v.b)) })),
		with(bodyRows("cmp.Compare", imp, "x string, y string", `"\(cmp.Compare(x, y))"`, textEdges, n, r, genTexts, textArg,
			func(v texts) string { return d(cmp.Compare(v.a, v.b)) })),
		with(bodyRows("cmp.Less", imp, "x int, y int", `"\(cmp.Less(x, y))"`, intEdges, n, r, genInts, intArg,
			func(v ints) string { return b(cmp.Less(v.a, v.b)) })),
		with(bodyRows("cmp.Less", imp, "x float64, y float64", `"\(cmp.Less(x, y))"`, floatEdges, n, r, genFloats, floatArg,
			func(v floats) string { return b(cmp.Less(v.a, v.b)) })),
		with(bodyRows("cmp.Less", imp, "x string, y string", `"\(cmp.Less(x, y))"`, textEdges, n, r, genTexts, textArg,
			func(v texts) string { return b(cmp.Less(v.a, v.b)) })),
	}
}

func stdGo3SliceCases(r *rand.Rand, n int, with func(bodyCase) bodyCase) []bodyCase {
	imp := []string{"slices", "strings", "strconv"}
	type ix struct {
		xs []int
		x  int
	}
	type fx struct {
		xs []float64
		x  float64
	}
	genInts := func(r *rand.Rand) []int {
		xs := make([]int, r.IntN(7))
		for i := range xs {
			xs[i] = r.IntN(5)
		}
		return xs
	}
	genFloats := func(r *rand.Rand) []float64 {
		xs := make([]float64, r.IntN(6))
		for i := range xs {
			xs[i] = stdGo3Float(r)
		}
		return xs
	}
	ixEdges := []ix{{nil, 0}, {[]int{1}, 1}, {[]int{1}, 0}, {[]int{2, 2, 2}, 2}, {[]int{3, 1, 2, 1}, 1}, {[]int{0, 1, 2, 3, 4}, 4}}
	genIx := func(r *rand.Rand) ix { return ix{genInts(r), r.IntN(5)} }
	ixArg := func(v ix) string { return np4fInts(v.xs) + ", " + strconv.Itoa(v.x) }
	xsArg := func(v ix) string { return np4fInts(v.xs) }
	fxEdges := []fx{{nil, 0}, {[]float64{math.NaN()}, math.NaN()}, {[]float64{0, math.Copysign(0, -1)}, 0}, {[]float64{math.Copysign(0, -1), 0}, math.Copysign(0, -1)},
		{[]float64{1, math.NaN(), 2}, 1}, {[]float64{math.Inf(-1), math.Inf(1)}, math.Inf(1)}}
	genFx := func(r *rand.Rand) fx { return fx{genFloats(r), stdGo3Float(r)} }
	fxArg := func(v fx) string { return np4fFloats(v.xs) + ", " + np4fFloatArg(v.x) }
	fsArg := func(v fx) string { return np4fFloats(v.xs) }
	type pair struct{ a, b []int }
	pairEdges := []pair{{nil, nil}, {nil, []int{0}}, {[]int{1, 2}, []int{1, 2}}, {[]int{1, 2}, []int{2, 1}}, {[]int{1}, []int{1, 1}}}
	genPair := func(r *rand.Rand) pair {
		a := genInts(r)
		if r.IntN(2) == 0 {
			return pair{a, slices.Clone(a)}
		}
		return pair{a, genInts(r)}
	}
	pairArg := func(v pair) string { return np4fInts(v.a) + ", " + np4fInts(v.b) }
	opt := func(v int, ok bool) string {
		if !ok {
			return "none"
		}
		return strconv.Itoa(v)
	}
	optF := func(xs []float64, f func([]float64) float64) string {
		if len(xs) == 0 {
			return "none"
		}
		return np4fFloatText(f(xs))
	}
	search := func(xs []int, x int) string {
		s := slices.Clone(xs)
		slices.Sort(s)
		i, ok := slices.BinarySearch(s, x)
		return opt(i, ok)
	}
	searchF := func(xs []float64, x float64) string {
		s := slices.Clone(xs)
		slices.Sort(s)
		i, ok := slices.BinarySearch(s, x)
		return opt(i, ok)
	}
	b := strconv.FormatBool
	return []bodyCase{
		with(bodyRows("slices.Contains", imp, "xs []int, x int", `"\(slices.Contains(xs, x))"`, ixEdges, n, r, genIx, ixArg,
			func(v ix) string { return b(slices.Contains(v.xs, v.x)) })),
		with(bodyRows("slices.Contains", imp, "xs []float64, x float64", `"\(slices.Contains(xs, x))"`, fxEdges, n, r, genFx, fxArg,
			func(v fx) string { return b(slices.Contains(v.xs, v.x)) })),
		with(bodyRows("slices.Index", imp, "xs []int, x int", `"\(slices.Index(xs, x))"`, ixEdges, n, r, genIx, ixArg,
			func(v ix) string { return strconv.Itoa(slices.Index(v.xs, v.x)) })),
		with(bodyRows("slices.Index", imp, "xs []float64, x float64", `"\(slices.Index(xs, x))"`, fxEdges, n, r, genFx, fxArg,
			func(v fx) string { return strconv.Itoa(slices.Index(v.xs, v.x)) })),
		with(bodyRows("slices.Equal", imp, "a []int, c []int", `"\(slices.Equal(a, c))"`, pairEdges, n, r, genPair, pairArg,
			func(v pair) string { return b(slices.Equal(v.a, v.b)) })),
		with(bodyRows("slices.Max", imp, "xs []int", `oi(slices.Max(xs))`, ixEdges, n, r, genIx, xsArg,
			func(v ix) string {
				if len(v.xs) == 0 {
					return "none"
				}
				return strconv.Itoa(slices.Max(v.xs))
			})),
		with(bodyRows("slices.Max", imp, "xs []float64", `of(slices.Max(xs))`, fxEdges, n, r, genFx, fsArg,
			func(v fx) string { return optF(v.xs, slices.Max[[]float64]) })),
		with(bodyRows("slices.Min", imp, "xs []int", `oi(slices.Min(xs))`, ixEdges, n, r, genIx, xsArg,
			func(v ix) string {
				if len(v.xs) == 0 {
					return "none"
				}
				return strconv.Itoa(slices.Min(v.xs))
			})),
		with(bodyRows("slices.Min", imp, "xs []float64", `of(slices.Min(xs))`, fxEdges, n, r, genFx, fsArg,
			func(v fx) string { return optF(v.xs, slices.Min[[]float64]) })),
		with(bodyRows("slices.BinarySearch", imp, "xs []int, x int", `oi(slices.BinarySearch(slices.Ascend(xs), x))`, ixEdges, n, r, genIx, ixArg,
			func(v ix) string { return search(v.xs, v.x) })),
		with(bodyRows("slices.BinarySearch", imp, "xs []float64, x float64", `oi(slices.BinarySearch(slices.Ascend(xs), x))`, fxEdges, n, r, genFx, fxArg,
			func(v fx) string { return searchF(v.xs, v.x) })),
	}
}

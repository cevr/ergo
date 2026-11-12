package main

import (
	"fmt"
	"strconv"
	"strings"
)

// The inputs: 64 values of each kind, the same in both programs. Text
// kinds are literals the harness writes from a fixed generator; slices and
// 1 KiB blobs are built by the program before its loop, so their set-up
// cancels in the slope. Texts hold no backslash and no double quote, so one
// literal spelling serves Ergo and Go; a few hold two-byte runes.

const inputCount = 64

// lcg is the generator of every seeded input (the constants of examples/lru).
type lcg struct{ x int }

func (g *lcg) next(n int) int {
	g.x = (g.x*1103515245 + 12345) % 2147483648
	return (g.x / 65536) % n
}

var syllables = []string{"al", "be", "ka", "do", "re", "mi", "su", "to", "va", "ne", "lo", "pi", "zé", "rü"}

func word(g *lcg) string {
	var b strings.Builder
	for k := 0; k < 2+g.next(4); k++ {
		s := syllables[g.next(len(syllables))]
		if k == 0 && g.next(3) == 0 {
			s = strings.ToUpper(s)
		}
		b.WriteString(s)
	}
	if g.next(4) == 0 {
		b.WriteString(strconv.Itoa(g.next(100)))
	}
	return b.String()
}

// textInputs are the literal kinds, by name.
func textInputs() map[string][]string {
	g := &lcg{x: 7}
	kinds := map[string][]string{}
	for i := 0; i < inputCount; i++ {
		kinds["word"] = append(kinds["word"], word(g))
		pad := strings.Repeat(" ", g.next(3))
		sep := []string{" = ", "=", ": "}[g.next(3)]
		line := pad + "key" + strconv.Itoa(i) + sep + "value " + word(g) + " " + word(g) + pad
		if g.next(4) == 0 {
			line = pad + word(g) + " " + word(g) + " " + word(g) + pad
		}
		kinds["line"] = append(kinds["line"], line)
		var fields []string
		for k := 0; k < 3+g.next(5); k++ {
			fields = append(fields, word(g))
		}
		kinds["csv"] = append(kinds["csv"], strings.Join(fields, ","))
		kinds["intText"] = append(kinds["intText"], strconv.Itoa(g.next(2000000)-1000000))
		bad := []string{"12x4", "", "abc", "99999999999999999999", "-", "1_000", " 7", "0x1f"}
		kinds["badText"] = append(kinds["badText"], bad[g.next(len(bad))])
		kinds["floatText"] = append(kinds["floatText"], []string{
			strconv.Itoa(g.next(1000)) + "." + strconv.Itoa(g.next(1000)),
			"-" + strconv.Itoa(g.next(100)) + "e" + strconv.Itoa(g.next(20)),
			strconv.Itoa(g.next(100000)),
		}[g.next(3)])
		var path []string
		if g.next(2) == 0 {
			path = append(path, "")
		}
		for k := 0; k < 2+g.next(4); k++ {
			path = append(path, []string{word(g), "..", ".", "", word(g) + ".txt"}[g.next(5)])
		}
		kinds["path"] = append(kinds["path"], strings.Join(path, "/")+"/"+word(g))
	}
	return kinds
}

func intInputs() []int {
	g := &lcg{x: 11}
	var out []int
	for i := 0; i < inputCount; i++ {
		out = append(out, g.next(10000))
	}
	return out
}

func floatInputs() []float64 {
	g := &lcg{x: 13}
	var out []float64
	for i := 0; i < inputCount; i++ {
		out = append(out, float64(g.next(100000)-20000)/float64(1+g.next(64)))
	}
	return out
}

// inputKinds maps a kind to the name of the function both programs call
// to get its 64 values.
var inputKinds = map[string]string{
	"word": "words", "line": "lines", "csv": "csvs", "intText": "intTexts", "badText": "badTexts",
	"floatText": "floatTexts", "path": "paths", "int": "ints", "float": "floats", "intSlice": "intSlices", "blob": "blobs",
}

// ergoInputFunc and goInputFunc are the source of one kind's function.
func ergoInputFunc(kind string) string {
	name := inputKinds[kind]
	switch kind {
	case "int":
		return fmt.Sprintf("func %s() []int {\n\t[]int{%s}\n}\n", name, joinInts(intInputs()))
	case "float":
		return fmt.Sprintf("func %s() []float64 {\n\t[]float64{%s}\n}\n", name, joinFloats(floatInputs()))
	case "intSlice":
		return "func intSlices() [][]int {\n\tlet mut g = 17\n\tlet mut out = [][]int{}\n\tfor _ in 0..64 {\n\t\tlet mut xs = []int{}\n\t\tfor _ in 0..64 {\n\t\t\tg = (g*1103515245 + 12345) % 2147483648\n\t\t\txs = xs.Append((g / 65536) % 1000)\n\t\t}\n\t\tout = out.Append(xs)\n\t}\n\tout\n}\n"
	case "blob":
		return "func blobs() []string {\n\tlet mut out = []string{}\n\tfor l in lines() {\n\t\tout = out.Append(strings.Slice(strings.Repeat(l, 1024 / len(l) + 1), 0, 1024))\n\t}\n\tout\n}\n" + ergoInputFunc("line")
	}
	return fmt.Sprintf("func %s() []string {\n\t[]string{%s}\n}\n", name, quoteAll(textInputs()[kind]))
}

func goInputFunc(kind string) string {
	name := inputKinds[kind]
	switch kind {
	case "int":
		return fmt.Sprintf("func %s() []int {\n\treturn []int{%s}\n}\n", name, joinInts(intInputs()))
	case "float":
		return fmt.Sprintf("func %s() []float64 {\n\treturn []float64{%s}\n}\n", name, joinFloats(floatInputs()))
	case "intSlice":
		return "func intSlices() [][]int {\n\tg := 17\n\tvar out [][]int\n\tfor i := 0; i < 64; i++ {\n\t\tvar xs []int\n\t\tfor k := 0; k < 64; k++ {\n\t\t\tg = (g*1103515245 + 12345) % 2147483648\n\t\t\txs = append(xs, (g/65536)%1000)\n\t\t}\n\t\tout = append(out, xs)\n\t}\n\treturn out\n}\n"
	case "blob":
		return "func blobs() []string {\n\tvar out []string\n\tfor _, l := range lines() {\n\t\tout = append(out, strings.Repeat(l, 1024/len(l)+1)[:1024])\n\t}\n\treturn out\n}\n" + goInputFunc("line")
	}
	return fmt.Sprintf("func %s() []string {\n\treturn []string{%s}\n}\n", name, quoteAll(textInputs()[kind]))
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = `"` + x + `"`
	}
	return strings.Join(q, ", ")
}

func joinInts(xs []int) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Itoa(x)
	}
	return strings.Join(q, ", ")
}

func joinFloats(xs []float64) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		q[i] = s
	}
	return strings.Join(q, ", ")
}

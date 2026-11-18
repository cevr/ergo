// Command control is the hand Go control of TPROBE's property kind: the
// least Go that gives TestSelfhostFormatLaws's verdicts on the same subject
// (compiler/format, linked as internal/linked/compiler and called through
// hostsyntax). For each input the formatter formats, the facade's limits
// must keep the text (limit 0, a negative limit, the size, the size plus
// one) and refuse the size less one with a *hostsyntax.LimitError and no
// text, the input must parse, and compiler/api's Laws must answer "ok";
// an input it does not format fails with a located *syntax.Error and no
// text. Inputs: every examples/ and compiler/ .ergo file, the formatter's
// fuzz seeds, one seeded edit of each (invalid UTF-8 in a comment or a
// string, a truncation, a random byte), and 64 random byte strings; the
// seed is ERGO_SELFHOST_SEED (default 1). It prints one FAIL line per
// broken input, at most three (exit 1), or "ok N inputs, M formatted"
// (exit 0). Run it from the repository root.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/hostsyntax"
	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/syntax"
)

func main() {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SELFHOST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			fmt.Printf("FAIL ERGO_SELFHOST_SEED=%q is not a seed\n", s)
			os.Exit(1)
		}
		seed = n
	}
	inputs := sources(seed)
	failed, formatted := 0, 0
	fail := func(src, why string) {
		failed++
		if failed <= 3 {
			fmt.Printf("FAIL seed %d: %s\ninput: %q\n", seed, why, clip(src))
		}
	}
	ctx := context.Background()
	for i, src := range inputs {
		out, err := hostsyntax.SourceBounded(src, 0)
		base := outcome(out, err)
		if err != nil {
			var se *syntax.Error
			if !errors.As(err, &se) || out != "" {
				fail(src, "got "+base+", want a located syntax failure with no text")
			}
			continue
		}
		if _, perr := syntax.Parse(src); perr != nil {
			fail(src, "formats, but does not parse: "+outcome("", perr))
			continue
		}
		formatted++
		n := len(out)
		limits := map[int]string{-1 - i%3: base, n: base, n + 1: base}
		if n > 1 {
			limits[n-1] = fmt.Sprintf("limit %d\n", n-1)
		}
		for limit, want := range limits {
			got, gerr := hostsyntax.SourceBounded(src, limit)
			if o := outcome(got, gerr); o != want {
				fail(src, fmt.Sprintf("limit %d: got %q, want %q", limit, clip(o), clip(want)))
			}
		}
		v, err := linked.Laws(ctx, src)
		if err != nil {
			v = "ergo error " + err.Error() + "\n"
		}
		if v != "ok\n" {
			fail(src, "a law of the formatter fails:\n"+clip(v))
		}
	}
	if formatted == 0 {
		fail("", "no input formats")
	}
	if failed > 0 {
		fmt.Printf("FAIL %d of %d inputs\n", failed, len(inputs))
		os.Exit(1)
	}
	fmt.Printf("ok %d inputs, %d formatted\n", len(inputs), formatted)
}

// outcome is a formatter's answer as one text (selfhostcheck's).
func outcome(text string, err error) string {
	if err == nil {
		return "ok\n" + text
	}
	if text != "" {
		return "partial " + strconv.Itoa(len(text)) + " " + err.Error() + "\n"
	}
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Sprintf("syntax %s %d:%s %d %d %d %d\n", se.Code, len(se.Message), se.Message, se.Offset, se.Length, se.Line, se.Column)
	}
	var le *hostsyntax.LimitError
	if errors.As(err, &le) {
		return fmt.Sprintf("limit %d\n", le.Limit)
	}
	return "go error " + err.Error() + "\n"
}

// sources are the inputs of seed.
func sources(seed uint64) []string {
	var inputs []string
	for _, dir := range []string{"examples", "compiler"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || !strings.HasSuffix(path, ".ergo") {
				return err
			}
			src, err := os.ReadFile(path)
			inputs = append(inputs, string(src))
			return err
		})
		if err != nil {
			fmt.Printf("FAIL %v\n", err)
			os.Exit(1)
		}
	}
	seeds, err := os.ReadDir(filepath.Join("internal", "hostsyntax", "testdata", "fuzz", "FuzzFormat"))
	if err != nil {
		fmt.Printf("FAIL %v\n", err)
		os.Exit(1)
	}
	for _, e := range seeds {
		data, err := os.ReadFile(filepath.Join("internal", "hostsyntax", "testdata", "fuzz", "FuzzFormat", e.Name()))
		if err != nil {
			fmt.Printf("FAIL %v\n", err)
			os.Exit(1)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if q, ok := strings.CutPrefix(line, "string("); ok {
				if s, err := strconv.Unquote(strings.TrimSuffix(q, ")")); err == nil {
					inputs = append(inputs, s)
				}
			}
		}
	}
	r := rand.New(rand.NewPCG(seed, 0x5e1f))
	for _, src := range inputs[:len(inputs):len(inputs)] {
		inputs = append(inputs, edit(r, src))
	}
	for range 64 {
		b := make([]byte, r.IntN(48))
		for j := range b {
			b[j] = byte(r.IntN(256))
		}
		inputs = append(inputs, string(b))
	}
	return inputs
}

var invalid = []string{"\x80", "\xc3", "\xc0\xaf", "\xed\xa0\x80", "\xff"}

// edit is one seeded edit of src (selfhostcheck's).
func edit(r *rand.Rand, src string) string {
	bad := invalid[r.IntN(len(invalid))]
	switch r.IntN(4) {
	case 0:
		if i := strings.Index(src, "//"); i >= 0 {
			return src[:i+2] + bad + src[i+2:]
		}
		return "// " + bad + "\n" + src
	case 1:
		if i := strings.Index(src, "\""); i >= 0 {
			return src[:i+1] + bad + src[i+1:]
		}
		return src + "\nlet s = \"" + bad + "\"\n"
	case 2:
		return src[:r.IntN(len(src)+1)]
	default:
		i := r.IntN(len(src) + 1)
		return src[:i] + string(rune(r.IntN(128))) + src[i:]
	}
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

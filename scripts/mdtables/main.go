// Command mdtables writes internal/sketch/std/markdown/tables.ergo, the
// Unicode and HTML tables std markdown reads. Run it from the repository
// root when an input changes:
//
//	go run ./scripts/mdtables          # write tables.ergo
//	go run ./scripts/mdtables -check   # exit 1 when tables.ergo is not what it writes
//
// Its inputs are checked in beside the package, in testdata:
//   - entities.json, the WHATWG list of HTML named character references
//     (https://html.spec.whatwg.org/entities.json); CommonMark reads only
//     the names that end in ';', so only those are written.
//   - CaseFolding.txt of the Unicode release Go's unicode package has
//     (unicode.Version): the C and F rows, Unicode full case folding,
//     which CommonMark matches link labels by.
//
// and Go's unicode.P and unicode.S tables, CommonMark's "Unicode
// punctuation character" (general category P or S).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func main() {
	dir := flag.String("dir", filepath.Join("internal", "sketch", "std", "markdown"), "the markdown package")
	check := flag.Bool("check", false, "compare with tables.ergo instead of writing it")
	flag.Parse()
	out, err := generate(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdtables:", err)
		os.Exit(1)
	}
	path := filepath.Join(*dir, "tables.ergo")
	if *check {
		old, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(old, out) {
			fmt.Fprintf(os.Stderr, "mdtables: %s is not what go run ./scripts/mdtables writes\n", path)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "mdtables:", err)
		os.Exit(1)
	}
}

func generate(dir string) ([]byte, error) {
	names, texts, err := entities(filepath.Join(dir, "testdata", "entities.json"))
	if err != nil {
		return nil, err
	}
	from, to, err := folding(filepath.Join(dir, "testdata", "CaseFolding.txt"))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	writeStrings(&b, "entityNames", names)
	writeStrings(&b, "entityTexts", texts)
	writeInts(&b, "foldRunes", from)
	writeStrings(&b, "foldTexts", to)
	writeInts(&b, "punctRanges", punctuation())
	return append(bytes.TrimRight(b.Bytes(), "\n"), '\n'), nil
}

func entities(path string) ([]string, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var all map[string]struct {
		Characters string `json:"characters"`
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, nil, err
	}
	var names []string
	for k := range all {
		if strings.HasPrefix(k, "&") && strings.HasSuffix(k, ";") {
			names = append(names, k[1:len(k)-1])
		}
	}
	sort.Strings(names)
	texts := make([]string, len(names))
	for i, n := range names {
		texts[i] = all["&"+n+";"].Characters
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("%s has no named reference", path)
	}
	return names, texts, nil
}

func folding(path string) ([]int, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	type row struct {
		r    int
		text string
	}
	var rows []row
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			want := "# CaseFolding-" + unicode.Version + ".txt"
			if line != want {
				return nil, nil, fmt.Errorf("%s starts %q, want %q (Go's unicode.Version)", path, line, want)
			}
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		parts := strings.Split(line, ";")
		if len(parts) < 3 {
			continue
		}
		status := strings.TrimSpace(parts[1])
		if status != "C" && status != "F" {
			continue
		}
		r, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 16, 32)
		if err != nil {
			return nil, nil, err
		}
		var text []rune
		for _, h := range strings.Fields(parts[2]) {
			c, err := strconv.ParseInt(h, 16, 32)
			if err != nil {
				return nil, nil, err
			}
			text = append(text, rune(c))
		}
		rows = append(rows, row{int(r), string(text)})
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].r < rows[j].r })
	from := make([]int, len(rows))
	to := make([]string, len(rows))
	for i, r := range rows {
		from[i], to[i] = r.r, r.text
	}
	return from, to, nil
}

func punctuation() []int {
	var los, his []int
	for _, t := range []*unicode.RangeTable{unicode.P, unicode.S} {
		for _, r := range t.R16 {
			for c := int(r.Lo); c <= int(r.Hi); c += int(r.Stride) {
				los, his = append(los, c), append(his, c)
			}
		}
		for _, r := range t.R32 {
			for c := int(r.Lo); c <= int(r.Hi); c += int(r.Stride) {
				los, his = append(los, c), append(his, c)
			}
		}
	}
	idx := make([]int, len(los))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return los[idx[a]] < los[idx[b]] })
	var out []int
	for _, i := range idx {
		lo, hi := los[i], his[i]
		if n := len(out); n > 0 && lo <= out[n-1]+1 {
			if hi > out[n-1] {
				out[n-1] = hi
			}
			continue
		}
		out = append(out, lo, hi)
	}
	return out
}

func writeStrings(b *bytes.Buffer, name string, xs []string) {
	fmt.Fprintf(b, "let %s = []string{", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(x))
	}
	b.WriteString("}\n\n")
}

func writeInts(b *bytes.Buffer, name string, xs []int) {
	fmt.Fprintf(b, "let %s = []int{", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "0x%x", x)
	}
	b.WriteString("}\n\n")
}

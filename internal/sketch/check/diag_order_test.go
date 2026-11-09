package check

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// diagOrderPackage is a generated package of n files with diagnostics in
// several of them. Each file has a name from a pool that holds two equal
// base names in different folders and the empty name (an unnamed file) more
// than once. Every file opens with the same declaration, so files have
// diagnostics at equal offsets with one code and one message; the rest
// adds unknown names, wrong results, unused imports and a type declared in
// more than one file.
func diagOrderPackage(r *rand.Rand, n int) (names, srcs []string) {
	pool := []string{"", "", "", "x.ergo", "a/x.ergo", "b/x.ergo", "y.ergo", "lib/z.ergo"}
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	for i := range n {
		var b strings.Builder
		if r.Intn(2) == 0 {
			b.WriteString("import log\n")
		}
		// The same first declaration in every file: one offset, one code
		// and one message in each file that has no import line.
		b.WriteString("func h() int { nope }\n")
		for k := range 1 + r.Intn(3) {
			switch r.Intn(4) {
			case 0:
				fmt.Fprintf(&b, "func f%d_%d() int { missing%d }\n", i, k, r.Intn(3))
			case 1:
				fmt.Fprintf(&b, "func g%d_%d() int { \"%s\" }\n", i, k, strings.Repeat("s", 1+r.Intn(3)))
			case 2:
				b.WriteString("type Dup {}\n")
			case 3:
				fmt.Fprintf(&b, "func k%d_%d() int { %s }\n", i, k, strings.Repeat("q", 1+r.Intn(3)))
			}
		}
		names = append(names, pool[i])
		srcs = append(srcs, b.String())
	}
	return names, srcs
}

// diagOrderList checks the files listed in order and returns each
// diagnostic as (file, offset, length, code, message), in the order Check
// returns them. The file is its name, and an unnamed file is its text,
// which is all that identifies it.
func diagOrderList(t *testing.T, names, srcs []string, order []int) []string {
	t.Helper()
	files := parseAll(t, listed(srcs, order))
	for i, k := range order {
		files[i].Name = names[k]
	}
	rank := make([]int, len(files))
	for r, k := range canonicalFiles(files) {
		rank[k] = r
	}
	_, ds := Check("main", files, nil)
	var out []string
	for i, d := range ds {
		// The diagnostics of one file are listed together, and the files
		// come in their canonical order (name, key, text): two unnamed
		// files, or two files of one base name, are never interleaved.
		if i > 0 && rank[ds[i-1].File] > rank[d.File] {
			t.Fatalf("order %v: diagnostics not grouped in the canonical order of their files: %v", order, ds)
		}
		f := files[d.File]
		id := f.Name
		if id == "" {
			id = fmt.Sprintf("%q", f.Src)
		}
		out = append(out, fmt.Sprintf("%s %d %d %s %s", id, d.Offset, d.Length, d.Code, d.Message))
	}
	return out
}

// TestCheckDiagnosticOrderIsCanonical: for generated packages with
// diagnostics in two or more files, every order of the file list gives the
// same list of (file, offset, length, code, message). The diagnostics are
// sorted by the canonical order of their files (canonicalFiles: name, key,
// text), not by the caller's index, which each still names.
func TestCheckDiagnosticOrderIsCanonical(t *testing.T) {
	for seed := range int64(40) {
		r := rand.New(rand.NewSource(seed))
		n := 3 + r.Intn(2)
		names, srcs := diagOrderPackage(r, n)
		var want []string
		for _, order := range canonOrders(n) {
			got := diagOrderList(t, names, srcs, order)
			if want == nil {
				want = got
				files := map[string]bool{}
				for _, d := range got {
					files[strings.SplitN(d, " ", 2)[0]] = true
				}
				if len(files) < 2 {
					t.Fatalf("seed %d: want diagnostics in two or more files, got %v", seed, got)
				}
				continue
			}
			if !slices.Equal(got, want) {
				t.Fatalf("seed %d, order %v (names %q): diagnostics\n%s\nwant\n%s", seed, order, names, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	}
}

//go:build (linux && (amd64 || arm64)) || darwin

package v1

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExchangePathsAreTheNames is the law of exchangePaths' unsafe body
// (internal/residue/testdata/unsafe.txt): the memory the host call reads
// through the two pointers is exactly the two path names, each ended by
// one NUL, so the call swaps the two files the names name and nothing
// else. It is a seeded property over generated names of every length up
// to NAME_MAX, each with decoy files beside it named by its prefix and by
// it plus a byte: a pointer to a path with no NUL after it lets the
// kernel read on past the name into whatever follows it in memory (a
// decoy's name or a missing file), and a pointer to a name truncated at
// an inner NUL swaps the decoy the truncated name names. A name with a
// NUL in it is refused before the call, with nothing moved.
//
// Mutant (runtime/v1 ledger, exchange-path-unterminated): passing the
// string's bytes (unsafe.StringData) instead of BytePtrFromString's NUL
// ended copy lets "dir/prefix\x00rest" swap the decoy dir/prefix.
func TestExchangePathsAreTheNames(t *testing.T) {
	const seed = 1
	r := rand.New(rand.NewPCG(seed, 0))
	for c := 0; c < 120; c++ {
		dir := t.TempDir()
		x, y := genName(r, 1+r.IntN(255)), genName(r, 1+r.IntN(255))
		for x == y {
			y = genName(r, 1+r.IntN(255))
		}
		files := map[string]string{x: "x", y: "y"}
		for _, n := range []string{x, y} {
			if p := runePrefix(n, len(n)/2); p != n {
				files[p] = "decoy " + p
			}
			if len(n) < 255 {
				files[n+"~"] = "decoy " + n + "~"
			}
		}
		files[x], files[y] = "x", "y"
		for n, text := range files {
			write(t, filepath.Join(dir, n), text)
		}
		if err := FSExchange(filepath.Join(dir, x), filepath.Join(dir, y)); err != nil {
			t.Fatalf("case %d (seed %d): FSExchange(%q, %q) = %v", c, seed, x, y, err)
		}
		files[x], files[y] = "y", "x"
		expect(t, c, dir, files)

		// A name with a NUL in it: the host must not see its prefix.
		cut := y
		for cut == y {
			cut = runePrefix(x, r.IntN(len(x)+1)%len(x))
		}
		if _, ok := files[cut]; !ok {
			write(t, filepath.Join(dir, cut), "cut")
			files[cut] = "cut"
		}
		bad := filepath.Join(dir, cut) + "\x00" + "rest"
		if err := FSExchange(bad, filepath.Join(dir, y)); err == nil {
			t.Fatalf("case %d (seed %d): FSExchange of a name with a NUL = nil; want a refusal", c, seed)
		}
		if err := FSExchange(filepath.Join(dir, y), bad); err == nil {
			t.Fatalf("case %d (seed %d): FSExchange to a name with a NUL = nil; want a refusal", c, seed)
		}
		expect(t, c, dir, files)
	}
}

// genName is a file name of at most n bytes and at least one: printable
// ASCII but '/' and '.', and two- and three-byte UTF-8 runes (APFS takes
// only UTF-8 names).
func genName(r *rand.Rand, n int) string {
	var b strings.Builder
	for {
		var c rune
		switch r.IntN(8) {
		case 0:
			c = rune(0x80 + r.IntN(0x780))
		case 1:
			c = rune(0x800 + r.IntN(0xD000-0x800))
		default:
			c = rune(0x21 + r.IntN(0x5e))
			if c == '/' || c == '.' {
				c = '_'
			}
		}
		if b.Len()+utf8.RuneLen(c) > n {
			break
		}
		b.WriteRune(c)
	}
	if b.Len() == 0 {
		return "n"
	}
	return b.String()
}

// runePrefix is the longest prefix of s of at most n bytes that ends on a
// rune boundary, or s's first rune.
func runePrefix(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if n == 0 {
		_, w := utf8.DecodeRuneInString(s)
		return s[:w]
	}
	return s[:n]
}

// expect checks that the files of dir are exactly files.
func expect(t *testing.T, c int, dir string, files map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(files) {
		t.Fatalf("case %d: dir holds %d files; want %d", c, len(entries), len(files))
	}
	for n, text := range files {
		if got := read(t, filepath.Join(dir, n)); got != text {
			t.Fatalf("case %d: %q holds %q; want %q", c, n, got, text)
		}
	}
}

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
		x, y := genPair(r)
		for x == y {
			x, y = genPair(r)
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
			if err := os.WriteFile(filepath.Join(dir, n), []byte(text), 0o644); err != nil {
				t.Fatalf("case %d (seed %d): the generator drew a name this host refuses: %q: %v", c, seed, n, err)
			}
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

// nameRunes are the runes a generated name draws from beside ASCII: each
// is assigned in every Unicode version the tested hosts' file systems
// read (APFS refuses a name with a rune it does not know, EILSEQ, as it
// refuses invalid UTF-8), lower case or caseless, precomposed, and no two
// fold together, so two distinct names stay two files on a host that
// compares names case- and normalization-insensitively (APFS's default).
var nameRunes = [][2]rune{
	{0xE0, 0xF6}, {0xF8, 0xFE}, // Latin-1 lower case letters
	{0x3B1, 0x3C1}, {0x3C3, 0x3C9}, // Greek lower case, final sigma left out (it folds to sigma)
	{0x430, 0x44F},   // Cyrillic lower case
	{0x3041, 0x3094}, // Hiragana
	{0x4E00, 0x9FA5}, // CJK unified ideographs of Unicode 1.1
}

// genName is a file name of at most n bytes and at least one, valid UTF-8
// that every tested host's file system accepts: printable ASCII but '/'
// and the capitals (a space and '.' included, so a name may hold spaces
// or end in a dot), and two- and three-byte runes from nameRunes. A name
// never starts with a dot, so no name or prefix of one is "." or "..".
// A host that refuses a drawn name at create is this generator's
// error, which the test reports as one.
func genName(r *rand.Rand, n int) string {
	var b strings.Builder
	for {
		var c rune
		switch r.IntN(8) {
		case 0, 1:
			span := nameRunes[r.IntN(len(nameRunes))]
			c = span[0] + rune(r.IntN(int(span[1]-span[0])+1))
		default:
			c = rune(0x20 + r.IntN(0x5f))
			if c == '/' || 'A' <= c && c <= 'Z' {
				c = '_'
			}
		}
		if b.Len()+utf8.RuneLen(c) > n {
			break
		}
		b.WriteRune(c)
	}
	name := b.String()
	if name == "" || name[0] == '.' {
		// No name, and no prefix of one (the decoys, the cut), is "."
		// or "..": a name never starts with a dot.
		name = "n" + name[min(1, len(name)):]
	}
	return name
}

// genPair is two distinct names: independent, or, one case in three, y
// sharing x's first bytes up to a long prefix and differing only after it.
// One name in four ends in a dot.
func genPair(r *rand.Rand) (string, string) {
	x := genName(r, 1+r.IntN(255))
	var y string
	for y == "" || y == x {
		if len(x) > 16 && r.IntN(3) == 0 {
			p := runePrefix(x, len(x)-1-r.IntN(8))
			y = p + genName(r, 1+r.IntN(255-len(p)))
		} else {
			y = genName(r, 1+r.IntN(255))
		}
	}
	dot := func(n string) string {
		if len(n) < 255 && r.IntN(4) == 0 {
			return n + "."
		}
		return n
	}
	x, y = dot(x), dot(y)
	if x == y {
		y = genName(r, 1+r.IntN(255))
	}
	return x, y
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

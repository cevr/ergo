package lower

import (
	"crypto/sha256"
	"hash/crc32"
	"math/rand/v2"
	"strconv"
)

// stdGo5Cases are NP4f-5's moved functions, each against Go's own:
// sha256.Sum and crc32.Checksum, which bind Go's digests on a view of the
// string's bytes, and strconv's ParseBool and Unquote, whose Go error is
// std's Malformed. Edges are the empty text, invalid bytes, every spelling
// ParseBool takes and near misses of them, and the three quote forms with
// escapes, bad escapes, newlines and unterminated literals.
func stdGo5Cases(r *rand.Rand, n int) []bodyCase {
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo5Decls
		return c
	}
	text := np4fErgoString
	digests := []string{"", "a", "abc", "\x00", "\xff\xfe", "é世🙂", "the quick brown fox jumps over the lazy dog, twice: the quick brown fox"}
	bools := []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"}
	genBool := func(r *rand.Rand) string {
		if r.IntN(2) == 0 {
			return bools[r.IntN(len(bools))]
		}
		return stdGo2Text(r)
	}
	genQuoted := func(r *rand.Rand) string {
		s := stdGo2Text(r)
		switch r.IntN(6) {
		case 0:
			return strconv.Quote(s)
		case 1:
			return strconv.QuoteToASCII(s)
		case 2:
			return "`" + s + "`"
		case 3:
			return strconv.QuoteRune(rune(r.IntN(0x110000 + 2)))
		case 4:
			q := strconv.Quote(s)
			return q[:r.IntN(len(q))]
		}
		return s
	}
	return []bodyCase{
		with(bodyRows("sha256.Sum", []string{"sha256", "strconv"}, "s string", `bs(sha256.Sum(s))`,
			digests, n, r, stdGo2Text, text, func(s string) string {
				d := sha256.Sum256([]byte(s))
				return stdGo2Bytes(string(d[:]))
			})),
		with(bodyRows("crc32.Checksum", []string{"crc32", "strconv"}, "s string", `"\(crc32.Checksum(s))"`,
			digests, n, r, stdGo2Text, text, func(s string) string {
				return strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(s))), 10)
			})),
		with(bodyRows("strconv.ParseBool", []string{"strconv"}, "s string", `np5Bool(s)`,
			append(bools, "", "tRUE", "yes", "2", " true", "true ", "\xff", "t\x00"), n, r, genBool, text, func(s string) string {
				b, err := strconv.ParseBool(s)
				if err != nil {
					return "malformed " + stdGo2Bytes(s)
				}
				return strconv.FormatBool(b)
			})),
		with(bodyRows("strconv.Unquote", []string{"strconv"}, "s string", `np5Unquote(s)`,
			[]string{"", `""`, `"`, "``", "`a\nb`", "`a`b`", "'x'", "'ab'", "''", `'\''`, `"\x41\u00e9\U0001F642"`,
				`"\q"`, "\"a\nb\"", "\"\xff\"", "'\xff'", `"\377"`, `'\400'`, `"a`, `x"a"`, ` "a" `, `"\ud800"`}, n, r, genQuoted, text,
			func(s string) string {
				u, err := strconv.Unquote(s)
				if err != nil {
					return "malformed " + stdGo2Bytes(s)
				}
				return stdGo2Bytes(u)
			})),
	}
}

// stdGo5Decls are stdGo2Decls with printers of a parse that tells
// Malformed, with the text it names, from a value.
const stdGo5Decls = stdGo2Decls + `

func np5Bool(s string) string {
	let b = strconv.ParseBool(s) catch {
	case strconv.Malformed as m:
		return "malformed " + bs(m.Text)
	}
	"\(b)"
}

func np5Unquote(s string) string {
	let u = strconv.Unquote(s) catch {
	case strconv.Malformed as m:
		return "malformed " + bs(m.Text)
	}
	bs(u)
}`

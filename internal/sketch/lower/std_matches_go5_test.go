package lower

import (
	"crypto/sha256"
	"hash/crc32"
	"math/rand/v2"
	"strconv"
)

// stdGo5Cases are NP4f-5's moved functions, each against Go's own:
// sha256.Sum and crc32.Checksum, which bind Go's digests on a view of the
// string's bytes, and strconv.ParseBool, an Ergo match whose Malformed is
// Go's error. Edges are the empty text, invalid bytes, every spelling
// ParseBool takes and near misses of them.
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
	}
}

// stdGo5Decls are stdGo2Decls with a printer of a parse that tells
// Malformed, with the text it names, from a value.
const stdGo5Decls = stdGo2Decls + `

func np5Bool(s string) string {
	let b = strconv.ParseBool(s) catch {
	case strconv.Malformed as m:
		return "malformed " + bs(m.Text)
	}
	"\(b)"
}`

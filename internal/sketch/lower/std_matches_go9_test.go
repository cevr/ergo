package lower

import (
	"encoding/base64"
	"hash/crc32"
	"math/rand/v2"
	"strconv"
)

// stdGo9Cases are NP4f-9's moved functions, each against Go's own:
// base64.Encode in each alphabet, which calls Go's StdEncoding or
// URLEncoding (package variables), and crc32.Update, which passes Go's
// IEEETable. Edges are the empty text, invalid UTF-8, lengths around each
// multiple of three (padding) and bytes that encode to + and / (or - and _).
func stdGo9Cases(r *rand.Rand, n int) []bodyCase {
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo2Decls
		return c
	}
	text := np4fErgoString
	texts := []string{"", "a", "ab", "abc", "abcd", "\xfb\xff", "\xff\xfe\xfd", "\x00", "é世🙂", "the quick brown fox?>"}
	type upd struct {
		crc uint32
		s   string
	}
	updates := []upd{{0, ""}, {0, "abc"}, {1, ""}, {0xffffffff, "abc"}, {0xcbf43926, "123456789"}, {7, "\xff\x00"}}
	genUpd := func(r *rand.Rand) upd { return upd{r.Uint32(), stdGo2Text(r)} }
	return []bodyCase{
		with(bodyRows("base64.Encode StdEncoding", []string{"base64", "strconv"}, "s string", `base64.Encode(base64.StdEncoding, s)`,
			texts, n, r, stdGo2Text, text, func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) })),
		with(bodyRows("base64.Encode URLEncoding", []string{"base64", "strconv"}, "s string", `base64.Encode(base64.URLEncoding, s)`,
			texts, n, r, stdGo2Text, text, func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) })),
		with(bodyRows("crc32.Update", []string{"crc32", "strconv"}, "crc uint32, s string", `"\(crc32.Update(crc, s))"`,
			updates, n, r, genUpd, func(v upd) string { return strconv.FormatUint(uint64(v.crc), 10) + ", " + text(v.s) },
			func(v upd) string {
				return strconv.FormatUint(uint64(crc32.Update(v.crc, crc32.IEEETable, []byte(v.s))), 10)
			})),
	}
}

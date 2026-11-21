package lower

import (
	"math/rand/v2"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// stdGo15Decls print a query's fields, each name and value quoted.
const stdGo15Decls = `func fieldsText(fs []http.Field) string {
	let mut out = ""
	for f in fs {
		out = out + strconv.Quote(f.Name) + "=" + strconv.Quote(f.Value) + ";"
	}
	out
}`

// stdGo15Cases are NP4f-15's moved http functions against Go's own:
// ParseQuery against url.ParseQuery with its keys sorted and each key's
// values in order (the old body, httpParseQuery), and EncodeQuery
// against url.QueryEscape joined in order (the old body). Edges are the
// empty query, "&&", a bare key, "=", a ";" pair, malformed and truncated
// escapes ("%", "%4", "%zz"), "+", upper and lower case hex, repeated keys
// out of order, invalid UTF-8 and non-ASCII, and 10000 and 10001 pairs
// (Go's default limit); seeded queries mix them. Mutants (std ledger):
// np4f15-parse-unsorted, np4f15-parse-unstable; the others left with the
// old Ergo body when SM2 moved ParseQuery and EncodeQuery to std url (the
// b24 merge, I-FLIP).
func stdGo15Cases(r *rand.Rand, n int) []bodyCase {
	with := func(c bodyCase) bodyCase {
		c.decls = stdGo15Decls
		return c
	}
	text := np4fErgoString
	fieldsText := func(names, values []string) string {
		var b strings.Builder
		for i := range names {
			b.WriteString(strconv.Quote(names[i]) + "=" + strconv.Quote(values[i]) + ";")
		}
		return b.String()
	}
	oldParse := func(raw string) string {
		values, _ := url.ParseQuery(raw)
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var ns, vs []string
		for _, k := range keys {
			for _, v := range values[k] {
				ns, vs = append(ns, k), append(vs, v)
			}
		}
		return fieldsText(ns, vs)
	}
	queries := []string{"", "&&", "a", "=", "a=", "=b", "a=1&b=2", "b=2&a=1&b=1", "a=1;b=2&c=3", "x=%", "x=%4", "x=%zz&y=1", "%41=%62",
		"a+b=c+d", "k=%e4%B8%96", "k=\xff\xfe", "é=ü", "a=1&&a=2&", strings.Repeat("a&", 9999) + "b", strings.Repeat("a&", 10000) + "b"}
	pieces := []string{"a", "b", "=", "&", "%", "%2", "%2B", "%zz", "+", ";", "é", "\xff", "x", "1", "%41"}
	genQuery := func(r *rand.Rand) string {
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		return b.String()
	}
	type fields struct{ names, values []string }
	fieldsLit := func(f fields) string {
		var b strings.Builder
		b.WriteString("[]http.Field{")
		for i := range f.names {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("http.Field{Name: " + text(f.names[i]) + ", Value: " + text(f.values[i]) + "}")
		}
		return b.String() + "}"
	}
	oldEncode := func(f fields) string {
		parts := make([]string, len(f.names))
		for i := range f.names {
			parts[i] = url.QueryEscape(f.names[i]) + "=" + url.QueryEscape(f.values[i])
		}
		return strings.Join(parts, "&")
	}
	encodes := []fields{{nil, nil}, {[]string{""}, []string{""}}, {[]string{"a b", "~-_."}, []string{"x&y=z", "100%"}},
		{[]string{"é", "\xff"}, []string{"/?#[]@!$'()*,;", "\x00\n\t"}}, {[]string{"b", "a", "b"}, []string{"1", "2", "3"}}}
	genEncode := func(r *rand.Rand) fields {
		var f fields
		for range r.IntN(4) {
			f.names = append(f.names, stdGo2Text(r))
			f.values = append(f.values, stdGo2Text(r))
		}
		return f
	}
	return []bodyCase{
		with(bodyRows("http.ParseQuery", []string{"http", "strconv"}, "raw string", `fieldsText(http.ParseQuery(raw))`,
			queries, n, r, genQuery, text, oldParse)),
		with(bodyRows("http.EncodeQuery", []string{"http", "strconv"}, "fs []http.Field", `http.EncodeQuery(fs)`,
			encodes, n, r, genEncode, fieldsLit, oldEncode)),
	}
}

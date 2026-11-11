package v1

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The names of std config (docs/spec/stdlib.md, config). A key is a
// dotted path of wire keys (types.md, Codecs 2: `apiToken`, `server.port`),
// and each source spells it its own way:
//
//   - a flag: each wire key's words in lower case, joined by "-", and the
//     keys joined by ".": `api-token`, `server.port`;
//   - an environment variable: the prefix, "_", each wire key's words in
//     upper case joined by "_", and the keys joined by "__":
//     `NOTES_API_TOKEN`, `NOTES_SERVER__PORT` (pydantic-settings'
//     env_nested_delimiter, ASP.NET Core's "__").
//
// A word of a wire key starts at its first byte and at each upper-case
// letter: `httpStatus` is http and status, `v2Path` is v2 and path. The
// reverse maps join the words back, the first as it is and each other with
// its first letter in upper case, so a flag's and a variable's name map to
// the key they were made from (law config.NamesInvert). Each body is pure:
// it reads its arguments only, so it names no other function of this
// package (Pure bodies 2) and splits with a local function.

// ConfigFlagName is the flag name of key.
//
//ergo:pure
func ConfigFlagName(key string) string {
	parts := strings.Split(key, ".")
	for i, p := range parts {
		var words []string
		start := 0
		for j, r := range p {
			if j > 0 && unicode.IsUpper(r) {
				words = append(words, p[start:j])
				start = j
			}
		}
		parts[i] = strings.ToLower(strings.Join(append(words, p[start:]), "-"))
	}
	return strings.Join(parts, ".")
}

// ConfigEnvName is the environment variable of key under prefix.
//
//ergo:pure
func ConfigEnvName(prefix, key string) string {
	parts := strings.Split(key, ".")
	for i, p := range parts {
		var words []string
		start := 0
		for j, r := range p {
			if j > 0 && unicode.IsUpper(r) {
				words = append(words, p[start:j])
				start = j
			}
		}
		parts[i] = strings.ToUpper(strings.Join(append(words, p[start:]), "_"))
	}
	return prefix + "_" + strings.Join(parts, "__")
}

// ConfigFlagKey is the key of the flag name: each "."-part's "-"-words
// joined in camel case, each word but the first with its first letter in
// upper case.
//
//ergo:pure
func ConfigFlagKey(name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		var b strings.Builder
		for j, w := range strings.Split(p, "-") {
			if j > 0 && w != "" {
				r, n := utf8.DecodeRuneInString(w)
				b.WriteRune(unicode.ToUpper(r))
				w = w[n:]
			}
			b.WriteString(w)
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, ".")
}

// ConfigEnvKey is the key of a variable's name after its prefix and "_":
// each "__"-part's "_"-words in lower case, joined as ConfigFlagKey joins.
//
//ergo:pure
func ConfigEnvKey(rest string) string {
	parts := strings.Split(rest, "__")
	for i, p := range parts {
		var b strings.Builder
		for j, w := range strings.Split(p, "_") {
			w = strings.ToLower(w)
			if j > 0 && w != "" {
				r, n := utf8.DecodeRuneInString(w)
				b.WriteRune(unicode.ToUpper(r))
				w = w[n:]
			}
			b.WriteString(w)
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, ".")
}

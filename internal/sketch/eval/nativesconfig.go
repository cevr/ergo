package eval

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std config's names (ORACLE O18), from docs/spec/stdlib.md, config: a key
// is a dotted path of wire keys; a word of a wire key starts at its first
// byte and at each upper-case letter. A flag spells each key's words in
// lower case joined by "-", the keys joined by "."; a variable is the
// prefix, "_", each key's words in upper case joined by "_", the keys
// joined by "__". The reverse maps join the words back in camel case: the
// first word as it is (a variable's in lower case), each other with its
// first letter in upper case.
func init() {
	pure := func(f func(args []string) string) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			var ss []string
			for _, a := range args {
				ss = append(ss, a.(string))
			}
			out := f(ss)
			return out, in.allocate(fr, at, int64(len(out)))
		}
	}
	natives["config.flagName"] = pure(func(a []string) string {
		return spellKey(a[0], ".", "-", strings.ToLower)
	})
	natives["config.envName"] = pure(func(a []string) string {
		return a[0] + "_" + spellKey(a[1], "__", "_", strings.ToUpper)
	})
	natives["config.flagKey"] = pure(func(a []string) string {
		return joinKey(a[0], ".", "-", func(s string) string { return s })
	})
	natives["config.envKey"] = pure(func(a []string) string {
		return joinKey(a[0], "__", "_", strings.ToLower)
	})
	natives["config.NewRedacted"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return &redactedV{v: args[0]}, in.charge(fr, at, 1)
	}
}

// redactedV is a config.Redacted: Reveal is the one read of its value,
// and no text of it shows the value.
type redactedV struct{ v Value }

func (r *redactedV) describe() string { return "<Redacted>" }

func (r *redactedV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if name != "Reveal" {
		ice("the Redacted operation %s", name)
	}
	return r.v, nil
}

// spellKey spells each "."-part of key as its words, each through
// casing, joined by wordSep, and the parts joined by partSep.
func spellKey(key, partSep, wordSep string, casing func(string) string) string {
	parts := strings.Split(key, ".")
	for i, p := range parts {
		// Byte offsets, so a byte of invalid UTF-8 stays as it is.
		var words []string
		start := 0
		for j, r := range p {
			if j > 0 && unicode.IsUpper(r) {
				words = append(words, p[start:j])
				start = j
			}
		}
		words = append(words, p[start:])
		parts[i] = casing(strings.Join(words, wordSep))
	}
	return strings.Join(parts, partSep)
}

// joinKey is the key of name: each partSep-part's wordSep-words, each
// through casing, in camel case, the parts joined by ".".
func joinKey(name, partSep, wordSep string, casing func(string) string) string {
	parts := strings.Split(name, partSep)
	for i, p := range parts {
		var b strings.Builder
		for j, w := range strings.Split(p, wordSep) {
			w = casing(w)
			if j > 0 && w != "" {
				r, n := utf8.DecodeRuneInString(w)
				w = string(unicode.ToUpper(r)) + w[n:]
			}
			b.WriteString(w)
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, ".")
}

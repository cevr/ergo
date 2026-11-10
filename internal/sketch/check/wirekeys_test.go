package check_test

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
)

// wireKeyReference is types.md Codecs 2 by words: the name's leading
// capitals are one initialism, unless a lower-case letter follows them,
// when the last of them begins the next word; the initialism is lowered
// whole and the rest is kept.
func wireKeyReference(name string) string {
	caps := len(name) - len(strings.TrimLeft(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	rest := name[caps:]
	if caps > 1 && rest != "" && strings.ToLower(rest[:1]) == rest[:1] && strings.ToUpper(rest[:1]) != rest[:1] {
		return strings.ToLower(name[:caps-1]) + name[caps-1:]
	}
	return strings.ToLower(name[:caps]) + rest
}

// TestWireKeysDeclared catches gw-key, gw-origin and gw-format: ignoring an
// explicit key, losing it through inclusion/instantiation, or dropping it in
// formatting. The implicit-key property never declares a key. Each seed
// reaches both an explicit/implicit collision and a renamed non-collision.
func TestWireKeysDeclared(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 98))
		name := wireName(r)
		for _, clash := range []bool{false, true} {
			key := "renamed_" + strconv.FormatUint(seed, 10) + " 🧭\"\\"
			if clash {
				key = wireKeyReference(name)
			}
			src := fmt.Sprintf("type Base[T] { Value T %s }\ntype R[T] { ..Base[T], %s int }\nfunc keep[T wire](v T) T { v }\nfunc use(v R[string]) R[string] { keep(v) }\n", strconv.Quote(key), name)
			formatted, err := format.Source(src)
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, src)
			}
			for _, text := range []string{src, formatted} {
				_, ds := check.CheckSource(text)
				if (!clash && len(ds) != 0) || (clash && (len(ds) != 1 || ds[0].Code != "S108" || !strings.Contains(ds[0].Message, fmt.Sprintf("fields Value and %s, which share the wire key %q", name, key)))) {
					t.Fatalf("seed %d clash=%t: %v\n%s\nreplay: go test -run '^TestWireKeysDeclared$' ./internal/sketch/check", seed, clash, ds, text)
				}
			}
		}
	}
}

// TestWireKeyDiagnostics catches gw-invalid and gw-group. These diagnostic
// text rows cover invalid metadata, which the valid-key property cannot draw.
func TestWireKeyDiagnostics(t *testing.T) {
	for _, row := range []struct{ src, code, text string }{
		{`type R { A, B string "x" }`, "S001", "a wire key names one field"},
		{`type R { A string "" }`, "S108", "a wire key must be non-empty valid UTF-8"},
		{`type R { A string "\xff" }`, "S108", "a wire key must be non-empty valid UTF-8"},
		{`type R = A{Tag string "_tag"} | B{}
func keep[T wire](v T) T { v }
func use(v R) R { keep(v) }`, "S108", `wire key "_tag", reserved for a union tag`},
	} {
		_, ds := check.CheckSource(row.src)
		if len(ds) != 1 || ds[0].Code != row.code || !strings.Contains(ds[0].Message, row.text) {
			t.Fatalf("%s: want %s %q, got %v", row.src, row.code, row.text, ds)
		}
	}
}

// fieldName draws from fewer letters, so that two names of one record
// often share a key (AI and Ai are both ai).
func fieldName(r *rand.Rand) string {
	b := []byte{"AI"[r.IntN(2)]}
	for n := r.IntN(3); n > 0; n-- {
		b = append(b, "AIai"[r.IntN(4)])
	}
	return string(b)
}

func wireName(r *rand.Rand) string {
	const letters = "AIDXUabd1"
	b := []byte{"AIDXU"[r.IntN(5)]}
	for n := r.IntN(5); n > 0; n-- {
		b = append(b, letters[r.IntN(len(letters))])
	}
	return string(b)
}

// TestWireKeysFollowTheRule is the H3 property. Over generated exported
// names, check.WireFieldName (the one key mapper of the codec, the TS
// projection and ergo gen rpc) agrees with the reference; and a record
// with generated fields is wire exactly when their keys differ, the S108
// naming both fields and their key when two meet. A failing seed prints
// the names and the replay command.
func TestWireKeysFollowTheRule(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewPCG(seed, 81))
		name := wireName(r)
		if got, want := check.WireFieldName(&check.Field{Name: name}), wireKeyReference(name); got != want {
			t.Fatalf("seed %d: the key of %s is %q, want %q\nreplay: go test -run TestWireKeysFollowTheRule ./internal/sketch/check", seed, name, got, want)
		}
	}
	clashes, distinct := 0, 0
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 83))
		var names []string
		owner := map[string]string{}
		var clash [2]string
		for len(names) < 3 {
			n := fieldName(r)
			if strings.Contains(strings.Join(names, " ")+" ", n+" ") {
				continue
			}
			names = append(names, n)
			k := check.WireFieldName(&check.Field{Name: n})
			if o, ok := owner[k]; ok && clash[0] == "" {
				clash = [2]string{o, n}
			}
			owner[k] = n
		}
		src := fmt.Sprintf("func keep[T wire](v T) T { v }\n\ntype R { %s int, %s int, %s int }\n\nfunc use(r R) R { keep(r) }\n", names[0], names[1], names[2])
		_, diags := check.CheckSource(src)
		fail := func(what string) {
			t.Fatalf("seed %d: %s\n%s\ndiagnostics: %v\nreplay: go test -run TestWireKeysFollowTheRule ./internal/sketch/check", seed, what, src, diags)
		}
		if clash[0] == "" {
			distinct++
			if len(diags) != 0 {
				fail("a record of distinct keys is refused")
			}
			continue
		}
		if len(diags) != 1 || !strings.Contains(diags[0].Message, fmt.Sprintf("fields %s and %s, which share the wire key %q", clash[0], clash[1], check.WireFieldName(&check.Field{Name: clash[1]}))) {
			fail("a key clash is not the one S108 naming both fields")
		}
		clashes++
	}
	if clashes == 0 || distinct == 0 {
		t.Fatalf("the generator reached %d clashes and %d records of distinct keys; it must reach both", clashes, distinct)
	}
}

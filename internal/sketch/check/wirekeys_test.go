package check_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
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
		if got, want := check.WireFieldName(name), wireKeyReference(name); got != want {
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
			k := check.WireFieldName(n)
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
		if len(diags) != 1 || !strings.Contains(diags[0].Message, fmt.Sprintf("fields %s and %s, which share the wire key %q", clash[0], clash[1], check.WireFieldName(clash[1]))) {
			fail("a key clash is not the one S108 naming both fields")
		}
		clashes++
	}
	if clashes == 0 || distinct == 0 {
		t.Fatalf("the generator reached %d clashes and %d records of distinct keys; it must reach both", clashes, distinct)
	}
}

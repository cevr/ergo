package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestRedactedNeverRenders is the Go half of the property that a secret
// never appears in rendered output (docs/spec/stdlib.md, config 1): over
// generated Go values that hold a Redacted at a generated depth (a field,
// exported or not, a slice or map element, an interface, a pointer chain,
// an error's text), no fmt verb, no fmt.Errorf chain, no encoding/json and
// no log/slog handler writes any byte run of the secret. The check is only
// that: where fmt cannot call a method (an unexported field read by
// reflection) it prints the pointer's address, which holds no secret, so
// the rule is "the payload is behind a pointer", not "every path writes
// RedactedText".
//
// Mutant it kills: the payload held inline in the struct instead of behind
// a pointer field (fmt's bad-verb path prints the struct at depth 0, seed
// 4; an unexported field is printed by reflection). With the pointer field,
// removing Format alone leaks nothing either, so it is not claimed.
// Replay: ERGO_REDACTED_SEED=n.
func TestRedactedNeverRenders(t *testing.T) {
	first, last := 0, 400
	if s := os.Getenv("ERGO_REDACTED_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n+1
	}
	for seed := first; seed < last; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 11))
		secret, value, shape := redactedCase(r)
		for _, out := range redactedRenders(value) {
			if strings.Contains(out.text, secret) {
				t.Fatalf("seed %d: %s of %s shows the secret %q:\n%s\nreplay: ERGO_REDACTED_SEED=%d go test -run '^TestRedactedNeverRenders$' ./runtime/v1", seed, out.how, shape, secret, out.text, seed)
			}
		}
	}
}

type redactedRender struct{ how, text string }

// redactedRenders is every rendering of v the property holds.
func redactedRenders(v any) []redactedRender {
	var out []redactedRender
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d", "%x", "%X", "%10.3v"} {
		out = append(out, redactedRender{"fmt " + verb, fmt.Sprintf(verb, v)})
	}
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("held %v", v))
	out = append(out, redactedRender{"fmt.Errorf chain", wrapped.Error() + " " + errors.Unwrap(wrapped).Error()})
	if b, err := json.Marshal(map[string]any{"held": v}); err == nil {
		out = append(out, redactedRender{"encoding/json", string(b)})
	}
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("m", "held", v)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("m", "held", v)
	out = append(out, redactedRender{"slog text", text.String()}, redactedRender{"slog json", js.String()})
	return out
}

// Shapes that hold a secret, for redactedCase.
type (
	redactedExported struct {
		Name string
		Held any
	}
	redactedHidden struct {
		name string
		held any
	}
	redactedPair struct {
		Inner string
		N     int
	}
)

// redactedCase is a secret, a value that holds it as a Redacted at a
// generated depth, and the value's shape for the failure text.
func redactedCase(r *rand.Rand) (string, any, string) {
	var secret string
	var leaf any
	switch r.IntN(3) {
	case 0:
		secret = redactedWord(r)
		leaf = NewRedacted(secret)
	case 1:
		n := 1_000_000 + r.IntN(1_000_000_000)
		secret = strconv.Itoa(n)
		leaf = NewRedacted(n)
	default:
		secret = redactedWord(r)
		leaf = NewRedacted(redactedPair{Inner: secret, N: 3})
	}
	v, shape := leaf, "Redacted"
	for range r.IntN(5) {
		switch r.IntN(6) {
		case 0:
			v, shape = redactedExported{Name: "x", Held: v}, "exported field of "+shape
		case 1:
			v, shape = redactedHidden{name: "x", held: v}, "unexported field of "+shape
		case 2:
			v, shape = []any{1, v}, "slice of "+shape
		case 3:
			v, shape = map[string]any{"k": v}, "map of "+shape
		case 4:
			p := v
			v, shape = &p, "pointer to "+shape
		default:
			v, shape = struct{ A, B any }{A: "a", B: v}, "anonymous struct of "+shape
		}
	}
	return secret, v, shape
}

// redactedWord is a text no rendering writes by chance: twelve capitals.
func redactedWord(r *rand.Rand) string {
	b := make([]byte, 12)
	for i := range b {
		b[i] = byte('A' + r.IntN(26))
	}
	return string(b)
}

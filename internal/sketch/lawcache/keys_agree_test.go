package lawcache

import (
	"context"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// TestKeysAgreeWithErgo holds the driver's cache keys written in Ergo
// (compiler/cachekey, linked as internal/linked/compiler) to this
// package's, the reference until the S5 swap: Key (law verdicts and gate
// runs), Framed, Environ and TestResultKey, on seeded parts. Parts hold
// the bytes a framing could confuse (digits, ':', NUL, empty parts,
// multi-byte runes), and environments hold build variables, others, the
// skipped variable and duplicates. A failing case prints its seed and the
// replay command.
//
// Mutants it alone catches: internal/selfhostcheck/testdata/mutants.json,
// killer internal/sketch/lawcache:TestKeysAgreeWithErgo.
func TestKeysAgreeWithErgo(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SELFHOST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	replay := "replay: ERGO_SELFHOST_SEED=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestKeysAgreeWithErgo$' ./internal/sketch/lawcache"
	rng := rand.New(rand.NewPCG(seed, 0xca5e))
	ctx := context.Background()
	call := func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	const skip = "ERGO_STAGE_REPORT"
	kept := 0
	for c := 0; c < 300; c++ {
		parts := keyParts(rng)
		if g, w := call(linked.KeyLaw(ctx, parts)), Key(parts...); g != w {
			t.Fatalf("seed %d case %d: law key of %q: %s, want %s\n%s", seed, c, parts, g, w, replay)
		}
		if g, w := call(linked.KeyFramed(ctx, parts)), Framed(parts...); g != w {
			t.Fatalf("seed %d case %d: framed key of %q: %s, want %s\n%s", seed, c, parts, g, w, replay)
		}
		environ := keyEnviron(rng, skip)
		env := Environ(environ, skip)
		kept += len(env)
		want := ""
		for _, kv := range env {
			want += kv + "\n"
		}
		if g := call(linked.KeyEnviron(ctx, environ, skip)); g != want {
			t.Fatalf("seed %d case %d: environ of %q: %q, want %q\n%s", seed, c, environ, g, want, replay)
		}
		id := ""
		if rng.IntN(5) != 0 {
			id = strings.Join(keyParts(rng), "\x00")
		}
		ledger := "no ledger"
		if rng.IntN(2) == 0 {
			ledger = "ledger " + strings.Join(keyParts(rng), "\n")
		}
		limit := rng.Uint64()
		if g, w := call(linked.KeyTestResult(ctx, id, ledger, limit, parts, environ, skip)), TestResultKey(id, ledger, limit, parts, environ, skip); g != w {
			t.Fatalf("seed %d case %d: test result key: %q, want %q\nid %q ledger %q limit %d args %q environ %q\n%s", seed, c, g, w, id, ledger, limit, parts, environ, replay)
		}
	}
	if kept == 0 {
		t.Fatalf("seed %d: no environment entry was kept: the filter is not reached\n%s", seed, replay)
	}
}

// keyPieces are what a part is made of: the bytes a length-prefixed
// framing must not confuse.
var keyPieces = []string{"", "a", "1", "12", ":", "1:", "\x00", "é", "𝄞", " ", "go1.27"}

func keyParts(rng *rand.Rand) []string {
	var parts []string
	for range rng.IntN(5) {
		var b strings.Builder
		for range rng.IntN(4) {
			b.WriteString(keyPieces[rng.IntN(len(keyPieces))])
		}
		parts = append(parts, b.String())
	}
	return parts
}

func keyEnviron(rng *rand.Rand, skip string) []string {
	names := []string{"GOFLAGS", "GOOS", "GO", "CGO_ENABLED", "CGO", "ERGO_GO", "ERGO_", "HOME", "PATH", "XGO", skip, skip + "X", "go"}
	var env []string
	for range rng.IntN(7) {
		env = append(env, names[rng.IntN(len(names))]+"="+keyPieces[rng.IntN(len(keyPieces))])
	}
	return env
}

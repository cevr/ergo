package sketchfront

import (
	"context"
	"io/fs"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// TestWarmKeyAgreesWithErgo holds the warm answer's key and folder digest
// written in Ergo (compiler/cachekey's WarmKey and ListingDigest, linked as
// internal/linked/compiler) to warmKeyOf and listingDigest, the reference
// until the S5 swap, on seeded requests: parts with the bytes a framing
// could confuse, compilers with and without an identity (empty,
// "process:"), environments holding build variables, others, the stage
// report and ERGO_BUILD, experiment lists, and folder listings in any
// order with any type bits. A failing case prints its seed and the replay
// command.
//
// Mutants it alone catches: internal/selfhostcheck/testdata/mutants.json,
// killer internal/sketchfront:TestWarmKeyAgreesWithErgo.
func TestWarmKeyAgreesWithErgo(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SELFHOST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	replay := "replay: ERGO_SELFHOST_SEED=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestWarmKeyAgreesWithErgo$' ./internal/sketchfront"
	rng := rand.New(rand.NewPCG(seed, 0x3a5))
	ctx := context.Background()
	pieces := []string{"", "a", "1", "12", ":", "1:", "\x00", "é", "𝄞", "build"}
	text := func() string {
		var b strings.Builder
		for range rng.IntN(4) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		return b.String()
	}
	list := func() []string {
		var xs []string
		for range rng.IntN(4) {
			xs = append(xs, text())
		}
		return xs
	}
	compilers := []string{"", "process:42", "sha256:ab", "ergo 1"}
	names := []string{"GOFLAGS", "GOOS", "CGO_ENABLED", "ERGO_GO", "HOME", "PATH", StageReportVariable, WarmVariable, WarmVariable + "X"}
	keyed := 0
	for c := 0; c < 300; c++ {
		compiler := compilers[rng.IntN(len(compilers))]
		parts, experiments := list(), list()
		var environ []string
		for range rng.IntN(6) {
			environ = append(environ, names[rng.IntN(len(names))]+"="+text())
		}
		want := warmKeyOf(compiler, parts, environ, experiments)
		if want != "" {
			keyed++
		}
		got, err := linked.KeyWarm(ctx, compiler, parts, environ, experiments)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("seed %d case %d: warm key: %q, want %q\ncompiler %q parts %q environ %q experiments %q\n%s", seed, c, got, want, compiler, parts, environ, experiments, replay)
		}
		var entries []fs.DirEntry
		var entryNames []string
		var types []int
		modes := []fs.FileMode{0, fs.ModeDir, fs.ModeSymlink, fs.ModeNamedPipe}
		for range rng.IntN(5) {
			e := listedEntry{name: text(), mode: modes[rng.IntN(len(modes))]}
			entries = append(entries, e)
			entryNames, types = append(entryNames, e.name), append(types, int(e.mode))
		}
		got, err = linked.KeyListing(ctx, entryNames, types)
		if err != nil {
			t.Fatal(err)
		}
		if w := listingDigest(entries); got != w {
			t.Fatalf("seed %d case %d: listing digest of %q %v: %s, want %s\n%s", seed, c, entryNames, types, got, w, replay)
		}
	}
	if keyed == 0 {
		t.Fatalf("seed %d: no request had a key\n%s", seed, replay)
	}
}

// listedEntry is a folder entry of a name and type bits.
type listedEntry struct {
	name string
	mode fs.FileMode
}

func (e listedEntry) Name() string               { return e.name }
func (e listedEntry) IsDir() bool                { return e.mode.IsDir() }
func (e listedEntry) Type() fs.FileMode          { return e.mode }
func (e listedEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

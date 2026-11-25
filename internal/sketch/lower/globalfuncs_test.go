package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

var (
	reGoCol     = regexp.MustCompile(`Column: \d+`)
	reIRPosCol  = regexp.MustCompile(`"(pos|end|epos|eend)":\[(\d+),(\d+),\d+\]`)
	reIRShaLine = regexp.MustCompile(`(?m)^// ergoir\.json sha256 [0-9a-f]+$`)
)

func normalizeIRCols(s string) string {
	s = reGoCol.ReplaceAllString(s, "Column: 0")
	s = reIRPosCol.ReplaceAllString(s, `"$1":[$2,$3,0]`)
	s = reIRShaLine.ReplaceAllString(s, "// ergoir.json sha256 <normalized>")
	return s
}

func firstDiffLines(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out strings.Builder
	shown := 0
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			fmt.Fprintf(&out, "line %d:\n  old: %s\n  new: %s\n", i+1, x, y)
			shown++
			if shown >= 8 {
				break
			}
		}
	}
	return out.String()
}

// TestGlobalFuncsIREqual verifies NO-PRELUDE Unit 5a: calling the qualified
// std-package forms (maps.Hash, codec.Encode, codec.Decode, scope.InForce,
// fstest.TestFolder, fstest.TestFolderHandle, time.NewLiveScheduler) produces
// byte-equal IR to their transitional universe/prelude spellings with zero
// fallbacks, and proof.AsWritten / slices.WithCapacity / maps.Using lower
// cleanly on IR with zero fallbacks.
func TestGlobalFuncsIREqual(t *testing.T) {
	irPayload := func(t *testing.T, src string) map[string]string {
		t.Helper()
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, src)
		}
		pkg, diags := check.Check("main", []*syntax.File{f}, check.Std)
		if len(diags) > 0 {
			t.Fatalf("check: %v\n%s", diags, src)
		}
		u := Unit{
			Package:    pkg,
			Files:      []*syntax.File{f},
			Sources:    []Source{{Path: testSourcePath, Text: src}},
			NoFallback: true,
		}
		files, rep, err := LowerIR(u)
		if err != nil || len(rep.Fallback) > 0 {
			t.Fatalf("LowerIR(NoFallback=true): err=%v fallback=%v\n%s", err, rep.Fallback, src)
		}
		out := map[string]string{}
		for _, lf := range files {
			if lf.Path == "go.mod" {
				continue
			}
			out[lf.Path] = string(lf.Data)
		}
		return out
	}

	t.Run("hash encode decode byte-equal IR", func(t *testing.T) {
		// Both programs keep the same imports so emitted package tables match
		// byte-for-byte; only the calls differ between global and std-qualified.
		const oldSrc = `import codec
import maps

type Pair { Key string, Val int }

func run(p Pair) uint64 {
	let ev = codec.NewEvents()
	encode(p, ev)
	let d = decode[Pair](codec.NewReplay(ev.Recorded())) catch {
	case WireWrongKind, WireMissingField:
		Pair{Key: "", Val: 0}
	}
	let _ = maps.Clone(Map[string, int]{"a": 1})
	hash(d)
}

func main() {
	let _ = run(Pair{Key: "k", Val: 1})
}
`
		const newSrc = `import codec
import maps

type Pair { Key string, Val int }

func run(p Pair) uint64 {
	let ev = codec.NewEvents()
	codec.Encode(p, ev)
	let d = codec.Decode[Pair](codec.NewReplay(ev.Recorded())) catch {
	case WireWrongKind, WireMissingField:
		Pair{Key: "", Val: 0}
	}
	let _ = maps.Clone(Map[string, int]{"a": 1})
	maps.Hash(d)
}

func main() {
	let _ = run(Pair{Key: "k", Val: 1})
}
`
		oldFiles := irPayload(t, oldSrc)
		newFiles := irPayload(t, newSrc)
		if len(oldFiles) != len(newFiles) {
			t.Fatalf("emitted file count differs: old=%d new=%d", len(oldFiles), len(newFiles))
		}
		for path, want := range oldFiles {
			if got := normalizeIRCols(newFiles[path]); got != normalizeIRCols(want) {
				t.Fatalf("IR output %s differs between global and std-qualified calls:\n%s", path, firstDiffLines(normalizeIRCols(want), got))
			}
		}
	})

	t.Run("scope.InForce time.NewLiveScheduler fstest.TestFolder byte-equal IR", func(t *testing.T) {
		const oldSrc = `import fstest
import scope
import time

type Greet { Say() string }

type Plain {} implements Greet

func Say(_ Plain) string { "hi" }

func capCtx() Context[Greet] needs Greet {
	scope.OnExit() {}
	InForce[Greet]()
}

func sched() LiveScheduler needs Ambient {
	let _ = time.NewManual(0)
	NewLiveScheduler()
}

func folder() string needs TestAmbient {
	let _ = fstest.NewFolder()
	let h = TestFolderHandle()
	TestFolder() + h.Name()
}

func main() {
	let c = with context { Greet: Plain } { capCtx() }
	let _ = with c { Greet.Say() }
}
`
		const newSrc = `import fstest
import scope
import time

type Greet { Say() string }

type Plain {} implements Greet

func Say(_ Plain) string { "hi" }

func capCtx() Context[Greet] needs Greet {
	scope.OnExit() {}
	scope.InForce[Greet]()
}

func sched() LiveScheduler needs Ambient {
	let _ = time.NewManual(0)
	time.NewLiveScheduler()
}

func folder() string needs TestAmbient {
	let _ = fstest.NewFolder()
	let h = fstest.TestFolderHandle()
	fstest.TestFolder() + h.Name()
}

func main() {
	let c = with context { Greet: Plain } { capCtx() }
	let _ = with c { Greet.Say() }
}
`
		oldFiles := irPayload(t, oldSrc)
		newFiles := irPayload(t, newSrc)
		if len(oldFiles) != len(newFiles) {
			t.Fatalf("emitted file count differs: old=%d new=%d", len(oldFiles), len(newFiles))
		}
		for path, want := range oldFiles {
			if got := normalizeIRCols(newFiles[path]); got != normalizeIRCols(want) {
				t.Fatalf("IR output %s differs between global and std-qualified calls:\n%s", path, firstDiffLines(normalizeIRCols(want), got))
			}
		}
	})

	t.Run("proof.AsWritten slices.WithCapacity maps.Using zero fallbacks", func(t *testing.T) {
		const src = `import maps
import proof
import slices

type KeyHash {} implements Hashing[string]

func Hash(_ KeyHash, k string) uint64 { maps.Hash(k) }

func Equal(_ KeyHash, a string, b string) bool { a == b }

func main() {
	let xs = slices.WithCapacity[int](4).Append(1)
	let mut m = maps.Using[string, int, KeyHash](KeyHash)
	m["a"] = proof.AsWritten[int](xs[0])
}
`
		_ = irPayload(t, src)
	})
}

package lower

import (
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

const tomlProbe = `import json
import strings
import toml
import utf8

type sample { Name string, Count int64, Active bool, Steps []int64 }

test ReadsGeneratedRecord(name string, count int64, active bool, steps []int64) {
	if !utf8.ValidString(name) { return }
	let sc = json.Codec[string]{MaxBytes: 0, MaxDepth: 0}
	let nc = json.Codec[int64]{MaxBytes: 0, MaxDepth: 0}
	let bc = json.Codec[bool]{MaxBytes: 0, MaxDepth: 0}
	let lc = json.Codec[[]int64]{MaxBytes: 0, MaxDepth: 0}
	let quoted = strings.ReplaceAll(json.Encode(sc, name), "\x7f", "\\u007f")
	let text = "name = " + quoted + "\ncount = " + json.Encode(nc, count) + "\nactive = " + json.Encode(bc, active) + "\nsteps = " + json.Encode(lc, steps) + "\n"
	let value = sample{Name: name, Count: count, Active: active, Steps: steps}
	let back = toml.Decode(json.Codec[sample]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		assert false
		value
	}
	assert back == value
}

// A refinement has the very same failure through TOML as through JSON.
type port int where 1 <= it && it <= 65535
type listener { Port port }
test RefinedConfigKeepsItsRow(n int) {
	let p = if n < 0 { -(n % 65535) + 1 } else { n % 65535 + 1 }
	let text = "port = " + json.Encode(json.Codec[int]{MaxBytes: 0, MaxDepth: 0}, p)
	let c = json.Codec[listener]{MaxBytes: 0, MaxDepth: 0}
	let back = toml.Decode(c, text) catch { case _: assert false; return }
	assert int(back.Port) == p
	let _ = toml.Decode(c, "port = 0") catch {
	case Unsatisfied:
		return
	case _:
		assert false
		return
	}
	assert false
}
`

var tomlLawStatements = map[string]string{
	"toml ReadsGeneratedRecord": `ReadsGeneratedRecord(name string, count int64, active bool, steps []int64) { if !utf8.ValidString(name) { return true } let sc = json.Codec[string]{MaxBytes: 0, MaxDepth: 0} let nc = json.Codec[int64]{MaxBytes: 0, MaxDepth: 0} let bc = json.Codec[bool]{MaxBytes: 0, MaxDepth: 0} let lc = json.Codec[[]int64]{MaxBytes: 0, MaxDepth: 0} let quoted = strings.ReplaceAll(json.Encode(sc, name), "\x7f", "\\u007f") let text = "name = " + quoted + "\ncount = " + json.Encode(nc, count) + "\nactive = " + json.Encode(bc, active) + "\nsteps = " + json.Encode(lc, steps) + "\n" let back = Decode(json.Codec[sample]{MaxBytes: 0, MaxDepth: 0}, text) catch { case _: return false } back == sample{Name: name, Count: count, Active: active, Steps: steps} }`,
}

// TestTOMLDecode runs the public derived decoder's seeded record law.
// The corpus tests the parser but cannot kill the std Decode-discarding-
// its-input mutant; this test does. The refinement property also checks
// that the shared decoder's contract is preserved. Replay the printed
// Ergo test seed with ERGO_TOML_SEED. Failure prints the source and output.
//
// It builds Go because the law/property runner executes only in the
// generated test binary; go/types cannot observe decoded record values.
func TestTOMLDecode(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "tomllaws", tomlProbe)}, true)
	if exes == nil {
		return
	}
	seed := "1"
	if s := os.Getenv("ERGO_TOML_SEED"); s != "" {
		seed = s
	}
	out, errout, code := runExeArgs(t, exes["tomllaws"], "--seed", seed)
	if code != 0 {
		t.Fatalf("source:\n%s\nseed %s exit %d\n%s\n%s\nreplay: ERGO_TOML_SEED=%s go test -run '^TestTOMLDecode$' ./internal/sketch/lower", tomlProbe, seed, code, out, errout, seed)
	}
	lawledger.Run(t, lawsPassed(t, tomlLawStatements, func(key string) string { return strings.TrimPrefix(key, "toml ") }, tomlProbe, out, "RefinedConfigKeepsItsRow"))
}

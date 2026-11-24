package irspike

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWireProjectionAgrees builds Go because it runs a projected
// witness across a user forwarding call, a decode, and a typed rejection.
// Mutant wire-project-stops-forwarding leaves that call's witness whole.
func TestIRWireProjectionAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := wireProjectionProgram(seed)
		dir := t.TempDir()
		u := loadText(t, dir, text)
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		var forward bool
		for _, name := range rep.IR {
			if strings.HasPrefix(name, "forward") && strings.Contains(name, "WitnessField") {
				forward = true
			}
			if strings.HasPrefix(name, "round") && strings.Contains(name, "WitnessField") {
				t.Fatalf("seed %d: a body using encode and decode did not keep both witness fields", seed)
			}
		}
		if !forward {
			t.Fatalf("seed %d: no projected user forwarding frame; IR %v\n%s", seed, rep.IR, text)
		}
		ref := filepath.Join(dir, "ref")
		func() {
			defer lower.SetWireProjectionForTest("off")()
			if _, err := Emit(u, IR, ref); err != nil {
				t.Fatal(err)
			}
		}()
		rb := filepath.Join(dir, "ref.bin")
		if _, err := Build(ir, IR, ref, rb, BuildOptions{}); err != nil {
			t.Fatal(err)
		}
		got, old, source := run(t, ib), run(t, rb), run(t, sb)
		if got != old || got != source || got.stdout != fmt.Sprintf("%d %d -1\n", seed, seed) {
			t.Fatalf("seed %d: native %+v, reference %+v, source %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRWireProjectionAgrees$' ./internal/experimental/irspike\n%s", seed, got, old, source, seed, text)
		}
	}
}

func wireProjectionProgram(seed uint64) string {
	return fmt.Sprintf(`
import json
import rpc

func forward[T wire](s string) T fails rpc.Transport needs wired[T] {
	rpc.Decode[T]("value", s)
}

func round[T wire](v T) T fails rpc.Transport needs wired[T] {
	let text = json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, v)
	forward[T](text)
}

func main() needs Console {
	let a = forward[int]("%[1]d") catch { case rpc.Transport: -2 }
	let b = round(%[1]d) catch { case rpc.Transport: -3 }
	let c = forward[int]("not-a-value") catch { case rpc.Transport: -1 }
	Console.Println("\(a) \(b) \(c)")
}
`, seed)
}

package lower

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every documented refusal needs a checker-admitted program that still
// reaches it. Retired entries fail here, including Interface:any (C10).
// Replay source programs independently of which named tests are selected.
func TestRefusedFamiliesHaveWitnesses(t *testing.T) {
	var sources []string
	for _, dir := range []string{"../check", "."} {
		ss, _, total, stride := checkCorpus(t, dir, 10000)
		if stride != 1 {
			t.Fatalf("refusal domain was sampled in %s (%d candidates); increase its bound", dir, total)
		}
		sources = append(sources, ss...)
	}
	for _, root := range []string{"../check/testdata", "../testdata", "../../../examples", "testdata"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".ergo") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err == nil {
				sources = append(sources, string(data))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Generated/import-dependent programs are not all literal candidates.
	sources = append(sources,
		`type A { Use() }
type Conn[T] { V T }
func Use[T](c Conn[T]) {}
func Close[T](c Conn[T]) {}
func Get() Conn[int] { Conn[int]{V: 1} }
let W = context { A: Get() }
func main() { with W { A.Use() } }
`,
		`type Tag[T] { V T }
func contextKey[T](t Tag[T]) string { "tag" }
func contextText[T](t Tag[T]) string { "v" }
func main() { with (Tag{V: 1}) { } }
`,
		// An error type with a Map field: its text is still refused (a
		// generic error type's text lowers per instance, lower-v1 stage 4).
		`type Fault { F Map[string, int] }
func main() fails Fault { return Fault{F: Map[string, int]{}} }
`,
		// An indexed slice literal: the checker admits it, and its keys
		// wait for #196.
		`func main() needs Console {
	let xs = []int{2: 5}
	Console.Println("\(len(xs))")
}
`,
	)
	seen, met := map[string]bool{}, map[string]int{}
	for _, src := range sources {
		if seen[src] {
			continue
		}
		seen[src] = true
		o := lowerSource("refusal", src, false)
		if o.file == nil || len(o.diags) != 0 {
			continue
		}
		if o.panicked != nil || o.err != nil {
			t.Errorf("admitted source failed lowering: %v %v\n%s", o.panicked, o.err, src)
			continue
		}
		for _, r := range o.refused {
			family := refusalFamily(r.Message)
			if family == "" {
				continue
			} // classification has its own corpus check
			met[family]++
		}
	}
	for family := range refusedFamilies {
		if met[family] == 0 {
			t.Errorf("stale refused family %s: no admitted source reaches it", family)
		}
	}
	t.Logf("observed refusals: %v", met)
	for _, reason := range refusalReasons {
		if refusedFamilies[reason.family] == "" {
			t.Errorf("undocumented refusal matcher %s", reason.family)
		}
	}
}

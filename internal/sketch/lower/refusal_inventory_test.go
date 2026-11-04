package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"errors"
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
	for _, root := range []string{"../check/testdata", "../proto/testdata", "../../../examples", "testdata"} {
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
	for _, r := range importedContextRefusalWitness(t) {
		met[refusalFamily(r.Message)]++
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

// An imported context needs two checked packages; a source-string replay
// cannot supply its importer. This is the same public seam as the
// runtime199 imported-context regression, with the observed refusal returned.
func importedContextRefusalWitness(t *testing.T) []Refusal {
	t.Helper()
	lib, err := syntax.Parse(`type A { Use() }
type Conn { n int }
func Use(_ Conn) {}
func Close(_ Conn) {}
func Get() Conn { Conn{n: 1} }
let W = context { A: Get() }
`)
	if err != nil {
		t.Fatal(err)
	}
	makers, ds := check.Check("makers", []*syntax.File{lib}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	const src = "import makers\nfunc main() { with makers.W { makers.A.Use() } }\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{file}, contextImporter{makers})
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	_, err = lowerPackage(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, false)
	var refused *Refused
	if !errors.As(err, &refused) || len(refused.Refusals) != 1 {
		t.Fatalf("want one imported-context refusal, got %v", err)
	}
	return refused.Refusals
}

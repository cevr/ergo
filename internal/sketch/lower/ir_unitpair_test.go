package lower

import (
	"encoding/json"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestIRDropErrUnitPair holds that a statement call of a generic fallible
// Ergo function whose type-parameter result instantiates to Unit and whose
// failure row is empty (such as sync.With(s) { ... }) binds and drops the
// Go (struct{}, error) pair with def2 instead of emitting a bare expr
// statement (which the IR decoder refuses: "a fallible call's error is dropped").
func TestIRDropErrUnitPair(t *testing.T) {
	const src = `import sync

func run(s sync.Semaphore, r sync.Ref[int]) {
	sync.With(s) {
		r.Set(1)
	}
}

func main() {
	let s = sync.NewSemaphore(1)
	let r = sync.NewRef(0)
	run(s, r)
}
`
	o := mustLower(t, "unitpair", src)
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	foundRun := false
	foundDef2 := false
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			if fn.Name != "run" {
				continue
			}
			foundRun = true
			for _, st := range fn.Body {
				if st.K == "expr" && st.X != nil && strings.Contains(st.X.Name, "With") {
					t.Fatalf("run emits bare expr statement for unit-pair fallible call %q; want def2", st.X.Name)
				}
				if st.K == "def2" && st.X != nil && strings.Contains(st.X.Name, "With") {
					foundDef2 = true
				}
			}
		}
	}
	if !foundRun {
		t.Fatal("run has no IR body")
	}
	if !foundDef2 {
		t.Fatal("run did not bind sync.With unit-pair call with def2")
	}
}

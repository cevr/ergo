package lower

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// TestRetainedForeignArguments extends ESC1's clause property with the
// two r2 repros: bufio keeps a lent file; timeout runs an outside Set on
// the wrong task. The reference policy permits only a reviewed resource
// lend or an owned hand-off. Draws vary sizes and nested non-escaping
// frames; each seed reaches all policies, including a synthetic unknown
// module with an old (unannotated) binding. No Go build is needed to
// observe the checked lifetime. Mutants: omit hostClauses' default take;
// revert timeout.After.f to non-escaping; ignore a host receiver's take;
// read a recursive Go interface's provisional underlying while resolving.
func TestRetainedForeignArguments(t *testing.T) {
	for _, seedText := range strings.Split(*clauseSeeds, ",") {
		seed, err := strconv.ParseUint(strings.TrimSpace(seedText), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		r := rand.New(rand.NewPCG(seed, 197))
		size := 1 + r.IntN(4096)
		for _, tc := range []struct {
			name, imports, params, row, body string
			allowed, module                  bool
		}{
			{"bufio-lent", "import go bufio\nimport go os", "f go.Ref[os.File]", "needs go.Foreign", fmt.Sprintf("let _ = bufio.NewReaderSize(f, %d)", size), false, false},
			{"bufio-owned", "import go bufio\nimport go os", "f go.Ref[os.File]", "needs go.Foreign takes f", "let _ = bufio.NewReader(f)", true, false},
			{"bufio-after-take", "import go bufio\nimport go os", "f go.Ref[os.File]", "needs go.Foreign takes f", "let _ = bufio.NewReader(f)\nlet _ = f.Name()", false, false},
			{"reviewed-lend", "import go io\nimport go os", "f go.Ref[os.File]", "fails go.Error needs go.Foreign", "let _ = try io.ReadAll(f)\nlet _ = f.Name()", true, false},
			{"unknown-lent", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign", "hostres.Unknown(f)", false, true},
			{"empty-reason-lent", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign", "hostres.Unreasoned(f)", false, true},
			{"unknown-owned", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign takes f", "hostres.Unknown(f)", true, true},
			{"unknown-after-take", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign takes f", "hostres.Unknown(f)\nf.Use()", false, true},
			{"receiver-lent", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign", "f.Hold()", false, true},
			{"receiver-after-take", "import go hostres", "f go.Ref[hostres.Handle]", "needs go.Foreign takes f", "f.Hold()\nf.Use()", false, true},
			{"interface-receiver-lent", "import go hostres", "f hostres.Retainer", "needs go.Foreign", "f.Hold()", false, true},
			{"recursive-interface-lent", "import go hostres", "f hostres.Recursive, g hostres.Recursive", "needs go.Foreign takes f", "f.Chain(g)", false, true},
			{"unknown-callback-clause", "import go hostres", "", "needs go.Foreign", "hostres.Callback(func() {})", false, true},
			{"unknown-pointer-borrow", "import go hostres", "f *hostres.Handle", "needs go.Foreign", "hostres.Pointer(f)", false, true},
			{"unknown-receiver-borrow", "import go hostres", "f *hostres.Handle", "needs go.Foreign", "f.Hold()", false, true},
			{"reviewed-receiver-borrow", "import go hostres", "f *hostres.Handle", "needs go.Foreign", "f.Use()", true, true},
			{"timeout-set", "import tasks\nimport timeout\ntype NoFail {}", "", "needs Scheduler", "let set = tasks.NewSet[NoFail](tasks.FailFast)\nlet _ = timeout.After(1s) { set.Len() } catch { case timeout.Expired: 0 }", false, false},
			{"timeout-local-set", "import tasks\nimport timeout\ntype NoFail {}", "", "needs Scheduler", "let _ = timeout.After(1s) { let set = tasks.NewSet[NoFail](tasks.FailFast); set.Len() } catch { case timeout.Expired: 0 }", true, false},
		} {
			t.Run(fmt.Sprintf("%d/%s", seed, tc.name), func(t *testing.T) {
				body := tc.body
				// A borrowed incoming resource must stay borrowed through local
				// frames too. Owned hand-offs stay in the owning frame.
				if !strings.Contains(tc.row, "takes") {
					for range r.IntN(4) {
						body = "func() {\n" + body + "\n}()"
					}
				}
				src := tc.imports + "\nfunc Use(" + tc.params + ") " + tc.row + " {\n" + body + "\n}\n"
				var o *lowered
				if tc.module {
					o = lowerSourceWith(tc.name, src, false, newHresImporterOf(t, true))
				} else {
					o = lowerSource(tc.name, src, false)
				}
				refused := false
				for _, d := range o.diags {
					if d.Code == "S108" && (strings.Contains(d.Message, "lent") || strings.Contains(d.Message, "borrow") || strings.Contains(d.Message, "resource") || strings.Contains(d.Message, "handed off") || strings.Contains(d.Message, "non-escaping Go callback")) {
						refused = true
					} else {
						t.Errorf("unexpected diagnostic: %v", d)
					}
				}
				if o.err != nil || o.panicked != nil || len(o.refused) > 0 || tc.allowed && len(o.diags) > 0 || !tc.allowed && !refused {
					t.Errorf("allowed=%t, diagnostics=%v, error=%v, panic=%v, lowering=%v\n%s\nreplay: go test -run TestRetainedForeignArguments ./internal/sketch/lower -args -clauses.seeds=%d", tc.allowed, o.diags, o.err, o.panicked, o.refused, src, seed)
				}
			})
		}
	}
}

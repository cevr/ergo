package lower

import (
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestSelfType builds Go because the checked copies must preserve the values
// of three different providers, including Result's unchanged error argument.
func TestSelfType(t *testing.T) {
	src, err := os.ReadFile("../../selfhostcheck/testdata/agree/self_type/main.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "selftype", string(src))
	for _, inst := range o.pkg.Info.Instances {
		if inst.Func == nil || inst.Func.Name != "Lengths" {
			continue
		}
		bound := map[*check.TypeParam]check.Type{inst.Func.Sig.TParams[0]: inst.TypeArgs[0]}
		facts, why := check.CopyInfo(o.pkg.Info, inst.Func.Decl, check.CopyReader(o.pkg, bound))
		if why != "" {
			t.Fatal(why)
		}
		for _, typ := range facts.Types {
			if text := typ.String(); strings.Contains(text, "M[") || strings.Contains(text, "Self") {
				t.Errorf("copy for %v still holds symbolic %s", inst.TypeArgs, text)
			}
		}
	}
	typecheckGo(t, []*lowered{o})
	needsBuild(t)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["selftype"])
	if want := "3 true 5 4\n"; code != 0 || stdout != want {
		t.Errorf("exit %d, stdout %q, want %q; stderr:\n%s", code, stdout, want, firstLines(stderr, 8))
	}
}

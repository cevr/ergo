package check_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestReleaseNeedRowMessages (check-v1 E5, C35; resources.md, Context
// ownership): a release runs outside its with, so its need widens no
// inferred row. An owning with whose provider's Close needs Log, in a
// function no row or outer with gives Log, is S102 at the with, with the
// inferred-row text when the function writes no row and the written-row
// text when it writes one. Each holds for a named, an inline and a local
// context, so a local context's release is charged at the with that builds
// it, never at its let.
func TestReleaseNeedRowMessages(t *testing.T) {
	const closes = `type Res {}
func Use(_ Res) {}
func Close(_ Res) needs Log { Log.Say("closed") }
let W = context { A: Res }
`
	const (
		inferred = "the release of main.Res when this with ends requires main.Log, which is not in the inferred capability row of function g and no outer with provides it; a release runs outside the with and widens no inferred row, so write the row of function g (resources.md, Context ownership)"
		written  = "the release of main.Res when this with ends requires main.Log, which is not in the capability row of function g and no outer with provides it; a release runs outside the with (resources.md, Context ownership)"
	)
	operands := []struct{ name, setup, op string }{
		{"named", "", "W"},
		{"inline", "", "context { A: Res }"},
		{"local", "\tlet w = context { A: Res }\n", "w"},
	}
	rows := []struct{ name, row, want string }{
		{"no written row", "", inferred},
		{"a written row", " needs A", written},
	}
	for _, o := range operands {
		for _, r := range rows {
			t.Run(o.name+"/"+r.name, func(t *testing.T) {
				src := closes + "func g()" + r.row + " {\n" + o.setup + "\twith " + o.op + " { A.Use() }\n}\n"
				_, ds := checkOwn(t, src)
				line := strings.Count(ownPrelude+src, "\n") - 1
				var got []string
				for _, d := range ds {
					got = append(got, fmt.Sprintf("%s@%d %s", d.Code, d.Line, d.Message))
				}
				want := fmt.Sprintf("S102@%d %s", line, r.want)
				if strings.Join(got, "\n") != want {
					t.Errorf("diagnostics:\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), want)
				}
			})
		}
	}
	// Control: Log in the written row, or an outer with that binds it,
	// holds the release need.
	for _, src := range []string{
		closes + "func g() needs A | Log {\n\tlet w = context { A: Res }\n\twith w { A.Use() }\n}\n",
		closes + "func g() {\n\tlet w = context { A: Res }\n\twith context { Log: Quiet } {\n\t\twith w { A.Use() }\n\t}\n}\n",
	} {
		if _, ds := checkOwn(t, src); len(ds) > 0 {
			t.Errorf("diagnostics %v in\n%s", ds, src)
		}
	}
}

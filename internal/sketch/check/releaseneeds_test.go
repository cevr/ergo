package check

import (
	"strings"
	"testing"
)

// TestReleaseNeedRows (resources R5; releaseneeds.go) holds the text and
// position of the release-need S102, and the two smallest repros of the
// bug it fixes (OWGEN r3, Known gaps): a function that takes a resource
// whose Close needs Console, called from a main with no needs Console,
// built and then panicked with a nil Console in the Close. Exported, the
// taking function must write the row (S102 at the taken parameter);
// unexported, its row is inferred and main's call is charged. The third
// row is the result a std call releases when its boundary drops it
// (Info.ResultDrops: timeout.After's late result), charged at the call.
// The fourth is the field a record update replaces (Info.Replaced),
// charged at the update's base.
//
// Mutants it kills that TestReleaseNeedsModel does not (ledger:
// releaseneeds.go): the release-need text without the Close, the type or
// the capability; the result a std call releases not charged; a replaced
// field not charged.
func TestReleaseNeedRows(t *testing.T) {
	const res = "type Res { id int }\n\nfunc Close(r Res) needs Console {\n\tConsole.Println(\"close \\(r.id)\")\n}\n\n"
	for _, c := range []struct{ name, src, want string }{
		{
			"exported taker",
			res + "func Drop(x Res) takes x {}\n\nfunc main() {\n\tDrop(Res{id: 1})\n}\n",
			"7:11: S102 Res.Close, run by the release of Res here, requires Console, which is not in the capability row of function Drop and no with provides it",
		},
		{
			"inferred taker",
			res + "func drop(x Res) takes x {}\n\nfunc main() {\n\tdrop(Res{id: 1})\n}\n",
			"10:2: S102 this call requires Console, which is not in the capability row of function main and no with provides it",
		},
		{
			"released result",
			"import timeout\n\ntype A { Use() }\ntype RA { id int }\nfunc Close(r RA) needs A { A.Use() }\n\nfunc G() RA fails timeout.Expired needs Scheduler {\n\ttry timeout.After(1s) { RA{id: 1} }\n}\n\nfunc main() {}\n",
			"8:6: S102 RA.Close, run by the release of RA here, requires A, which is not in the capability row of function G and no with provides it",
		},
		{
			"replaced field",
			"type B { Use() }\ntype RB { id int }\nfunc Close(r RB) needs B { B.Use() }\ntype Box { r RB, n int }\n\nfunc G() {\n\tlet b = Box{r: RB{id: 1}, n: 1}\n\tlet c = Box{..b, r: RB{id: 2}}\n}\n\nfunc main() {}\n",
			"7:6: S102 RB.Close, run by the release of Box here, requires B, which is not in the capability row of function G and no with provides it\n" +
				"8:6: S102 RB.Close, run by the release of Box here, requires B, which is not in the capability row of function G and no with provides it\n" +
				"8:16: S102 RB.Close, run by the release of RB here, requires B, which is not in the capability row of function G and no with provides it",
		},
	} {
		_, ds := checkFor(c.src)
		var got []string
		for _, d := range ds {
			got = append(got, d.String())
		}
		if strings.Join(got, "\n") != c.want {
			t.Errorf("%s:\n%s\ngot:\n\t%s\nwant:\n\t%s", c.name, ofNumbered(c.src), strings.Join(got, "\n\t"), c.want)
		}
	}
}

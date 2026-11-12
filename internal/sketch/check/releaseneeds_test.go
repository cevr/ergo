package check

import (
	"strings"
	"testing"
)

// TestReleaseNeedRows (resources R5; releaseneeds.go) holds the text and
// position of the release-need S102, with the clause to write, and the
// smallest repro of the bug it fixes (OWGEN r3, Known gaps): main, whose
// row is empty, called a function that takes a resource whose Close
// needs Console; the program checked, built and then panicked with a nil
// Console in the Close. The taking function must write the row (S102 at
// the taken parameter). The second row is the result a std call releases
// when its boundary drops it (Info.ResultDrops: timeout.After's late
// result), charged at the call. The third is the field a record update
// replaces (Info.Replaced), charged at the update's base. The fourth is
// the release of a with's provider through a field (Context ownership 6),
// charged at the with and named by the provider's type. The next two are
// a with release that needs an instance of this package's generic
// capability, of a named argument and of composite ones, which the second
// check names again (withown.go seedType) and ergo fix spells (clausefix.go
// spell): a clause it cannot spell is marked. The last two
// are the codec refusals (types.md, Codecs 8): a wire type parameter
// bound to a type whose codec needs a capability, and, in the clause
// writer's inference (Options.Infer), a decode in a package let that
// reaches an inferred ParseX that needs one.
//
// Mutants it kills that TestReleaseNeedsModel does not (ledger:
// releaseneeds.go, withown.go, clausefix.go): the release-need text
// without the Close, the type or the capability, or without its subject
// once the clause to write is added; a with release named by its part's
// type; a generic capability instance, or a composite argument of one, not
// named again or not spelled; the result a std call releases not charged; a replaced
// field not charged; a codec instance whose codec needs a capability, by a
// written or an inferred row, not charged at the call (needs wired[T],
// q141); a decode in a package let that reaches an inferred ParseX
// admitted.
func TestReleaseNeedRows(t *testing.T) {
	const res = "type Res { id int }\n\nfunc Close(r Res) needs Console {\n\tConsole.Println(\"close \\(r.id)\")\n}\n\n"
	for _, c := range []struct {
		name, src, want string
		infer           bool
	}{
		{
			name: "exported taker",
			src:  res + "func Drop(x Res) takes x {}\n\nfunc main() {\n\tDrop(Res{id: 1})\n}\n",
			want: "7:11: S102 Res.Close, run by the release of Res here, requires Console, which is not in the capability row of function Drop and no with provides it; write: func Drop(x Res) needs Console takes x",
		},
		{
			name: "released result",
			src:  "import timeout\n\ntype A { Use() }\ntype RA { id int }\nfunc Close(r RA) needs A { A.Use() }\n\nfunc G() RA fails timeout.Expired needs Scheduler {\n\ttry timeout.After(1s) { RA{id: 1} }\n}\n\nfunc main() {}\n",
			want: "8:6: S102 RA.Close, run by the release of RA here, requires A, which is not in the capability row of function G and no with provides it; write: func G() RA fails timeout.Expired needs Scheduler | A",
		},
		{
			name: "replaced field",
			src:  "type B { Use() }\ntype RB { id int }\nfunc Close(r RB) needs B { B.Use() }\ntype Box { r RB, n int }\n\nfunc G() {\n\tlet b = Box{r: RB{id: 1}, n: 1}\n\tlet c = Box{..b, r: RB{id: 2}}\n}\n\nfunc main() {}\n",
			want: "7:6: S102 RB.Close, run by the release of Box here, requires B, which is not in the capability row of function G and no with provides it; write: func G() needs B\n" +
				"8:6: S102 RB.Close, run by the release of Box here, requires B, which is not in the capability row of function G and no with provides it; write: func G() needs B\n" +
				"8:16: S102 RB.Close, run by the release of RB here, requires B, which is not in the capability row of function G and no with provides it; write: func G() needs B",
		},
		{
			name: "with provider part",
			src:  "type B { Use() }\ntype C { Use() }\ntype RB { id int }\nfunc Close(r RB) needs B { B.Use() }\ntype PcBox { r RB, id int }\nfunc Use(_ PcBox) {}\n\nfunc G() {\n\twith context { C: PcBox{r: RB{id: 1}, id: 1} } { C.Use() }\n}\n\nfunc main() {}\n",
			want: "9:2: S102 RB.Close, run by the release of PcBox when this with ends, requires B, which is not in the capability row of function G and no with provides it; write: func G() needs B",
		},
		{
			name: "with generic capability",
			src:  "type Log[T] { Put(v T) }\ntype Job { id int }\ntype C { Use() }\ntype RB { id int }\nfunc Close(r RB) needs Log[Job] { Log[Job].Put(Job{id: r.id}) }\nfunc Use(_ RB) {}\n\nfunc G() {\n\twith context { C: RB{id: 1} } { C.Use() }\n}\n\nfunc main() {}\n",
			want: "9:2: S102 RB.Close, run by the release of RB when this with ends, requires Log[Job], which is not in the capability row of function G and no with provides it; write: func G() needs Log[Job]",
		},
		{
			name: "with generic capability, composite arguments",
			src:  "type Tab[K, V] { Put(k K, v V) }\ntype Job { id int }\ntype C { Use() }\ntype RB { id int }\nfunc Close(r RB) needs Tab[Map[Job, [2]Job], []Job] {}\nfunc Use(_ RB) {}\n\nfunc G() {\n\twith context { C: RB{id: 1} } { C.Use() }\n}\n\nfunc main() {}\n",
			want: "9:2: S102 RB.Close, run by the release of RB when this with ends, requires Tab[Map[Job, [2]Job], []Job], which is not in the capability row of function G and no with provides it; write: func G() needs Tab[Map[Job, [2]Job], []Job]",
		},
		{
			name: "codec instance",
			src:  "import codec\n\ntype A { Use() }\ntype Bad { Text string }\ntype Lv { name string }\n\nfunc ParseLv(s string) Lv fails Bad { Lv{name: s} }\n\nfunc Wire(l Lv) string needs A {\n\tA.Use()\n\tl.name\n}\n\nfunc G() Lv fails wired[Lv] {\n\ttry codec.RoundTrip(Lv{name: \"x\"})\n}\n\nfunc main() {}\n",
			want: "15:6: S102 this call requires A, which is not in the capability row of function G and no with provides it; write: func G() Lv fails wired[Lv] needs A",
		},
		{
			name:  "inferred codec unordered",
			infer: true,
			src:   "import codec\n\ntype A { Use() }\ntype Bad { Text string }\ntype lw { name string }\n\nfunc Wire(l lw) string needs A { l.name }\n\nlet early = decode[lw](codec.NewReplay([]codec.Event{})) catch {\ncase _:\n\tlw{name: \"\"}\n}\n\nfunc G() lw fails wired[lw] {\n\ttry codec.RoundTrip(lw{name: \"y\"})\n}\n\nfunc parseLw(s string) lw fails Bad {\n\tA.Use()\n\tlw{name: s}\n}\n\nfunc main() {}\n",
			want: "9:13: S108 this decode reaches parseLw, whose inferred needs row is {A}, and its body has no function whose row could take it; write the row of parseLw (types.md, Codecs 8)\n" +
				"15:6: S102 this call requires A, which is not in the capability row of function G and no with provides it",
		},
	} {
		check := checkFor
		if c.infer {
			check = checkForInfer
		}
		_, ds := check(c.src)
		var got []string
		for _, d := range ds {
			s := d.String()
			if d.Fix != nil && d.Fix.Text == "" {
				// The clause the S102 names, which ergo fix cannot spell.
				s += " [ergo fix cannot write it]"
			}
			got = append(got, s)
		}
		if strings.Join(got, "\n") != c.want {
			t.Errorf("%s:\n%s\ngot:\n\t%s\nwant:\n\t%s", c.name, ofNumbered(c.src), strings.Join(got, "\n\t"), c.want)
		}
	}
}

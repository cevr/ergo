package unionreprbench

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"unionreprbench/internal/gensrc"
)

// The contract: every form computes the same results as form A on the
// generated domain, and every form D value keeps its inactive slots zero.

func TestGeneratedFilesCurrent(t *testing.T) {
	for name, want := range gensrc.Files() {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v (run go run ./gen)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what go run ./gen writes", name)
		}
	}
}

func breakerDomain() (states []Closed, opens []Open, signals []Failure) {
	for n := int64(0); n < 4; n++ {
		states = append(states, Closed{n})
	}
	opens = []Open{{"svc-a"}, {"svc-b"}}
	signals = []Failure{{}, {"svc-a"}, {"svc-b"}} // {} is Retry
	return
}

func TestBreakerFormsAgree(t *testing.T) {
	closeds, opens, raws := breakerDomain()
	type state struct {
		a StateA
		d StateD
		e StateE
		f StateF
	}
	var all []state
	for _, c := range closeds {
		all = append(all, state{c, ClosedD(c), StateE{stateClosed, c}, c})
	}
	for _, o := range opens {
		all = append(all, state{o, OpenD(o), StateE{stateOpen, o}, o})
	}
	sa, sd, se, sf := signalsA(raws), signalsD(raws), signalsE(raws), signalsF(raws)
	for _, s := range all {
		if !s.d.wellFormed() {
			t.Fatalf("%s: form D state is not well formed: %+v", textStateA(s.a), s.d)
		}
		for i := range raws {
			want := textDecisionA(stepA(s.a, sa[i]))
			d, dx := stepD(s.d, sd[i]), stepDx(s.d, sd[i])
			got := map[string]string{
				"D":  textDecisionD(d),
				"Dx": textDecisionD(dx),
				"E":  textDecisionE(stepE(s.e, se[i])),
				"F":  textDecisionF(stepF(s.f, sf[i])),
			}
			for form, g := range got {
				if g != want {
					t.Errorf("step(%s, %v) form %s = %s, want %s", textStateA(s.a), raws[i], form, g, want)
				}
			}
			for _, dd := range []DecisionD[StateD, string]{d, dx} {
				if !dd.wellFormedWith(StateD.wellFormed) {
					t.Errorf("step(%s, %v): form D decision is not well formed: %+v", textStateA(s.a), raws[i], dd)
				}
			}
		}
	}
}

// TestBreakerRunsAgree runs the machine over a seeded signal sequence in
// each form, as the benchmark does, and compares the traces.
func TestBreakerRunsAgree(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		raw := breakerSignals(4096, seed)
		sa, sd, se, sf := signalsA(raw), signalsD(raw), signalsE(raw), signalsF(raw)
		var a StateA = Closed{}
		d, dx, e := ClosedD(Closed{}), ClosedD(Closed{}), StateE{stateClosed, Closed{}}
		var f StateF = Closed{}
		for i := range raw {
			da := stepA(a, sa[i])
			want := textDecisionA(da)
			ddd, ddx := stepD(d, sd[i]), stepDx(dx, sd[i])
			de, df := stepE(e, se[i]), stepF(f, sf[i])
			for form, got := range map[string]string{"D": textDecisionD(ddd), "Dx": textDecisionD(ddx), "E": textDecisionE(de), "F": textDecisionF(df)} {
				if got != want {
					t.Fatalf("seed %d signal %d form %s: %s, want %s", seed, i, form, got, want)
				}
			}
			a, d, dx, e, f = applyA(a, da), applyD(d, ddd), applyD(dx, ddx), applyE(e, de), applyF(f, df)
			if !d.wellFormed() || !dx.wellFormed() {
				t.Fatalf("seed %d signal %d: form D state is not well formed", seed, i)
			}
		}
	}
}

func TestTidewalkFormsAgree(t *testing.T) {
	lists := append([]string{}, tideReplays...)
	for seed := uint64(1); seed <= 300; seed++ {
		lists = append(lists, randomMoves(40, seed))
	}
	enters := 0
	for _, s := range lists {
		ga, wa := playA(movesA(s), []string{})
		gd, wd := playG(movesB(s), []string{}, func(g GameD, d DecisionD[GameD, string]) {
			if !g.wellFormed() || !d.wellFormedWith(GameD.wellFormed) {
				t.Fatalf("%s: form D value is not well formed: %+v %+v", s, g, d)
			}
			for _, e := range decideD(g, moveWait) {
				if !e.wellFormed() {
					t.Fatalf("%s: form D event is not well formed: %+v", s, e)
				}
			}
		})
		if !reflect.DeepEqual(wa, wd) {
			t.Fatalf("%s: decisions %v, form G %v", s, wa, wd)
		}
		walkA, statusA := walkOfA(ga)
		walkD, statusD := walkOfD(gd)
		if walkA != walkD || statusA != statusD {
			t.Fatalf("%s: last state %s %+v, form G %s %+v", s, statusA, walkA, statusD, walkD)
		}
		for _, w := range wa {
			if w == "enter" {
				enters++
			}
		}
	}
	enumA, enumB := movesA(randomMoves(1024, 5)), movesB(randomMoves(1024, 5))
	for i := range enumA {
		p := Pos{X: i % 7, Y: i % 5}
		if enumTargetA(p, enumA[i]) != enumTargetB(p, enumB[i]) {
			t.Fatalf("move %d: target differs between forms A and B", i)
		}
	}
	if enters < len(lists) {
		t.Fatalf("the move lists enter only %d times; the domain is too small", enters)
	}
	// The recorded case of examples/tidewalk-replay: shore ends drowned or
	// won with the shells the case prints; the A form is the reference, and
	// this checks only that a replay reaches a final state.
	g, _ := playA(movesA(tideReplays[0]), nil)
	if _, st := walkOfA(g); st == "" {
		t.Fatal("no status")
	}
}

func TestDispatchFormsAgree(t *testing.T) {
	check := func(name string, want, got []int) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: results differ from form A", name)
		}
	}
	collect := func(n int, f func(int) int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	{
		a, d, e, f := inputsU2(rawSingles(1024, 2, 2))
		check("U2 D", collect(1024, func(i int) int { return dispatchU2A(a[i]) }), collect(1024, func(i int) int { return dispatchU2D(d[i]) }))
		check("U2 E", collect(1024, func(i int) int { return dispatchU2A(a[i]) }), collect(1024, func(i int) int { return dispatchU2E(e[i]) }))
		check("U2 F", collect(1024, func(i int) int { return dispatchU2A(a[i]) }), collect(1024, func(i int) int { return dispatchU2F(f[i]) }))
	}
	{
		a, d, e, f := inputsU4(rawSingles(1024, 4, 4))
		check("U4 D", collect(1024, func(i int) int { return dispatchU4A(a[i]) }), collect(1024, func(i int) int { return dispatchU4D(d[i]) }))
		check("U4 E", collect(1024, func(i int) int { return dispatchU4A(a[i]) }), collect(1024, func(i int) int { return dispatchU4E(e[i]) }))
		check("U4 F", collect(1024, func(i int) int { return dispatchU4A(a[i]) }), collect(1024, func(i int) int { return dispatchU4F(f[i]) }))
	}
	{
		a, d, e, f := inputsU8(rawSingles(1024, 8, 8))
		check("U8 D", collect(1024, func(i int) int { return dispatchU8A(a[i]) }), collect(1024, func(i int) int { return dispatchU8D(d[i]) }))
		check("U8 E", collect(1024, func(i int) int { return dispatchU8A(a[i]) }), collect(1024, func(i int) int { return dispatchU8E(e[i]) }))
		check("U8 F", collect(1024, func(i int) int { return dispatchU8A(a[i]) }), collect(1024, func(i int) int { return dispatchU8F(f[i]) }))
	}
	{
		a, d, e, f := inputsU16(rawSingles(1024, 16, 16))
		check("U16 D", collect(1024, func(i int) int { return dispatchU16A(a[i]) }), collect(1024, func(i int) int { return dispatchU16D(d[i]) }))
		check("U16 E", collect(1024, func(i int) int { return dispatchU16A(a[i]) }), collect(1024, func(i int) int { return dispatchU16E(e[i]) }))
		check("U16 F", collect(1024, func(i int) int { return dispatchU16A(a[i]) }), collect(1024, func(i int) int { return dispatchU16F(f[i]) }))
	}
	{
		in := inputsP3x4(rawPairs(1024, 3, 4, 34))
		want := collect(1024, func(i int) int { return dispatchP3x4A(in[i].aA, in[i].bA) })
		check("3x4 D", want, collect(1024, func(i int) int { return dispatchP3x4D(in[i].aD, in[i].bD) }))
		check("3x4 Dx", want, collect(1024, func(i int) int { return dispatchP3x4Dx(in[i].aD, in[i].bD) }))
		check("3x4 E", want, collect(1024, func(i int) int { return dispatchP3x4E(in[i].aE, in[i].bE) }))
		check("3x4 F", want, collect(1024, func(i int) int { return dispatchP3x4F(in[i].aF, in[i].bF) }))
	}
	{
		in := inputsP6x6(rawPairs(1024, 6, 6, 66))
		want := collect(1024, func(i int) int { return dispatchP6x6A(in[i].aA, in[i].bA) })
		check("6x6 D", want, collect(1024, func(i int) int { return dispatchP6x6D(in[i].aD, in[i].bD) }))
		check("6x6 Dx", want, collect(1024, func(i int) int { return dispatchP6x6Dx(in[i].aD, in[i].bD) }))
		check("6x6 E", want, collect(1024, func(i int) int { return dispatchP6x6E(in[i].aE, in[i].bE) }))
		check("6x6 F", want, collect(1024, func(i int) int { return dispatchP6x6F(in[i].aF, in[i].bF) }))
	}
}

func TestCopyFormsAgree(t *testing.T) {
	var a16, a32, a64, a128 = Copy16A(Small16{}), Copy32A(Small32{}), Copy64A(Small64{}), Copy128A(Small128{})
	d16, d32, d64, d128 := Copy16D{tag: 2}, Copy32D{tag: 2}, Copy64D{tag: 2}, Copy128D{tag: 2}
	for i := 0; i < 100; i++ {
		a16, a32, a64, a128 = pass16A(next16A(a16)), pass32A(next32A(a32)), pass64A(next64A(a64)), pass128A(next128A(a128))
		d16, d32, d64, d128 = pass16D(next16D(d16)), pass32D(next32D(d32)), pass64D(next64D(d64)), pass128D(next128D(d128))
	}
	got := []int64{firstWord16D(d16), firstWord32D(d32), firstWord64D(d64), firstWord128D(d128)}
	want := []int64{firstWord16A(a16), firstWord32A(a32), firstWord64A(a64), firstWord128A(a128)}
	if !reflect.DeepEqual(got, want) || want[0] != 100 {
		t.Fatalf("form D %v, form A %v", got, want)
	}
}

func TestGCFormsAgree(t *testing.T) {
	raw := rawSingles(10000, 2, 40)
	npa, npd, npe := buildNPA(raw), buildNPD(raw), buildNPE(raw)
	wpa, wpd, wpe := buildWPA(raw), buildWPD(raw), buildWPE(raw)
	for round := 0; round < 3; round++ {
		for i := range raw {
			if textNPD(npd[i]) != textNPA(npa[i]) || textNPE(npe[i]) != textNPA(npa[i]) {
				t.Fatalf("round %d NP %d: %s %s, want %s", round, i, textNPD(npd[i]), textNPE(npe[i]), textNPA(npa[i]))
			}
			if textWPD(wpd[i]) != textWPA(wpa[i]) || textWPE(wpe[i]) != textWPA(wpa[i]) {
				t.Fatalf("round %d WP %d: %s %s, want %s", round, i, textWPD(wpd[i]), textWPE(wpe[i]), textWPA(wpa[i]))
			}
			if !npd[i].wellFormed() || !wpd[i].wellFormed() {
				t.Fatalf("round %d %d: form D value is not well formed", round, i)
			}
			npa[i], npd[i], npe[i] = nextNPA(npa[i]), nextNPD(npd[i]), nextNPE(npe[i])
			wpa[i], wpd[i], wpe[i] = nextWPA(wpa[i]), nextWPD(wpd[i]), nextWPE(wpe[i])
		}
	}
}

func TestNarrowFormsAgree(t *testing.T) {
	a, d, ds := n64Inputs(rawSingles(1024, 2, 64))
	for i := range a {
		if narrowD(d[i]) != narrowA(a[i]) || narrowDs(ds[i]) != narrowA(a[i]) {
			t.Fatalf("%d: D %d Ds %d, want %d", i, narrowD(d[i]), narrowDs(ds[i]), narrowA(a[i]))
		}
	}
}

func TestCloneFormsAgree(t *testing.T) {
	a, d := heldBag(16)
	ca, cd := cloneA(a), cloneD(d)
	a.(Bag).Xs[0], d.xs[0] = -1, -1
	if ca.(Bag).Xs[0] != 256 || cd.xs[0] != 256 {
		t.Fatal("a clone shares the slice of its source")
	}
	if !reflect.DeepEqual(ca.(Bag).Xs, cd.xs) || !cd.wellFormed() {
		t.Fatalf("clones differ: %v %v", ca, cd)
	}
	tagA, tagD := cloneA(Tag{N: 300}), cloneD(HeldD{tag: 2, n: 300})
	if tagA.(Tag).N != tagD.n || !tagD.wellFormed() {
		t.Fatalf("clones differ: %v %v", tagA, tagD)
	}
}

func TestOptionFormsAgree(t *testing.T) {
	for _, small := range []bool{false, true} {
		m, keys := optionMapInt(small)
		want := sumGetIntGo(m, keys, 4096)
		if a, c := sumGetIntA(m, keys, 4096), sumGetIntC(m, keys, 4096); a != want || c != want {
			t.Errorf("Get int (small %v): A %d C %d, want %d", small, a, c, want)
		}
		if a, c := sumLookupA(m, keys, 4096), sumLookupC(m, keys, 4096); a != want || c != want || sumLookupGo(m, keys, 4096) != want {
			t.Errorf("lookup int (small %v): A %d C %d, want %d", small, a, c, want)
		}
	}
	ms, keys := optionMapString()
	want := sumGetStringGo(ms, keys, 4096)
	if a, c := sumGetStringA(ms, keys, 4096), sumGetStringC(ms, keys, 4096); a != want || c != want {
		t.Errorf("Get string: A %d C %d, want %d", a, c, want)
	}
	in := cutInputs()
	want = sumCutGo(in, 4096)
	if a, c := sumCutA(in, 4096), sumCutC(in, 4096); a != want || c != want {
		t.Errorf("Cut: A %d C %d, want %d", a, c, want)
	}
	if a, c, g := sumCutOutA(in, 4096), sumCutOutC(in, 4096), sumCutOutGo(in, 4096); a != want || c != want || g != want {
		t.Errorf("Cut through a call: A %d C %d Go %d, want %d", a, c, g, want)
	}
	// The zero-slot invariant of form C: a None is the zero value.
	if o := getC(map[string]int{}, "x"); o != (OptionC[int]{}) {
		t.Errorf("a None of form C is not the zero value: %+v", o)
	}
	for _, s := range in {
		if o := cutC(s, "="); !o.ok && o != (OptionC[Halves]{}) {
			t.Errorf("cutC(%q) is a None that is not the zero value: %+v", s, o)
		}
	}
}

// Question 7: == on form D agrees with == on form A for every pair of
// generated values, because every constructor keeps inactive slots zero.
func TestEqualityAgrees(t *testing.T) {
	closeds, opens, _ := breakerDomain()
	var sa []StateA
	var sd []StateD
	for _, c := range closeds {
		sa, sd = append(sa, c), append(sd, ClosedD(c))
	}
	for _, o := range opens {
		sa, sd = append(sa, o), append(sd, OpenD(o))
	}
	// Open{""} has the same slots as no Closed value, and Closed{0} the
	// same as no Open value, only because the tag differs.
	sa, sd = append(sa, Open{""}), append(sd, OpenD(Open{""}))
	for i := range sa {
		for j := range sa {
			if (sa[i] == sa[j]) != (sd[i] == sd[j]) {
				t.Errorf("State %d == %d: form A %v, form D %v", i, j, sa[i] == sa[j], sd[i] == sd[j])
			}
		}
	}
	raw := rawSingles(2000, 2, 77)
	for i := range raw {
		raw[i].x = 256 + raw[i].x%4 // a small domain, so equal pairs occur
	}
	npa, npd := buildNPA(raw), buildNPD(raw)
	wpa, wpd := buildWPA(raw), buildWPD(raw)
	equal := 0
	for n := 0; n < 20000; n++ {
		i, j := (n*7919)%len(raw), (n*104729+13)%len(raw)
		if (npa[i] == npa[j]) != (npd[i] == npd[j]) {
			t.Fatalf("NP %d == %d: form A %v, form D %v", i, j, npa[i] == npa[j], npd[i] == npd[j])
		}
		if (wpa[i] == wpa[j]) != (wpd[i] == wpd[j]) {
			t.Fatalf("WP %d == %d: form A %v, form D %v", i, j, wpa[i] == wpa[j], wpd[i] == wpd[j])
		}
		if npa[i] == npa[j] {
			equal++
		}
	}
	if equal == 0 {
		t.Fatal("no equal pair: the domain does not test equality")
	}
}

// TestDirtySlotBreaksEquality shows why the invariant is needed: a form D
// value whose inactive slot is not zero is the same Ergo value as a clean
// one, and Go's == says it is not.
func TestDirtySlotBreaksEquality(t *testing.T) {
	clean := ClosedD(Closed{1})
	dirty := StateD{tag: stateClosed, failures: 1, endpoint: "left over"}
	if textStateD(clean) != textStateD(dirty) {
		t.Fatal("the two values must be the same Ergo value")
	}
	if clean == dirty {
		t.Fatal("== ignores the inactive slot; the invariant would not be needed")
	}
	if dirty.wellFormed() {
		t.Fatal("wellFormed accepts a dirty slot")
	}
}

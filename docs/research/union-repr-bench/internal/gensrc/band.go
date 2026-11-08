package gensrc

import (
	"fmt"
	"strings"
)

// The size band of round 2 (the 33 to 136 byte question of rule G).
//
//   - decisionBand: Breaker's step and apply with a Decision whose form D
//     struct is 40, 56, 64 or 96 bytes (Done carries w int64 words), in
//     form A (every union A), form D (every union D) and form M (State D
//     boxed in an A Decision: what the round-1 rule picked).
//   - optionBand: Option[Word{k}] with a payload of k int64 words, returned
//     through a call that Go does not inline and matched at once (get), and
//     carried unchanged through such a call (pass), in forms A and C.
//   - copyTests: the question 5 benchmarks and contract test for every
//     size in copyWords.

// Done words of the Decision band: form D size = 8 (tag) + 32 (StateD) + 8w.
var decisionWords = []int{0, 2, 3, 7}

// Payload words of the Option band: form C size = 8k + 8.
var optionWords = []int{1, 2, 3, 4, 6, 8, 16}

func words(prefix string, n int, typ string) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s%d %s", prefix, i, typ))
	}
	return out
}

func structOf(fields []string) string {
	if len(fields) == 0 {
		return "struct{}"
	}
	return "struct {\n\t" + strings.Join(fields, "\n\t") + "\n}"
}

func decisionBand(b *strings.Builder) {
	for _, w := range decisionWords {
		n := 40 + 8*w
		out := fmt.Sprintf("Out%d", w)
		var vals []string
		for i := 0; i < w; i++ {
			vals = append(vals, fmt.Sprint(i+1))
		}
		fmt.Fprintf(b, "// ---- Decision band: the form D Decision is %d bytes ----\n\n", n)
		fmt.Fprintf(b, "type %s %s\n\n", out, structOf(words("O", w, "int64")))
		fmt.Fprintf(b, "var the%s = %s{%s}\n\n", out, out, strings.Join(vals, ", "))
		fmt.Fprintf(b, "func (o %s) sum() int64 {\n\tt := int64(0)\n", out)
		for i := 0; i < w; i++ {
			fmt.Fprintf(b, "\tt += o.O%d\n", i)
		}
		b.WriteString("\treturn t\n}\n\n")
		fmt.Fprintf(b, "// Dec%dD: Enter.Next and Stay.Next share the StateD slot; Done's words\n// have a slot each.\ntype Dec%dD %s\n\n", n, n, structOf(append([]string{"tag uint8", "next StateD"}, words("o", w, "int64")...)))

		// Form A and form M share the shape of the code: only the state
		// type differs.
		for _, f := range []struct{ id, st string }{{"A", "StateA"}, {"M", "StateD"}} {
			dec := fmt.Sprintf("DecisionA[%s, %s]", f.st, out)
			if f.id == "A" {
				fmt.Fprintf(b, "//go:noinline\nfunc step%dA(s StateA, sig SignalA) %s {\n", n, dec)
				fmt.Fprintf(b, "\tswitch st := s.(type) {\n\tcase Closed:\n\t\tswitch e := sig.(type) {\n\t\tcase Failure:\n\t\t\tif st.Failures >= 2 {\n\t\t\t\treturn Enter[StateA]{Open{e.Endpoint}}\n\t\t\t}\n\t\t\treturn Stay[StateA]{Closed{st.Failures + 1}}\n\t\tcase Retry:\n\t\t\treturn Ignore{}\n\t\t}\n\tcase Open:\n\t\tswitch sig.(type) {\n\t\tcase Retry:\n\t\t\treturn Enter[StateA]{Closed{0}}\n\t\tcase Failure:\n\t\t\treturn Done[%s]{the%s}\n\t\t}\n\t}\n\tpanic(\"ergo: defect: no case of the match matched\")\n}\n\n", out, out)
			} else {
				// Form M: the round-1 rule's choice, a form D state boxed
				// in a form A Decision.
				fmt.Fprintf(b, "//go:noinline\nfunc step%dM(s StateD, sig SignalD) %s {\n", n, dec)
				fmt.Fprintf(b, "\tswitch s.tag {\n\tcase stateClosed:\n\t\tswitch sig.tag {\n\t\tcase signalFailure:\n\t\t\tif s.failures >= 2 {\n\t\t\t\treturn Enter[StateD]{OpenD(Open{sig.endpoint})}\n\t\t\t}\n\t\t\treturn Stay[StateD]{ClosedD(Closed{s.failures + 1})}\n\t\tcase signalRetry:\n\t\t\treturn Ignore{}\n\t\t}\n\tcase stateOpen:\n\t\tswitch sig.tag {\n\t\tcase signalRetry:\n\t\t\treturn Enter[StateD]{ClosedD(Closed{0})}\n\t\tcase signalFailure:\n\t\t\treturn Done[%s]{the%s}\n\t\t}\n\t}\n\tpanic(\"ergo: defect: no case of the match matched\")\n}\n\n", out, out)
			}
			fmt.Fprintf(b, "//go:noinline\nfunc apply%d%s(s %s, d %s) %s {\n\tswitch v := d.(type) {\n\tcase Enter[%s]:\n\t\treturn v.Next\n\tcase Stay[%s]:\n\t\treturn v.Next\n\tcase Ignore, Reject, Done[%s]:\n\t\treturn s\n\t}\n\tpanic(\"ergo: defect: no case of the match matched\")\n}\n\n", n, f.id, f.st, dec, f.st, f.st, f.st, out)
			text := "textStateA"
			if f.id == "M" {
				text = "textStateD"
			}
			fmt.Fprintf(b, "func text%d%s(d %s) string {\n\tswitch v := d.(type) {\n\tcase Enter[%s]:\n\t\treturn \"Enter{\" + %s(v.Next) + \"}\"\n\tcase Stay[%s]:\n\t\treturn \"Stay{\" + %s(v.Next) + \"}\"\n\tcase Ignore:\n\t\treturn \"Ignore\"\n\tcase Reject:\n\t\treturn \"Reject\"\n\tcase Done[%s]:\n\t\treturn \"Done{\" + strconv.FormatInt(v.Output.sum(), 10) + \"}\"\n\t}\n\tpanic(\"no variant\")\n}\n\n", n, f.id, dec, f.st, text, f.st, text, out)
		}

		// Form D.
		var outSlots, outRead, zeroOut []string
		for i := 0; i < w; i++ {
			outSlots = append(outSlots, fmt.Sprintf("o%d: the%s.O%d", i, out, i))
			outRead = append(outRead, fmt.Sprintf("d.o%d", i))
			zeroOut = append(zeroOut, fmt.Sprintf("d.o%d == 0", i))
		}
		dd := fmt.Sprintf("Dec%dD", n)
		fmt.Fprintf(b, "//go:noinline\nfunc step%dD(s StateD, sig SignalD) %s {\n", n, dd)
		fmt.Fprintf(b, "\tswitch s.tag {\n\tcase stateClosed:\n\t\tswitch sig.tag {\n\t\tcase signalFailure:\n\t\t\tif s.failures >= 2 {\n\t\t\t\treturn %s{tag: decEnter, next: OpenD(Open{sig.endpoint})}\n\t\t\t}\n\t\t\treturn %s{tag: decStay, next: ClosedD(Closed{s.failures + 1})}\n\t\tcase signalRetry:\n\t\t\treturn %s{tag: decIgnore}\n\t\t}\n\tcase stateOpen:\n\t\tswitch sig.tag {\n\t\tcase signalRetry:\n\t\t\treturn %s{tag: decEnter, next: ClosedD(Closed{0})}\n\t\tcase signalFailure:\n\t\t\treturn %s{%s}\n\t\t}\n\t}\n\tpanic(\"ergo: defect: no case of the match matched\")\n}\n\n", dd, dd, dd, dd, dd, strings.Join(append([]string{"tag: decDone"}, outSlots...), ", "))
		fmt.Fprintf(b, "//go:noinline\nfunc apply%dD(s StateD, d %s) StateD {\n\tswitch d.tag {\n\tcase decEnter, decStay:\n\t\treturn d.next\n\tcase decIgnore, decReject, decDone:\n\t\treturn s\n\t}\n\tpanic(\"ergo: defect: no case of the match matched\")\n}\n\n", n, dd)
		sum := "0"
		if w > 0 {
			sum = strings.Join(outRead, " + ")
		}
		fmt.Fprintf(b, "func text%dD(d %s) string {\n\tswitch d.tag {\n\tcase decEnter:\n\t\treturn \"Enter{\" + textStateD(d.next) + \"}\"\n\tcase decStay:\n\t\treturn \"Stay{\" + textStateD(d.next) + \"}\"\n\tcase decIgnore:\n\t\treturn \"Ignore\"\n\tcase decReject:\n\t\treturn \"Reject\"\n\tcase decDone:\n\t\treturn \"Done{\" + strconv.FormatInt(%s, 10) + \"}\"\n\t}\n\tpanic(\"no variant\")\n}\n\n", n, dd, sum)
		zero := "true"
		if w > 0 {
			zero = strings.Join(zeroOut, " && ")
		}
		fmt.Fprintf(b, "func (d %s) wellFormed() bool {\n\tswitch d.tag {\n\tcase decEnter, decStay:\n\t\treturn d.next.wellFormed() && %s\n\tcase decIgnore, decReject:\n\t\treturn d.next == StateD{} && %s\n\tcase decDone:\n\t\treturn d.next == StateD{}\n\t}\n\treturn false\n}\n\n", dd, zero, zero)
	}
}

func optionBand(b *strings.Builder) {
	for _, k := range optionWords {
		n := 8*k + 8
		wt := fmt.Sprintf("Word%d", k)
		fmt.Fprintf(b, "// ---- Option band: form C of Option[%s] is %d bytes ----\n\n", wt, n)
		fmt.Fprintf(b, "type %s %s\n\n", wt, structOf(words("W", k, "int64")))
		fmt.Fprintf(b, "func words%d(n int) []%s {\n\tout := make([]%s, n)\n\tfor i := range out {\n\t\tout[i].W0 = int64(i)\n\t}\n\treturn out\n}\n\n", k, wt, wt)
		fmt.Fprintf(b, "//go:noinline\nfunc optGet%dA(xs []%s, i int) OptionA[%s] {\n\tif i&1 == 0 {\n\t\treturn Some[%s]{Value: xs[i&1023]}\n\t}\n\treturn None{}\n}\n\n", k, wt, wt, wt)
		fmt.Fprintf(b, "//go:noinline\nfunc optGet%dC(xs []%s, i int) OptionC[%s] {\n\tif i&1 == 0 {\n\t\treturn OptionC[%s]{v: xs[i&1023], ok: true}\n\t}\n\treturn OptionC[%s]{}\n}\n\n", k, wt, wt, wt, wt)
		fmt.Fprintf(b, "func sumOpt%dA(xs []%s, n int) int64 {\n\tt := int64(0)\n\tfor i := 0; i < n; i++ {\n\t\tswitch o := optGet%dA(xs, i).(type) {\n\t\tcase Some[%s]:\n\t\t\tt += o.Value.W0\n\t\tcase None:\n\t\t\tt--\n\t\t}\n\t}\n\treturn t\n}\n\n", k, wt, k, wt)
		fmt.Fprintf(b, "func sumOpt%dC(xs []%s, n int) int64 {\n\tt := int64(0)\n\tfor i := 0; i < n; i++ {\n\t\tif o := optGet%dC(xs, i); o.ok {\n\t\t\tt += o.v.W0\n\t\t} else {\n\t\t\tt--\n\t\t}\n\t}\n\treturn t\n}\n\n", k, wt, k)
		fmt.Fprintf(b, "//go:noinline\nfunc optPass%dA(o OptionA[%s]) OptionA[%s] { return o }\n\n", k, wt, wt)
		fmt.Fprintf(b, "//go:noinline\nfunc optPass%dC(o OptionC[%s]) OptionC[%s] { return o }\n\n", k, wt, wt)
	}
}

// generatedTest is generated_test.go: the benchmarks and contract tests of
// the generated sizes.
func generatedTest() *strings.Builder {
	b := &strings.Builder{}
	b.WriteString(header + "package unionreprbench\n\nimport \"testing\"\n\n")

	// Question 5.
	b.WriteString("// Question 5: pass (take and return) and next (narrow, change, rebuild).\n\n")
	b.WriteString("func BenchmarkCopyPass(b *testing.B) {\n")
	for _, k := range copyWords {
		n := 8 * k
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=A\", func(b *testing.B) {\n\t\tvar u Copy%dA = Big%d{}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\tu = pass%dA(u)\n\t\t}\n\t\tsinkInt64 = firstWord%dA(u)\n\t})\n", n, n, n, n, n)
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=D\", func(b *testing.B) {\n\t\tu := Copy%dD{tag: 1}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\tu = pass%dD(u)\n\t\t}\n\t\tsinkInt64 = firstWord%dD(u)\n\t})\n", n, n, n, n)
	}
	b.WriteString("}\n\nfunc BenchmarkCopyNext(b *testing.B) {\n")
	for _, k := range copyWords {
		n := 8 * k
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=A\", func(b *testing.B) {\n\t\tb.ReportAllocs()\n\t\tvar u Copy%dA = Big%d{}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\tu = next%dA(u)\n\t\t}\n\t\tsinkInt64 = firstWord%dA(u)\n\t})\n", n, n, n, n, n)
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=D\", func(b *testing.B) {\n\t\tb.ReportAllocs()\n\t\tu := Copy%dD{tag: 1}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\tu = next%dD(u)\n\t\t}\n\t\tsinkInt64 = firstWord%dD(u)\n\t})\n", n, n, n, n)
	}
	b.WriteString("}\n\nfunc TestCopyFormsAgree(t *testing.T) {\n")
	for _, k := range copyWords {
		n := 8 * k
		fmt.Fprintf(b, "\t{\n\t\tvar a Copy%dA = Small%d{}\n\t\td := Copy%dD{tag: 2}\n\t\tfor i := 0; i < 100; i++ {\n\t\t\ta, d = pass%dA(next%dA(a)), pass%dD(next%dD(d))\n\t\t}\n\t\tif firstWord%dA(a) != 100 || firstWord%dD(d) != 100 {\n\t\t\tt.Fatalf(\"%d bytes: form A %%d, form D %%d\", firstWord%dA(a), firstWord%dD(d))\n\t\t}\n\t}\n", n, n, n, n, n, n, n, n, n, n, n, n)
	}
	b.WriteString("}\n\n")

	// The Decision band.
	b.WriteString("// The Decision band: one transition (step, then apply) per op, with the\n// Decision's form D struct at 40 to 96 bytes.\n\n")
	b.WriteString("func BenchmarkDecisionBand(b *testing.B) {\n\traw := breakerSignals(1024, 7)\n")
	for _, w := range decisionWords {
		n := 40 + 8*w
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=A\", func(b *testing.B) {\n\t\tin := signalsA(raw)\n\t\tvar s StateA = Closed{}\n\t\texactAllocs(b, 1, \"transition\", func() {\n\t\t\tfor i := 0; i < b.N; i++ {\n\t\t\t\ts = apply%dA(s, step%dA(s, in[i&1023]))\n\t\t\t}\n\t\t})\n\t\tsinkStateA = s\n\t})\n", n, n, n)
		for _, f := range []string{"D", "M"} {
			fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=%s\", func(b *testing.B) {\n\t\tin := signalsD(raw)\n\t\ts := ClosedD(Closed{})\n\t\texactAllocs(b, 1, \"transition\", func() {\n\t\t\tfor i := 0; i < b.N; i++ {\n\t\t\t\ts = apply%d%s(s, step%d%s(s, in[i&1023]))\n\t\t\t}\n\t\t})\n\t\tsinkStateD = s\n\t})\n", n, f, n, f, n, f)
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("// TestDecisionBandAgrees: every state × signal and a seeded run give the\n// same decisions in forms A, D and M; every form D Decision is well formed.\nfunc TestDecisionBandAgrees(t *testing.T) {\n\tcloseds, opens, raws := breakerDomain()\n\tvar sa []StateA\n\tvar sd []StateD\n\tfor _, c := range closeds {\n\t\tsa, sd = append(sa, c), append(sd, ClosedD(c))\n\t}\n\tfor _, o := range opens {\n\t\tsa, sd = append(sa, o), append(sd, OpenD(o))\n\t}\n\tga, gd := signalsA(raws), signalsD(raws)\n\trun := breakerSignals(4096, 3)\n\tra, rd := signalsA(run), signalsD(run)\n")
	for _, w := range decisionWords {
		n := 40 + 8*w
		fmt.Fprintf(b, "\tfor i := range sa {\n\t\tfor j := range raws {\n\t\t\twant := text%dA(step%dA(sa[i], ga[j]))\n\t\t\td := step%dD(sd[i], gd[j])\n\t\t\tif got := text%dD(d); got != want || !d.wellFormed() {\n\t\t\t\tt.Fatalf(\"%d bytes: form D %%s (well formed %%v), want %%s\", got, d.wellFormed(), want)\n\t\t\t}\n\t\t\tif got := text%dM(step%dM(sd[i], gd[j])); got != want {\n\t\t\t\tt.Fatalf(\"%d bytes: form M %%s, want %%s\", got, want)\n\t\t\t}\n\t\t}\n\t}\n", n, n, n, n, n, n, n, n)
		fmt.Fprintf(b, "\t{\n\t\tvar a StateA = Closed{}\n\t\td, m := ClosedD(Closed{}), ClosedD(Closed{})\n\t\tfor i := range run {\n\t\t\tda, dd, dm := step%dA(a, ra[i]), step%dD(d, rd[i]), step%dM(m, rd[i])\n\t\t\twant := text%dA(da)\n\t\t\tif text%dD(dd) != want || text%dM(dm) != want || !dd.wellFormed() {\n\t\t\t\tt.Fatalf(\"%d bytes, signal %%d: D %%s, M %%s, want %%s\", i, text%dD(dd), text%dM(dm), want)\n\t\t\t}\n\t\t\ta, d, m = apply%dA(a, da), apply%dD(d, dd), apply%dM(m, dm)\n\t\t}\n\t}\n", n, n, n, n, n, n, n, n, n, n, n, n)
	}
	b.WriteString("}\n\n")

	// The Option band.
	b.WriteString("// The Option band: an Option of 16 to 136 bytes in form C, returned\n// through a call and matched at once (get), or carried unchanged (pass).\n\n")
	b.WriteString("func BenchmarkOptionSize(b *testing.B) {\n")
	for _, k := range optionWords {
		n := 8*k + 8
		for _, f := range []string{"A", "C"} {
			fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=%s\", func(b *testing.B) {\n\t\txs := words%d(1024)\n\t\texactAllocs(b, 1, \"get\", func() { sinkInt64 = sumOpt%d%s(xs, b.N) })\n\t})\n", n, f, k, k, f)
		}
	}
	b.WriteString("}\n\nfunc BenchmarkOptionPass(b *testing.B) {\n")
	for _, k := range optionWords {
		n := 8*k + 8
		wt := fmt.Sprintf("Word%d", k)
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=A\", func(b *testing.B) {\n\t\tvar o OptionA[%s] = Some[%s]{Value: %s{W0: 1}}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\to = optPass%dA(o)\n\t\t}\n\t\tsinkInt64 = o.(Some[%s]).Value.W0\n\t})\n", n, wt, wt, wt, k, wt)
		fmt.Fprintf(b, "\tb.Run(\"bytes=%d/form=C\", func(b *testing.B) {\n\t\to := OptionC[%s]{v: %s{W0: 1}, ok: true}\n\t\tfor i := 0; i < b.N; i++ {\n\t\t\to = optPass%dC(o)\n\t\t}\n\t\tsinkInt64 = o.v.W0\n\t})\n", n, wt, wt, k)
	}
	b.WriteString("}\n\n")
	b.WriteString("func TestOptionBandAgrees(t *testing.T) {\n")
	for _, k := range optionWords {
		n := 8*k + 8
		wt := fmt.Sprintf("Word%d", k)
		fmt.Fprintf(b, "\t{\n\t\txs := words%d(1024)\n\t\tif a, c := sumOpt%dA(xs, 4096), sumOpt%dC(xs, 4096); a != c {\n\t\t\tt.Fatalf(\"%d bytes: form A %%d, form C %%d\", a, c)\n\t\t}\n\t\tif o := optGet%dC(xs, 1); o != (OptionC[%s]{}) {\n\t\t\tt.Fatalf(\"%d bytes: None is not the zero value: %%+v\", o)\n\t\t}\n\t\tif optPass%dC(optGet%dC(xs, 2)).v != xs[2] || optPass%dA(optGet%dA(xs, 2)).(Some[%s]).Value != xs[2] {\n\t\t\tt.Fatalf(\"%d bytes: pass changed the value\")\n\t\t}\n\t}\n", k, k, k, n, k, wt, n, k, k, k, k, wt, n)
	}
	b.WriteString("}\n")
	return b
}

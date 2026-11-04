package bindc

import (
	"fmt"
	"strings"
)

// Report is the classification report of a binding (the report.txt
// golden): the inputs, every typed function with its Go signature, every
// raw-only function with its reason, every callback typedef (bound with
// its entry and the class of each parameter, or raw with its reason),
// every pointer field with its class
// and reason, the counts, and every exception and skip with its reason. The order is the model's, so the report is stable.
func Report(b *Binding) []byte {
	var w strings.Builder
	c := b.Contract
	fmt.Fprintf(&w, "binding report: %s\n", c.Header)
	fmt.Fprintf(&w, "header sha256 %s\n", b.HeaderSHA256)
	fmt.Fprintf(&w, "go package %s (%s)\n", c.Go.Package, c.Go.ImportPath)
	fmt.Fprintf(&w, "status %s: success %s, %d codes\n", c.Status.Type, c.Status.Success, len(b.Codes))
	fmt.Fprintf(&w, "abi %s() == %s (%d)\n", c.ABI.Function, c.ABI.Macro, b.ABIWant)
	for _, h := range c.Handles {
		fmt.Fprintf(&w, "handle %s (%s): create %s.%s, destroy %s, thread %s\n",
			h.Type, b.GoTypes[h.Type], h.Create, h.Out, h.Destroy, h.Thread)
	}
	for _, a := range c.Autofill {
		fmt.Fprintf(&w, "autofill %s = %s\n", a.Field, a.Value)
	}

	var typed, raw []*BoundFunc
	counts := map[Class]int{}
	funcPositions := 0
	for _, f := range b.Funcs {
		if f.RawOnly == "" {
			typed = append(typed, f)
		} else {
			raw = append(raw, f)
		}
		for _, q := range f.Positions {
			counts[q.Class]++
			funcPositions++
		}
	}
	for _, q := range b.Fields {
		counts[q.Class]++
	}
	fmt.Fprintf(&w, "\ntyped functions (%d):\n", len(typed))
	for _, f := range typed {
		fmt.Fprintf(&w, "  %s: %s\n", f.C.Name, f.Sig.String(f.GoName))
	}
	fmt.Fprintf(&w, "\nraw-only functions (%d):\n", len(raw))
	for _, f := range raw {
		fmt.Fprintf(&w, "  %s: %s\n", f.C.Name, f.RawOnly)
	}
	fmt.Fprintf(&w, "\ncallbacks (%d):\n", len(b.Callbacks))
	for _, cb := range b.Callbacks {
		fmt.Fprintf(&w, "  %s: %s\n", cb.Decl.Name, callbackLine(cb))
	}
	fmt.Fprintf(&w, "\npointer fields (%d):\n", len(b.Fields))
	for _, q := range b.Fields {
		fmt.Fprintf(&w, "  %s.%s: %s: %s\n", q.Struct, q.Name, q.Class, q.Reason)
	}
	fmt.Fprintf(&w, "\ncounts:\n")
	fmt.Fprintf(&w, "  functions %d: typed %d, raw-only %d, skipped %d\n",
		len(b.Model.Functions), len(typed), len(raw), len(b.Model.Functions)-len(b.Funcs))
	fmt.Fprintf(&w, "  pointer positions %d (parameters and results %d, fields %d):",
		funcPositions+len(b.Fields), funcPositions, len(b.Fields))
	for i, cl := range []Class{ClassHandle, ClassValue, ClassOut, ClassInBuffer, ClassOutBuffer, ClassOutCount, ClassCallback, ClassKey, ClassRaw} {
		if i > 0 {
			w.WriteString(",")
		}
		fmt.Fprintf(&w, " %s %d", cl, counts[cl])
	}
	w.WriteString("\n")
	fmt.Fprintf(&w, "  constants %d, status codes %d\n", len(b.Constants), len(b.Codes))
	fmt.Fprintf(&w, "\nexceptions (%d):\n", len(c.Exceptions))
	for _, e := range c.Exceptions {
		extra := ""
		switch e.Class {
		case ClassInBuffer:
			extra = fmt.Sprintf(", length %s in %s", e.Length, e.Unit)
		case ClassOutBuffer:
			extra = fmt.Sprintf(", capacity %s, count %s", e.Length, e.Count)
		case ClassOutArray:
			extra = fmt.Sprintf(" of %d", e.ArrayLength)
		}
		fmt.Fprintf(&w, "  %s %s: %s%s: %s\n", e.target(), e.Position, e.Class, extra, e.Why)
	}
	fmt.Fprintf(&w, "\nskips (%d):\n", len(c.Skips)+len(c.Status.Exclude))
	for _, s := range c.Skips {
		fmt.Fprintf(&w, "  %s: %s\n", s.Name, s.Why)
	}
	for _, s := range c.Status.Exclude {
		fmt.Fprintf(&w, "  %s (not a status code): %s\n", s.Name, s.Why)
	}
	return []byte(w.String())
}

// callbackLine is the report line of one callback typedef: bound, with its
// Go func type, scope, owner, end functions, key, default and the class of each
// parameter; or raw with the reason.
func callbackLine(cb *BoundCallback) string {
	if !cb.Bound() {
		return "raw: " + cb.RawOnly
	}
	r := cb.Rule
	var w strings.Builder
	fmt.Fprintf(&w, "bound as %s %s, scope %s", cb.GoName, cb.Sig.String(), r.Scope)
	if r.Owner != "" {
		fmt.Fprintf(&w, ", owner %s", r.Owner)
	}
	if len(r.Ends) > 0 {
		fmt.Fprintf(&w, ", ends %s", strings.Join(r.Ends, " "))
	}
	fmt.Fprintf(&w, ", key (%s) from %s", strings.Join(r.Key.Params, ", "), r.Key.Source)
	if len(r.Default) > 0 {
		fmt.Fprintf(&w, ", default %s", r.Default)
	}
	f := callbackFunc(cb.Decl)
	var ps []string
	for _, q := range cb.Positions {
		s := positionName(f, q.Param) + " " + string(q.Class)
		switch q.Class {
		case ClassInBuffer:
			unit := "elements"
			if q.Bytes {
				unit = "bytes"
			}
			s += fmt.Sprintf(" (length %s in %s)", f.Params[q.Length].Name, unit)
		case ClassOutArray:
			s += fmt.Sprintf(" of %d", q.Array)
		}
		ps = append(ps, s)
	}
	fmt.Fprintf(&w, "; %s", strings.Join(ps, ", "))
	return w.String()
}

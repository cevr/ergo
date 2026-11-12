package v1

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
	// The gofmt leaf: a lowered program that starts a machine imports it
	// (v1.NeedsHostText), so the runtime's tests run with it too, as the
	// machine state text of the actor tests needs.
	_ "ergo.local/prototype/runtime/v1/gofmt"
)

// refText is fmt's %v of v, as the report wrote a value before binsize
// BS2; a *Panic is written as its value, as Panic.String did with fmt.
func refText(v any) string {
	if p, ok := v.(*Panic); ok {
		return refText(p.Value)
	}
	return fmt.Sprint(v)
}

// refReport is report as it was before binsize BS2, with fmt: the
// reference of TestReportMatchesFmt.
func refReport(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var reasons []rt.Reason
	switch e := err.(type) {
	case *rt.Cause:
		reasons = e.Reasons
	default:
		if err == rt.Interrupted {
			reasons = []rt.Reason{{Kind: rt.KindInterruption}}
		} else {
			reasons = []rt.Reason{{Kind: rt.KindFailure, Err: err}}
		}
	}
	if len(reasons) == 1 && reasons[0].Kind == rt.KindFailure {
		if x, ok := reasons[0].Err.(Exit); ok {
			fmt.Fprintln(w, x.Message)
			if x.Code >= 1 && x.Code <= 125 {
				return x.Code
			}
			fmt.Fprintf(w, "error: os.Exit code %d is outside 1..125\n", x.Code)
			return 1
		}
	}
	defect, interruptedOnly := false, true
	for _, r := range reasons {
		switch r.Kind {
		case rt.KindFailure:
			interruptedOnly = false
			fmt.Fprintf(w, "error: %v\n", r.Err)
		case rt.KindDefect:
			defect, interruptedOnly = true, false
			where := ""
			if r.InRelease {
				where = " (in an exit action)"
			}
			fmt.Fprintf(w, "panic: %s%s\n", refText(r.Value), where)
			p, _ := r.Value.(*Panic)
			var stack []byte
			var context []Binding
			if p != nil {
				stack, context = p.Stack, p.Context
			}
			if len(r.Chain) > 1 || len(context) > 0 {
				for i, f := range r.Chain {
					fmt.Fprintf(w, "\ttask #%d %s\n", f.Task, f.Span)
					if i == 0 && len(context) > 0 {
						fmt.Fprint(w, "\t  context")
						for _, b := range context {
							fmt.Fprintf(w, " %s.%s=%s", b.Package, b.Key, strconv.Quote(b.Text))
						}
						fmt.Fprintln(w)
					}
				}
				if r.ChainCut {
					fmt.Fprintf(w, "\t%s\n", rt.ChainCutLine)
				}
			}
			if len(stack) > 0 {
				fmt.Fprintf(w, "\n%s\n", stack)
			}
		case rt.KindInterruption:
			fmt.Fprintln(w, "interrupted")
		}
	}
	switch {
	case defect:
		return 2
	case interruptedOnly:
		return 130
	default:
		return 1
	}
}

// refCauseError is rt's Cause.Error as it was before binsize BS2.
func refCauseError(c *rt.Cause) string {
	var b bytes.Buffer
	for i, r := range c.Reasons {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch r.Kind {
		case rt.KindFailure:
			fmt.Fprintf(&b, "failure: %v", r.Err)
		case rt.KindDefect:
			if r.InRelease {
				fmt.Fprintf(&b, "defect in release: %s", refText(r.Value))
			} else {
				fmt.Fprintf(&b, "defect: %s", refText(r.Value))
			}
			for _, f := range r.Chain {
				fmt.Fprintf(&b, "\n  task #%d %s", f.Task, f.Span)
			}
			if r.ChainCut {
				fmt.Fprintf(&b, "\n  %s", rt.ChainCutLine)
			}
		case rt.KindInterruption:
			b.WriteString("interrupted")
		}
	}
	return b.String()
}

// reportErr is an error with a value receiver, whose Error may panic.
type reportErr struct {
	text   string
	panics any
}

func (e reportErr) Error() string {
	if e.panics != nil {
		panic(e.panics)
	}
	return e.text
}

// nilReportErr is an error whose Error reads its receiver; a nil one
// panics in it.
type nilReportErr struct{ text string }

func (e *nilReportErr) Error() string { return e.text }

// genReportValue is a defect's or failure's value: a text, a number, a
// boolean, nil, an error (one that panics in Error, a nil pointer one, a Go
// runtime error, an Exit) or a Stringer.
func genReportValue(r *rand.Rand) any {
	switch r.IntN(12) {
	case 0:
		return nil
	case 1:
		return runeText(r)
	case 2:
		return r.IntN(2) == 0
	case 3:
		return []any{int(r.Int64()), int8(r.Int64()), int32(r.Int64()), r.Int64() - r.Int64()}[r.IntN(4)]
	case 4:
		return []any{uint(r.Uint64()), uint8(r.Uint64()), r.Uint64(), uintptr(r.Uint64())}[r.IntN(4)]
	case 5:
		return []any{float32(anyFloat(r)), anyFloat(r)}[r.IntN(2)]
	case 6:
		return errors.New(runeText(r))
	case 7:
		var panics any
		if r.IntN(2) == 0 {
			panics = []any{runeText(r), r.IntN(9)}[r.IntN(2)]
		}
		return reportErr{runeText(r), panics}
	case 8:
		return []any{(*nilReportErr)(nil), &nilReportErr{runeText(r)}}[r.IntN(2)]
	case 9:
		var xs []int
		return func() (p any) { defer func() { p = recover() }(); _ = xs[r.IntN(3)]; return nil }()
	case 10:
		return Exit{Code: r.IntN(300) - 50, Message: runeText(r)}
	default:
		return &MachineTimerDefect{Machine: runeText(r), State: runeText(r), In: time.Duration(r.Int64N(2e10) - 1e10)}
	}
}

// genReason is one reason of a cause: a failure, an interruption or a
// defect whose value is raw or a *Panic, with a chain, context values
// with any text, a stack, a cut chain and a release.
func genReason(r *rand.Rand) rt.Reason {
	switch r.IntN(3) {
	case 0:
		v, _ := genReportValue(r).(error)
		return rt.Reason{Kind: rt.KindFailure, Err: v}
	case 1:
		return rt.Reason{Kind: rt.KindInterruption}
	}
	value := genReportValue(r)
	if r.IntN(3) > 0 {
		p := &Panic{Value: value}
		if r.IntN(2) == 0 {
			p.Stack = []byte(runeText(r) + "\n")
		}
		for range r.IntN(3) {
			p.Context = append(p.Context, Binding{Package: runeText(r), Key: runeText(r), Text: runeText(r)})
		}
		value = p
	}
	reason := rt.Reason{Kind: rt.KindDefect, Value: value, ChainCut: r.IntN(4) == 0, InRelease: r.IntN(3) == 0}
	for range r.IntN(4) {
		reason.Chain = append(reason.Chain, rt.Frame{Task: r.Uint64(), Span: runeText(r)})
	}
	return reason
}

// genExitErr is main's exit: nil, an interruption, a lone failure (an
// Exit among them) or a cause of one to four reasons.
func genExitErr(r *rand.Rand) error {
	switch r.IntN(5) {
	case 0:
		return nil
	case 1:
		return rt.Interrupted
	case 2:
		if err, ok := genReportValue(r).(error); ok {
			return err
		}
		return Exit{Code: r.IntN(300) - 50, Message: runeText(r)}
	}
	c := &rt.Cause{}
	for range 1 + r.IntN(4) {
		c.Reasons = append(c.Reasons, genReason(r))
	}
	return c
}

// TestReportMatchesFmt (binsize BS2): report writes main's exit, and rt's
// Cause.Error a cause, byte for byte as the fmt formatter they replaced
// (refReport, refCauseError), with the same status, over generated exits:
// typed failures and defects of every value kind (an error that panics in
// Error, a nil one, a Go runtime error, floats of every class, texts that
// are not UTF-8), interruptions, Exit with any code, chains, cut chains,
// context values and stacks. It runs without the gofmt leaf's host text,
// as a program that does not import go does. A failing case prints both
// outputs; the generator is seeded, so the run replays it.
// Mutant: bs2-status-line (the os.Exit line loses its code).
func TestReportMatchesFmt(t *testing.T) {
	prev := rt.UseHostText(nil)
	defer rt.UseHostText(prev)
	saved := registered
	registered = false
	defer func() { registered = saved }()
	r := rand.New(rand.NewPCG(5, 23))
	for i := range 5000 {
		err := genExitErr(r)
		var got, want bytes.Buffer
		gs, ws := report(&got, err), refReport(&want, err)
		if gs != ws || got.String() != want.String() {
			t.Fatalf("case %d (replay: go test -run TestReportMatchesFmt ./runtime/v1): %#v\nreport %d %q\nfmt    %d %q", i, err, gs, got.String(), ws, want.String())
		}
		if c, ok := err.(*rt.Cause); ok {
			if got, want := c.Error(), refCauseError(c); got != want {
				t.Fatalf("case %d (replay: go test -run TestReportMatchesFmt ./runtime/v1): Cause.Error\nnew %q\nfmt %q", i, got, want)
			}
		}
	}
}

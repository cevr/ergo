package v1

import (
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"testing"
)

var goErrorSeed = flag.Uint64("goerror.seed", 0, "run TestGoErrorChainsModel on this seed alone")

// Leaves and wrappers of the generated chains: each kind errors.Is and
// errors.As treat differently.
type (
	valErr  struct{ code int }   // a comparable value: Is compares by ==
	ptrErr  struct{ msg string } // a pointer: Is compares identity
	wrapErr struct {             // a custom Unwrap() error
		msg   string
		inner error
	}
	multiErr struct{ errs []error } // a custom Unwrap() []error
	mutErr   struct{ msg string }   // its text changes after the failure
)

func (e valErr) Error() string { return fmt.Sprintf("val %d", e.code) }

// Error is nil-safe, so a typed nil's text is defined (Go calls it).
func (e *ptrErr) Error() string {
	if e == nil {
		return "<nil ptrErr>"
	}
	return "ptr " + e.msg
}
func (e *wrapErr) Error() string    { return e.msg + ": " + e.inner.Error() }
func (e *wrapErr) Unwrap() error    { return e.inner }
func (e *multiErr) Error() string   { return fmt.Sprintf("multi %d", len(e.errs)) }
func (e *multiErr) Unwrap() []error { return e.errs }
func (e *mutErr) Error() string     { return e.msg }

// Two sentinels with one text: go.Is must tell them apart.
var sameA, sameB = errors.New("same"), errors.New("same")

// genChain is a generated Go error chain of depth at most d, and every
// error in it (the targets go.Is is asked about).
func genChain(r *rand.Rand, d int, nodes *[]error) error {
	var e error
	k := r.IntN(10)
	if d <= 0 {
		k %= 5
	}
	switch k {
	case 0:
		e = []error{sameA, sameB}[r.IntN(2)]
	case 1:
		e = valErr{code: r.IntN(3)}
	case 2:
		e = &ptrErr{msg: fmt.Sprint(r.IntN(3))}
	case 3:
		e = (*ptrErr)(nil) // a typed nil: an error, as Go says
	case 4:
		e = GoErrorText(fmt.Sprint("t", r.IntN(2))).Native()
	case 5:
		e = fmt.Errorf("w: %w", genChain(r, d-1, nodes))
	case 6:
		e = fmt.Errorf("%w + %w", genChain(r, d-1, nodes), genChain(r, d-1, nodes))
	case 7:
		e = errors.Join(genChain(r, d-1, nodes), genChain(r, d-1, nodes))
	case 8:
		e = &wrapErr{msg: "wrap", inner: genChain(r, d-1, nodes)}
	default:
		e = &multiErr{errs: []error{genChain(r, d-1, nodes), genChain(r, d-1, nodes)}}
	}
	*nodes = append(*nodes, e)
	return e
}

// asAgrees reports whether GoAs[T] of g finds what errors.As finds in err.
func asAgrees[T comparable](g GoError, err error) (bool, string) {
	got, gok := GoAs[T](g)
	var want T
	wok := errors.As(err, &want)
	if gok != wok || got != want {
		var zero T
		return false, fmt.Sprintf("GoAs[%T] = (%v, %v), errors.As = (%v, %v)", zero, got, gok, want, wok)
	}
	return true, ""
}

// TestGoErrorChainsModel is the in-process property of the native
// go.Error (IG5): on seeded Go error chains (fmt.Errorf %w with one and two
// operands, errors.Join, a custom Unwrap() error and Unwrap() []error,
// comparable values, pointers, a typed nil, two sentinels with one text,
// an Ergo-made go.Error), in both lowerings (GoErrorOf, and the eager
// reference GoErrorEagerOf): the failure is nil exactly when Go's error is
// nil (a typed nil fails); Text is err.Error(); go.Is of every error of
// the chain and of the sentinels is errors.Is; go.As into a pointer, a
// value, a wrapper and an interface is errors.As; Native is err. Go's own
// errors package is the model. -goerror.seed=N replays one seed.
//
// Mutants: go.Is compares Text (the two "same" sentinels); go.As reads
// the top error only (a wrapped ptrErr); a typed nil treated as nil;
// Native drops the eager wrapper's error. TestGoErrorTextIsRead holds the
// lazy read.
func TestGoErrorChainsModel(t *testing.T) {
	seeds := make([]uint64, 200)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if *goErrorSeed != 0 {
		seeds = []uint64{*goErrorSeed}
	}
	type mode struct {
		name string
		of   func(error) error
		from func(error) GoError
	}
	modes := []mode{{"lazy", GoErrorOf, GoErrorFrom}, {"eager", GoErrorEagerOf, GoErrorEagerFrom}}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6e7))
		var nodes []error
		err := genChain(r, 3, &nodes)
		targets := append(nodes, sameA, sameB, valErr{code: 0}, valErr{code: 9}, &ptrErr{msg: "0"}, GoErrorText("t0").Native())
		for _, m := range modes {
			fail := func(format string, args ...any) {
				t.Helper()
				t.Errorf("seed %d (%s; replay -goerror.seed=%d), chain %q: "+format, append([]any{seed, m.name, seed, err.Error()}, args...)...)
			}
			f := m.of(err)
			if f == nil {
				fail("the failure is nil")
				continue
			}
			g, ok := f.(GoError)
			if !ok {
				fail("the failure is %T, not GoError", f)
				continue
			}
			if g.Text() != err.Error() {
				fail("Text %q, want %q", g.Text(), err.Error())
			}
			if g.Native() != err {
				fail("Native is not Go's error")
			}
			for i, tgt := range targets {
				if got, want := GoIs(g, m.from(tgt)), errors.Is(err, tgt); got != want {
					fail("go.Is(target %d %q) = %v, errors.Is = %v", i, tgt.Error(), got, want)
				}
			}
			for _, check := range []func(GoError, error) (bool, string){
				asAgrees[*ptrErr], asAgrees[valErr], asAgrees[*mutErr], asAgrees[*wrapErr],
				asAgrees[interface{ Unwrap() []error }],
			} {
				if ok, why := check(g, err); !ok {
					fail("%s", why)
				}
			}
		}
	}
	if GoErrorOf(nil) != nil || GoErrorEagerOf(nil) != nil {
		t.Error("a nil Go error is a failure")
	}
	var typedNil *ptrErr
	if GoErrorOf(typedNil) == nil || GoErrorEagerOf(typedNil) == nil {
		t.Error("a typed nil error is no failure; Go's err != nil says it is one")
	}
}

// TestGoErrorTextIsRead holds the lazy go.Error: Text is the error's
// Error() when it is read, not a copy taken at the failure (the eager
// reference takes one; generated chains are immutable, so the property
// cannot see the difference). Mutant: GoErrorOf computes the text at the
// failure (the stale copy).
func TestGoErrorTextIsRead(t *testing.T) {
	m := &mutErr{msg: "before"}
	g := GoErrorOf(m).(GoError)
	m.msg = "after"
	if g.Text() != "after" {
		t.Errorf("Text %q, want the error's Error() at the read, %q", g.Text(), "after")
	}
	if (GoError{}).Text() != "" || (GoError{}).Native() != nil {
		t.Error("the zero go.Error is not go.Error{}")
	}
}

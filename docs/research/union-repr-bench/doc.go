// Package unionreprbench holds hand-written Go for the union-repr research
// (docs/research/union-repr.md, plan wf10 M2). It measures how a closed
// Ergo union can lower to Go. It is measurement only: nothing here is
// compiler or runtime code, and the root module's ./... does not reach it
// (it is its own module).
//
// The forms (plan §2.3):
//
//   - A: a sealed interface with an unexported marker method; each variant
//     is its own struct. This is what the lowering emits today
//     (internal/sketch/lower/decls.go, kindUnion).
//   - B: an enum. Every variant is empty, so the union is a uint8 tag.
//   - C: the option shape. One payload variant and one empty variant:
//     struct{ v T; ok bool }, Go's comma-ok form.
//   - D: flat typed slots. struct{ tag uint8; slots }, where a slot holds a
//     field of one Go type and variants share a slot when their fields have
//     the same Go type. An inactive slot holds its zero value.
//   - Dx: form D, and a match on two subjects is one switch on a combined
//     index (s.tag*|E| + e.tag).
//   - E: a tag and a boxed payload: struct{ tag uint8; p any }.
//   - F: form A plus a tag method; dispatch is a switch on the method's
//     result, then a type assertion to narrow.
//   - G: the hybrid: B for an all-empty union, D for the others.
//
// Tags start at 1 in every tagged form. The zero value of a tagged form has
// tag 0, which no constructor builds, so a match on it reaches the no-case
// defect, as a nil interface does in form A ("a union has no zero value",
// docs/spec/types.md, Unions 3).
//
// Every function a benchmark measures is marked //go:noinline, so that each
// form pays one call, as a lowered Ergo function does when it is not
// inlined. The contract tests (contract_test.go) check that every form
// computes the same results as form A, and that every form D value keeps
// its inactive slots zero.
package unionreprbench

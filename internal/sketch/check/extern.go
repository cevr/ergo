package check

// The host edge (check-v1 stage E4, 196x; D-E5). An `extern go path.F func
// Name(sig)` declaration binds a Go function. The checker trusts its
// written signature and does not resolve the Go target (D-E5): go build
// does.
//
// An extern function is call-only (P3; lower-v1 Q2). Any use of it other
// than a direct call is S108 with externCallOnlyText, at the reference:
// ident, qualified and explicit give it modeExtern, which only callOf
// accepts and needValue refuses. The rule keys on the declaration
// (isExtern), in every package, std included, so a host binding never
// escapes its call site and scope.OnExit always runs in its source frame.
//
// Prior art: Gleam's @external and ReScript's external trust a written
// type; cgo functions and Go's built-ins are call-only; Elixir makes a
// function value only by an explicit capture (&:math.sqrt/1), as P3's
// literal wrapper does.

// externCallOnlyText is the P3 message (check-v1 spec add-on), quoted
// exactly.
const externCallOnlyText = "an extern function is called directly; wrap it in a literal: func() { f() }"

// isExtern reports an extern go function, in any package.
func isExtern(f *Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern != nil
}

// externRef is the operand of a reference to the extern f: call-only
// (modeExtern), so only callOf accepts it.
func externRef(f *Func, sig *Signature) operand {
	return operand{mode: modeExtern, typ: sig, fn: f}
}

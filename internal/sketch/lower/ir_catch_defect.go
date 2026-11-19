package lower

// IR builder: the default of a full catch (FAIL-ELIDE). A catch without
// try handles every error of its call's row (rows.md, Catch 4; the
// checker's S103), so its type switch's default is reached only if that
// proof is wrong. The emitter's default panics with the failure itself,
// and that panic is the failure value's only escape: Go builds it on the
// heap at every failing call, even when no arm reads it (strconv's
// ParseBool and Unquote with `case Malformed: false`).
//
// The IR's default panics with the unreachable defect at the catch
// instead, naming no failure. A failure no arm binds then does not escape,
// and Go builds it on the stack once it inlines the callee, or not at all.
// An arm that reads it (`as e` read past the switch) still lets it escape,
// as a hand-written Go program does.
//
// The reference is irCatchDefectLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irCatchDefectLowering=off):
// the default panics with the failure, as the emitter's does.

import "ergo.local/prototype/internal/sketch/syntax"

// irCatchDefectLowering is "off" for the reference: a full catch's default
// panics with the failure.
var irCatchDefectLowering string

// IRCatchDefectOn reports that a full catch's default names no failure
// (the property's reach check).
func IRCatchDefectOn() bool { return irCatchDefectLowering != "off" }

// catchDefault is the default arm of a catch's type switch on the error
// local e, at p: the failure itself for a partial catch (try … catch
// returns it, or panics with it in an infallible frame), else the
// unreachable defect at the catch x.
func (b *irBuilder) catchDefault(x *syntax.CatchExpr, k string, partial bool, e int, p irPos) *irStmt {
	if partial || irCatchDefectLowering == "off" {
		return &irStmt{K: k, Pos: p, X: &irExpr{K: "local", Pos: p, I: e}}
	}
	b.st.report.CatchDefects++
	b.keepImport("var _ = " + b.l.rt() + ".UnreachableAt")
	args := []*irExpr{
		{K: "const", Pos: p, T: "string", V: "a failure no catch arm takes"},
		{K: "const", Pos: p, T: "string", V: b.l.siteText(x)},
	}
	return &irStmt{K: "panic", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "UnreachableAt", Args: args}}
}

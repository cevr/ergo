package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestStdBodyErrorIsAnICE (fu-lower): a std extern that binds a runtime/v1
// body which v1.BodyOf cannot read (missing, or a pure mark the reviewed
// rules refuse) is an internal inconsistency of the compiler's own std
// and runtime, not a form of the user's program. So stdBody stops with an
// internal compiler error that carries BodyOf's error, never a silent "no
// std body" that a later site reports with the cause lost.
func TestStdBodyErrorIsAnICE(t *testing.T) {
	fd := &funcDecl{
		fn:      &check.Func{Name: "Ghost", Decl: &syntax.FuncDecl{}},
		extern:  runtimeImport + ".NoSuchBodyForFuLower",
		library: true,
	}
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				ref, ok := r.(refusal)
				if !ok {
					panic(r)
				}
				msg = ref.msg
			}
		}()
		(&lowerer{}).stdBody(fd)
	}()
	if msg == "" {
		t.Fatal("stdBody of a std extern whose runtime/v1 body is missing returned, want an internal compiler error")
	}
	_, bodyErr := v1.BodyOf("NoSuchBodyForFuLower")
	if bodyErr == nil {
		t.Fatal("v1.BodyOf(NoSuchBodyForFuLower) gave no error")
	}
	for _, want := range []string{"internal compiler error", "Ghost", bodyErr.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("stdBody's refusal %q does not name %q", msg, want)
		}
	}
}

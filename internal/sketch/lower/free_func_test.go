package lower

import (
	"strings"
	"testing"
)

// TestRuntimeRecordFunctionIsFree is the smallest repro of the rpc.Malformed
// bug (STD-NOPRIV inbox, 2026-11-13, from FK's FD1-g2): a std function
// whose first parameter is a std record bound to a runtime struct
// (rpc.Malformed is v1.RPCMalformed, v1.StdRecord) lowered as a Go method
// on that record. The program then declared a struct of its own for the
// record, and called the method on the runtime's Go type, which has no such
// method and is not that struct, so the Go did not compile. A Go method
// can only be declared in the package that declares its receiver, and the
// runtime declares this one, so the function stays a Go function (types.md,
// Methods 3: a Go method "when Go can express it"). Mutant
// method-on-runtime-record (methodReceiver does not ask runtimeType)
// lowers the method again, and go/types refuses the program.
func TestRuntimeRecordFunctionIsFree(t *testing.T) {
	const fixture = `type Malformed { Reason string }

func Describe(m Malformed) string {
	"malformed: " + m.Reason
}
`
	// The fixture std package binds its record through rpc.Malformed's row
	// of the binding table, v1.RPCMalformed (a struct with Reason).
	old := stdRecord
	defer func() { stdRecord = old }()
	stdRecord = func(pkg, name string) (string, bool) {
		if pkg == "fixturerpc" && name == "Malformed" {
			return "RPCMalformed", true
		}
		return old(pkg, name)
	}
	imp := pkgImporter{"fixturerpc": stdFixture(t, "fixturerpc", fixture)}
	prog := lowerSourceWith("runtime_record_free", `import fixturerpc

func main() needs Console {
	Console.Println(fixturerpc.Describe(fixturerpc.Malformed{Reason: "x"}))
}
`, true, imp)
	if prog.err != nil || len(prog.diags) > 0 || prog.panicked != nil {
		t.Fatalf("lower: %v %v %v", prog.err, prog.diags, prog.panicked)
	}
	if strings.Contains(prog.mainGo(), ").Describe(") {
		t.Errorf("rpc.Describe lowered as a Go method on the runtime's record:\n%s", prog.mainGo())
	}
	typecheckGo(t, []*lowered{prog})
}

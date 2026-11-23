package lower

import (
	_ "embed"
	"go/token"
	"testing"
)

//go:embed testdata/qualified-failure-field.ergo
var qualifiedFailureField string

// TestQualifiedFailureField is the repro for qualified-text-helper-name:
// the checker accepts a failure with an imported runtime type as a field,
// and the Go of both lowerings must type-check.
func TestQualifiedFailureField(t *testing.T) {
	// The agreement case also returns a failure value. Its local GoError
	// has the same leaf spelling as the runtime type but needs its own
	// helper for a different Go parameter type.
	for _, c := range []struct{ name, src string }{
		{"repro", qualifiedFailureField},
		{"agreement", `type GoError { Code int }
type Bad { Cause go.Error, Detail GoError }

func main() fails Bad {
	return Bad{Cause: go.Error{Text: "missing"}, Detail: GoError{Code: 1}}
}
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := factsUnit(t, c.src)
			t.Run("text", func(t *testing.T) {
				typecheckGo(t, []*lowered{mustLowerUnit(t, c.name+"_text", u, false)})
			})
			t.Run("ir", func(t *testing.T) {
				files, report, err := LowerIR(u)
				if err != nil {
					t.Fatal(err)
				}
				if len(report.IR) == 0 || len(report.Fallback) != 0 {
					t.Fatalf("want an IR body and no fallback: bodies %v, fallback %v", report.IR, report.Fallback)
				}
				typecheckGo(t, []*lowered{{name: c.name + "_ir", src: c.src, files: files}})
				imp, err := goExportImporter(t, token.NewFileSet(), []string{runtimeImport, taskImport})
				if err != nil {
					t.Fatal(err)
				}
				printed, err := IRGoText(files, imp)
				if err != nil {
					t.Fatal(err)
				}
				typecheckGo(t, []*lowered{{name: c.name + "_printed_ir", src: c.src, files: printed}})
			})
		})
	}
}

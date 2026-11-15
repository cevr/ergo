package lint

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func testPack(check func(*Pass) error) *Pack {
	return &Pack{
		Namespace: "acme", Version: "1.0.0", FactVersions: []int{FactSchemaVersion},
		Rules: []*Rule{{
			Name: "no-op", Version: "1", Description: "test rule", DefaultSeverity: SeverityWarning,
			Requires: []Family{FamilyCallables},
			Options: []OptionSpec{
				{Name: "label", Type: OptionString, Enum: []string{"a", "b"}, Default: json.RawMessage(`"a"`)},
			},
			Check: check,
		}},
	}
}

func testManifest(t *testing.T, pack *Pack) Manifest {
	t.Helper()
	manifest, err := pack.Manifest(Executable{Path: "bin/acme-lint"})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// mainSpan is main's name in the 20-byte test source "f main() {}\n...".
var mainSpan = Span{File: "main.ergo", Offset: 2, Length: 4, Line: 1, Column: 3}

func testSnapshot() *Snapshot {
	return &Snapshot{
		Schema:    Schema{Name: FactSchemaName, Version: FactSchemaVersion},
		Semantic:  Semantic{Revision: "r1", Target: "go"},
		Sources:   []Source{{File: "main.ergo", Bytes: 20}},
		Checked:   true,
		Families:  []Family{FamilyDeclarations, FamilyCallables},
		Callables: []Callable{{Identity: "func:main.main", Package: "main", Name: "main", Kind: CallableFunction, Span: mainSpan}},
	}
}

func configure(t *testing.T, registry *Registry, document string) *Configuration {
	t.Helper()
	config, err := ParseConfig([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	configuration, problems := registry.Configure(config)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return configuration
}

func reportMain(pass *Pass) error {
	pass.Reportf(pass.Snapshot.FunctionNamed("main.main")[0].Span, "label %s", pass.Options.String("label"))
	return nil
}

// TestEvaluateStatuses is the rule status model of one pack: completed with
// its configured severity and options, off, skipped for an unavailable
// family, failed for an unsupported schema, and failed, with none of its
// findings, for each way a rule's own output is invalid. Mutants:
// lint3-sdk-panic-completes, lint3-sdk-unavailable-runs.
func TestEvaluateStatuses(t *testing.T) {
	evaluate := func(t *testing.T, check func(*Pass) error, document string, snapshot *Snapshot) Report {
		t.Helper()
		pack := testPack(check)
		registry, err := NewRegistry(testManifest(t, pack))
		if err != nil {
			t.Fatal(err)
		}
		report, err := Evaluate(pack, configure(t, registry, document), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	report := evaluate(t, reportMain, `{"version":1,"rules":{"acme/no-op":{"severity":"error","options":{"label":"b"}}}}`, testSnapshot())
	if !report.Complete || report.Rules[0].Status != StatusCompleted || len(report.Findings) != 1 || !reflect.DeepEqual(report.Findings[0], ReportedFinding{Rule: "acme/no-op", Severity: SeverityError, Message: "label b", Span: mainSpan}) {
		t.Fatalf("completed: %+v", report)
	}
	if report := evaluate(t, reportMain, `{"version":1,"rules":{"acme/no-op":"off"}}`, testSnapshot()); !report.Complete || report.Rules[0].Status != StatusOff || len(report.Findings) != 0 {
		t.Fatalf("off: %+v", report)
	}
	unchecked := testSnapshot()
	unchecked.Checked, unchecked.Families = false, nil
	unchecked.Unavailable = []UnavailableFamily{{FamilyCallables, ReasonUncheckedSource}}
	if report := evaluate(t, reportMain, `{"version":1}`, unchecked); report.Complete || report.Rules[0].Status != StatusSkipped || report.Rules[0].Reason != "facts-unavailable: callables (unchecked-source)" {
		t.Fatalf("unavailable: %+v", report)
	}
	future := testSnapshot()
	future.Schema.Version = FactSchemaVersion + 1
	if report := evaluate(t, reportMain, `{"version":1}`, future); report.Complete || report.Rules[0].Status != StatusFailed || !strings.Contains(report.Rules[0].Reason, "unsupported fact schema") {
		t.Fatalf("schema: %+v", report)
	}
	at := func(offset, length int) Span {
		return Span{File: "main.ergo", Offset: offset, Length: length, Line: 1, Column: offset + 1}
	}
	failures := map[string]func(*Pass) error{
		"panicked":            func(*Pass) error { panic("boom") },
		"outside the 20-byte": func(pass *Pass) error { pass.Reportf(at(18, 5), "past EOF"); return nil },
		"names no admitted": func(pass *Pass) error {
			pass.Reportf(Span{File: "other.ergo", Line: 1, Column: 1}, "elsewhere")
			return nil
		},
		"non-empty UTF-8":         func(pass *Pass) error { pass.Reportf(at(0, 0), ""); return nil },
		"configured type missing": func(*Pass) error { return errorString("configured type missing") },
		"the limit is 1000": func(pass *Pass) error {
			for i := 0; i <= MaxFindingsPerRule; i++ {
				pass.Reportf(at(0, 0), "finding %d", i)
			}
			return nil
		},
	}
	for want, check := range failures {
		report := evaluate(t, check, `{"version":1}`, testSnapshot())
		if report.Complete || report.Rules[0].Status != StatusFailed || !strings.Contains(report.Rules[0].Reason, want) || len(report.Findings) != 0 {
			t.Fatalf("%s: %+v", want, report)
		}
	}
}

// TestApplyValidatesAgainstTheAdmittedSource: a rule cannot authorize its
// own output by enlarging the source bound in its snapshot; findings are
// validated against the admitted sources. Mutant:
// lint3-sdk-bounds-from-pass.
func TestApplyValidatesAgainstTheAdmittedSource(t *testing.T) {
	outside := Span{File: "main.ergo", Offset: 500, Length: 1, Line: 1, Column: 501}
	pack := testPack(func(pass *Pass) error {
		pass.Snapshot.Sources[0].Bytes = 10_000
		pass.Reportf(outside, "outside")
		return nil
	})
	registry, err := NewRegistry(testManifest(t, pack))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Evaluate(pack, configure(t, registry, `{"version":1}`), testSnapshot())
	if err != nil || report.Complete || report.Rules[0].Status != StatusFailed || !strings.Contains(report.Rules[0].Reason, "outside the 20-byte source") || len(report.Findings) != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// TestSDKDoesNotImportCompilerInternals: the SDK and the example pack are
// consumed by independent rule authors; they depend on the public fact
// schema, never on compiler internals. Mutant: lint3-sdk-imports-internal.
func TestSDKDoesNotImportCompilerInternals(t *testing.T) {
	for _, pattern := range []string{"*.go"} {
		files, err := filepath.Glob(pattern)
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", pattern, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range parsed.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				if strings.Contains(path, "/internal/") || strings.HasSuffix(path, "/internal") {
					t.Fatalf("%s imports %s", file, path)
				}
			}
		}
	}
}

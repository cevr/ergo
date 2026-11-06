package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// TestReportOrdersFindingsByFileThenOffset (docs/tooling.md, Finding order;
// round 2 M2): for generated diagnostics over several files of a package, in
// any input order, the report lists the located findings first, ordered by
// file, then offset, length, code and message, the located ones before the
// rest; the JSON and the text list them in that one order, and each text
// line names its finding's file. So the findings of two files never
// interleave.
func TestReportOrdersFindingsByFileThenOffset(t *testing.T) {
	files := []string{"a.ergo", "b.ergo", "lib/c.ergo", "lib/z/d.ergo"}
	text := strings.Repeat("abc def\n", 40) // 8 bytes a line
	snapshot := SourceSnapshot{URI: "file:///m", Files: map[string]string{}}
	for _, f := range files {
		snapshot.Files[f] = text
	}
	codes := []string{"S104", "S108", "S110"}
	for seed := int64(1); seed <= 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var in []Diagnostic
		for i := 0; i < 2+rng.Intn(14); i++ {
			d := Diagnostic{Code: codes[rng.Intn(len(codes))], Message: fmt.Sprintf("m%d", rng.Intn(3))}
			if rng.Intn(6) > 0 {
				line := rng.Intn(40)
				col := []int{0, 4}[rng.Intn(2)]
				d.File = files[rng.Intn(len(files))]
				d.Span = Span{Offset: line*8 + col, Length: 3, Line: line + 1, Column: col + 1}
			}
			in = append(in, d)
		}
		rng.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
		report := NewResult("", Target, in).Report(snapshot, false)
		got := report.Diagnostics
		for i := 1; i < len(got); i++ {
			if before(got[i], got[i-1]) {
				t.Fatalf("seed %d: finding %d %+v is listed after %+v", seed, i, key(got[i]), key(got[i-1]))
			}
		}
		// Every input finding is listed, identical ones once.
		want := map[string]bool{}
		for _, d := range in {
			want[fmt.Sprint(d.Code, d.File, d.Message, d.Span.Offset, d.Span.Length)] = true
		}
		if len(got) != len(want) {
			t.Fatalf("seed %d: %d findings for %d distinct diagnostics", seed, len(got), len(want))
		}
		// The JSON lists the same order.
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct{ Diagnostics []DiagnosticFinding }
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		for i := range got {
			if key(decoded.Diagnostics[i]) != key(got[i]) {
				t.Fatalf("seed %d: JSON finding %d is %v, report %v", seed, i, key(decoded.Diagnostics[i]), key(got[i]))
			}
		}
		// The text lists the same order and names each finding's file.
		var out bytes.Buffer
		if err := report.WriteText(&out, "pkg"); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != len(got) {
			t.Fatalf("seed %d: %d text lines for %d findings", seed, len(lines), len(got))
		}
		for i, f := range got {
			name := f.File
			if name == "" {
				name = "pkg"
			}
			wantLine := fmt.Sprintf("%s:%d:%d: error %s: %s", name, f.LSP.Range.Start.Line+1, f.LSP.Range.Start.Character+1, f.Code, f.Message)
			if lines[i] != wantLine {
				t.Fatalf("seed %d: text line %d is %q, want %q", seed, i, lines[i], wantLine)
			}
			if f.File != "" && (!f.LocationAvailable || f.LSP.Range.Start.Line != f.Span.Line-1) {
				t.Fatalf("seed %d: finding %+v is not located in its file", seed, key(f))
			}
		}
	}
}

type findingKey struct {
	Located        bool
	File           string
	Offset, Length int
	Code, Message  string
}

func key(f DiagnosticFinding) findingKey {
	return findingKey{f.LocationAvailable, f.File, f.Span.Offset, f.Span.Length, f.Code, f.Message}
}

// before is the stated order (docs/tooling.md, Finding order), written
// from the documentation, not from the implementation.
func before(x, y DiagnosticFinding) bool {
	a, b := key(x), key(y)
	switch {
	case a.Located != b.Located:
		return a.Located
	case a.File != b.File:
		return a.File < b.File
	case a.Offset != b.Offset:
		return a.Offset < b.Offset
	case a.Length != b.Length:
		return a.Length < b.Length
	case a.Code != b.Code:
		return a.Code < b.Code
	}
	return a.Message < b.Message
}

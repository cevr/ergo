package selfhostcheck

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/report"
)

// TestReportAgreesWithGo holds the driver's report written in Ergo
// (compiler/report, linked as internal/linked/compiler through
// compiler/api/report.ergo) to internal/report, the reference until the
// S5 swap: the findings `ergo diagnostics` projects (dedup, the file a
// span lies in, the UTF-16 LSP range, the order) and the lines WriteText
// prints, the revision, the evaluation status sum, and the EF110 refusals.
//
// Inputs are seeded: texts of ASCII, two-, three- and four-byte runes, LF,
// CR, CRLF and invalid bytes; diagnostics whose spans start and end on rune
// boundaries, inside a rune, between a CR and its LF, at the end, past the
// end or negative, the zero span, duplicates, and files the snapshot holds
// and does not hold. A failing case prints its seed and the replay command.
//
// Mutants it alone catches: testdata/mutants.json, killer
// selfhostcheck:TestReportAgreesWithGo.
func TestReportAgreesWithGo(t *testing.T) {
	seed := selfhostSeed(t)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestReportAgreesWithGo$' ./internal/selfhostcheck"
	rng := rand.New(rand.NewPCG(seed, 0x5e5))
	ctx := context.Background()
	failed := 0
	located, unlocated := 0, 0
	for c := 0; c < 400; c++ {
		in := reportInput(rng)
		want, l, u := goReport(in)
		got := must(t)(linked.ReportProject(ctx, "src.ergo", in.text, in.files, in.strs, in.nums))
		if got != want {
			failed++
			if failed <= 3 {
				line, g, w := firstDiff(got, want)
				t.Errorf("seed %d case %d: Ergo report differs at line %d\n  got:  %q\n  want: %q\ntext: %q\nfiles: %q\nstrs: %q\nnums: %v\n%s", seed, c, line, g, w, in.text, in.files, in.strs, in.nums, replay)
			}
		}
		located += l
		unlocated += u
		if g, w := must(t)(linked.ReportRevision(ctx, in.text)), report.Revision(in.text); g != w {
			t.Errorf("seed %d case %d: revision %q, want %q\n%s", seed, c, g, w, replay)
		}
	}
	if located == 0 || unlocated == 0 {
		t.Fatalf("seed %d: the inputs located %d findings and left %d without a location: both kinds must be reached\n%s", seed, located, unlocated, replay)
	}
	statuses := []string{"passed", "skipped", "incomplete", "not run", "refuted", "defect", "bogus"}
	for c := 0; c < 200; c++ {
		var xs []string
		for range rng.IntN(4) {
			xs = append(xs, statuses[rng.IntN(len(statuses))])
		}
		if g, w := must(t)(linked.ReportStatus(ctx, xs)), report.EvaluationStatus(xs); g != w {
			t.Errorf("status of %q: %q, want %q", xs, g, w)
		}
	}
	for _, target := range []string{"go", "js", "", "Go", "wasm"} {
		want := ""
		if d, refused := report.UnsupportedTarget(target); refused {
			want = d.Code + " " + d.Message
		}
		if g := must(t)(linked.ReportTarget(ctx, target)); g != want {
			t.Errorf("target %q: %q, want %q", target, g, want)
		}
	}
	platforms := []string{"linux/amd64", "darwin/arm64"}
	d := report.UnsupportedPlatform("plan9/386", platforms)
	if g, w := must(t)(linked.ReportPlatform(ctx, "plan9/386", platforms)), d.Code+" "+d.Message; g != w {
		t.Errorf("platform: %q, want %q", g, w)
	}
}

// must is the answer of a linked call, failing t at its error.
func must(t *testing.T) func(string, error) string {
	return func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

type reportCase struct {
	text  string
	files []string
	strs  []string
	nums  []int
}

// reportPieces are the bits a text is made of: every line ending, runes of
// every UTF-8 width (one outside the BMP, two UTF-16 units), and invalid
// bytes (a lone continuation, a truncated sequence).
var reportPieces = []string{"a", "z", " ", "\n", "\r", "\r\n", "é", "€", "𝄞", "\x80", "\xe2\x82", "\t", "ok"}

func reportText(rng *rand.Rand) string {
	var b strings.Builder
	for range rng.IntN(24) {
		b.WriteString(reportPieces[rng.IntN(len(reportPieces))])
	}
	return b.String()
}

func reportInput(rng *rand.Rand) reportCase {
	in := reportCase{text: reportText(rng)}
	texts := map[string]string{"": in.text}
	for i := range rng.IntN(3) {
		name := fmt.Sprintf("p/f%d.ergo", i)
		texts[name] = reportText(rng)
		in.files = append(in.files, name, texts[name])
	}
	codes := []string{"S101", "S110", "EF110", "S108"}
	n := rng.IntN(7)
	for i := 0; i < n; i++ {
		if i > 0 && rng.IntN(5) == 0 { // a duplicate of the one before
			in.strs = append(in.strs, in.strs[len(in.strs)-3:]...)
			in.nums = append(in.nums, in.nums[len(in.nums)-4:]...)
			continue
		}
		file := ""
		switch rng.IntN(4) {
		case 1:
			file = "p/f0.ergo"
		case 2:
			file = "p/missing.ergo"
		}
		size := len(texts[file])
		offset, length := rng.IntN(size+1), 0
		if offset < size {
			length = rng.IntN(size - offset + 1)
		}
		line, column := 1+rng.IntN(3), 1+rng.IntN(5)
		switch rng.IntN(8) {
		case 0:
			offset, length, line, column = 0, 0, 0, 0 // no location
		case 1:
			length = size - offset + 1 + rng.IntN(3) // past the end
		case 2:
			offset = -1
		}
		in.strs = append(in.strs, codes[rng.IntN(len(codes))], file, fmt.Sprintf("message %d é", rng.IntN(3)))
		in.nums = append(in.nums, offset, length, line, column)
	}
	return in
}

// goReport is internal/report's answer in ReportProject's encoding, and
// how many findings it located and left without a location.
func goReport(in reportCase) (string, int, int) {
	var ds []report.Diagnostic
	for k := 0; k*3 < len(in.strs); k++ {
		ds = append(ds, report.Diagnostic{Code: in.strs[k*3], File: in.strs[k*3+1], Message: in.strs[k*3+2], Span: report.Span{Offset: in.nums[k*4], Length: in.nums[k*4+1], Line: in.nums[k*4+2], Column: in.nums[k*4+3]}})
	}
	files := map[string]string{}
	for i := 0; i < len(in.files); i += 2 {
		files[in.files[i]] = in.files[i+1]
	}
	r := report.NewResult(in.text, "go", ds).Report(report.SourceSnapshot{Text: in.text, Files: files}, false)
	field := func(s string) string { return strconv.Itoa(len(s)) + ":" + s }
	var b bytes.Buffer
	located, unlocated := 0, 0
	for _, f := range r.Diagnostics {
		at := 0
		if f.LocationAvailable {
			at = 1
			located++
		} else {
			unlocated++
		}
		g := f.LSP.Range
		fmt.Fprintf(&b, "%s %s %s %d %d %d %d %d %d %d %d %d\n", field(f.Code), field(f.File), field(f.Message), f.Span.Offset, f.Span.Length, f.Span.Line, f.Span.Column, at, g.Start.Line, g.Start.Character, g.End.Line, g.End.Character)
	}
	b.WriteString("--\n")
	if err := r.WriteText(&b, "src.ergo"); err != nil {
		panic(err)
	}
	return b.String(), located, unlocated
}

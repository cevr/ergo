package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// ratchetPath is the check agreement's ratchet: per code, how many of the
// Go checker's diagnostics the Ergo checker misses and how many it adds,
// over the cases it was measured on. It is generated (ratchetCommand
// writes it); a conflict in it is a regeneration.
var ratchetPath = filepath.Join("testdata", "check_agree.ratchet.json")

// ratchetCommand writes ratchetPath from a run of TestCompilerCheckAgrees.
const ratchetCommand = "ERGO_WRITE_RATCHET=1 go test -count=1 -run '^TestCompilerCheckAgrees$' ./internal/selfhostcheck"

// ratchet is the file's form.
type ratchet struct {
	Generated string                  `json:"generated"`
	Cases     int                     `json:"cases"`
	GoLines   int                     `json:"goLines"`
	Codes     map[string]ratchetCount `json:"codes"`
}

type ratchetCount struct {
	Missing int `json:"missing"`
	Extra   int `json:"extra"`
}

// checkLine is one diagnostic as checkdump prints it.
type checkLine struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (l checkLine) String() string {
	return fmt.Sprintf("%s %d:%d: %s %s", l.File, l.Line, l.Col, l.Code, l.Message)
}

// agreeCase is one package of the agreement: its files, the tables the Go
// side writes for the Ergo side, and the Go checker's diagnostics.
type agreeCase struct {
	name         string
	path         string
	files        []string
	ext, foreign []string
	want         []checkLine
	src          string // a generated case's source
}

// goCheckLines is ds as checkdump's lines, each in the file names gives.
func goCheckLines(ds []check.Diagnostic, names []string) []checkLine {
	var out []checkLine
	for _, d := range ds {
		name := ""
		if d.File >= 0 && d.File < len(names) {
			name = names[d.File]
		}
		out = append(out, checkLine{File: name, Line: d.Line, Col: d.Column, Code: d.Code, Message: d.Message})
	}
	return out
}

// TestCompilerCheckAgrees is S3-AGREE's one driver: the self-hosted
// checker's diagnostics (cmd/checkdump: code, position and text, as JSON)
// against the Go checker's, over
//   - every corpus folder the Go checker accepts (it reports none there:
//     an Ergo line is a false diagnostic),
//   - the Go checker's negative fixtures, each a package of one file, and
//   - seeded programs: the unused perturbations of corpus files and the
//     ownership-flow programs.
//
// checkdump runs at -threads 1 and at -threads 8; the two outputs must be
// the same bytes (a hard failure, never ratcheted). The disagreements are
// counted per code, a Go line the Ergo side lacks (missing) and an Ergo
// line the Go side lacks (extra), and held against the ratchet file: a
// count over it fails, naming the code and three of its sites; a count
// under it is logged (the file only shrinks, by regeneration:
// ratchetCommand). The Ergo side types with the tables the Go side writes
// (prelude, universe, Go imports), as the expression agreement does.
//
// It builds Go because checkdump is the Ergo program under test: only its
// lowered, built form runs over the corpus.
func TestCompilerCheckAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	var cases []agreeCase
	// The corpus, one folder at a time (eachGoPackage).
	dirs := corpusDirs(t)
	corpus := make([]*agreeCase, len(dirs))
	eachGoPackage(dirs, func(i int, pkg *check.Package) {
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		corpus[i] = &agreeCase{name: rel(dirs[i]), path: importPath(dirs[i], pkg), ext: ext, foreign: foreign}
	})
	for i, c := range corpus {
		if c != nil {
			c.files = ergoFiles(t, dirs[i])
			cases = append(cases, *c)
		}
	}
	folders := len(cases)
	// One source as a package of one file: the Go checker's diagnostics
	// and its tables.
	tmp := t.TempDir()
	single := func(name, file, src string) bool {
		pkg, ds := check.CheckSource(src)
		if pkg == nil || (len(ds) > 0 && ds[0].Code == check.CodeSyntax) {
			return false // the parser's, not the checker's
		}
		dir := filepath.Join(tmp, "case", strconv.Itoa(len(cases)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, file)
		writeLines(t, f, []string{strings.TrimSuffix(src, "\n")})
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		cases = append(cases, agreeCase{name: name, path: "main", files: []string{f}, ext: ext, foreign: foreign, want: goCheckLines(ds, []string{file}), src: src})
		return true
	}
	fixtures := 0
	for _, glob := range []string{"internal/sketch/check/testdata/fixtures/negative/*.ergo", "internal/sketch/check/testdata/v1/*.neg.ergo"} {
		paths, err := filepath.Glob(filepath.Join(repoRoot(), glob))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if single(rel(p), filepath.Base(p), string(src)) {
				fixtures++
			}
		}
	}
	seeds := 0
	pool := unusedPool(t)
	for seed := base; seed < base+300; seed++ {
		if src, ok := perturbUnused(pool, seed); ok && single(fmt.Sprintf("unused seed %d", seed), "p.ergo", src) {
			seeds++
		}
		if single(fmt.Sprintf("flow seed %d", seed), "p.ergo", flowProgram(seed)) {
			seeds++
		}
	}
	var manifest []string
	goLines := 0
	for i, c := range cases {
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, e, c.ext)
		writeLines(t, f, c.foreign)
		manifest = append(manifest, strings.Join(append([]string{c.path, e, f}, c.files...), " "))
		goLines += len(c.want)
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	bin := filepath.Join(t.TempDir(), "checkdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/checkdump"); err != nil {
		t.Fatalf("ergo build ./cmd/checkdump: %v\n%s", err, o)
	}
	one := runDump(t, ctx, bin, "-std", stdDir(), "-threads", "1", "-corpus", m)
	eight := runDump(t, ctx, bin, "-std", stdDir(), "-threads", "8", "-corpus", m)
	if a, b := strings.Join(one, "\n"), strings.Join(eight, "\n"); a != b {
		t.Fatalf("checkdump -threads 1 and -threads 8 differ:\n%s", firstDifference(one, eight))
	}
	got := make([][]checkLine, len(cases))
	at := -1
	for _, l := range one {
		if strings.HasPrefix(l, "@pkg ") {
			at++
			continue
		}
		var c checkLine
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatalf("checkdump wrote a line that is no diagnostic object: %v\n%s", err, l)
		}
		if at >= 0 && at < len(cases) {
			got[at] = append(got[at], c)
		}
	}
	if at != len(cases)-1 {
		t.Fatalf("checkdump -corpus wrote %d packages, want %d", at+1, len(cases))
	}
	// The disagreements, per code, with their sites.
	counts := map[string]ratchetCount{}
	sites := map[string][]string{}
	for i, c := range cases {
		have := map[string]int{}
		for _, l := range got[i] {
			have[l.String()]++
		}
		for _, w := range c.want {
			if have[w.String()] > 0 {
				have[w.String()]--
				continue
			}
			n := counts[w.Code]
			n.Missing++
			counts[w.Code] = n
			sites[w.Code] = append(sites[w.Code], fmt.Sprintf("missing in %s: %s", c.name, w))
		}
		for _, l := range got[i] {
			if have[l.String()] > 0 {
				have[l.String()]--
				n := counts[l.Code]
				n.Extra++
				counts[l.Code] = n
				sites[l.Code] = append(sites[l.Code], fmt.Sprintf("extra in %s: %s", c.name, l))
			}
		}
	}
	codes := make([]string, 0, len(counts))
	missing, extra := 0, 0
	for code, n := range counts {
		codes = append(codes, code)
		missing += n.Missing
		extra += n.Extra
	}
	sort.Strings(codes)
	var table []string
	for _, code := range codes {
		table = append(table, fmt.Sprintf("%s missing %d extra %d", code, counts[code].Missing, counts[code].Extra))
	}
	t.Logf("%d corpus folders, %d fixtures, %d seeds (%d cases); %d Go diagnostics; %d missing, %d extra:\n%s", folders, fixtures, seeds, len(cases), goLines, missing, extra, strings.Join(table, "\n"))
	now := ratchet{Generated: "generated by " + ratchetCommand + "; do not edit", Cases: len(cases), GoLines: goLines, Codes: counts}
	if os.Getenv("ERGO_WRITE_RATCHET") != "" {
		b, err := json.MarshalIndent(now, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ratchetPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", ratchetPath)
		return
	}
	b, err := os.ReadFile(ratchetPath)
	if err != nil {
		t.Fatalf("no ratchet: %v (write it: %s)", err, ratchetCommand)
	}
	var was ratchet
	if err := json.Unmarshal(b, &was); err != nil {
		t.Fatalf("%s: %v", ratchetPath, err)
	}
	for _, code := range codes {
		n, w := counts[code], was.Codes[code]
		if n.Missing > w.Missing || n.Extra > w.Extra {
			ex := sites[code]
			if len(ex) > 3 {
				ex = ex[:3]
			}
			t.Errorf("%s disagrees more than the ratchet: missing %d (ratchet %d), extra %d (ratchet %d); sites:\n%s", code, n.Missing, w.Missing, n.Extra, w.Extra, strings.Join(ex, "\n"))
		}
	}
	for code, w := range was.Codes {
		if n := counts[code]; n.Missing < w.Missing || n.Extra < w.Extra {
			t.Logf("%s is under the ratchet: missing %d (ratchet %d), extra %d (ratchet %d); regenerate: %s", code, n.Missing, w.Missing, n.Extra, w.Extra, ratchetCommand)
		}
	}
	if was.Cases != len(cases) || was.GoLines != goLines {
		t.Logf("the ratchet was measured on %d cases and %d Go diagnostics, this run has %d and %d", was.Cases, was.GoLines, len(cases), goLines)
	}
}

// firstDifference is the first line where a and b differ, with its index.
func firstDifference(a, b []string) string {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n-threads 1: %s\n-threads 8: %s", i+1, x, y)
		}
	}
	return "(same lines)"
}

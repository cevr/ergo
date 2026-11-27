package selfhostcheck

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	linkedcompiler "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

type sdDiag struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type sdFormatOut struct {
	OK           bool   `json:"ok"`
	Changed      bool   `json:"changed"`
	InputDigest  string `json:"inputDigest"`
	OutputDigest string `json:"outputDigest"`
	Text         string `json:"text"`
	Error        string `json:"error"`
}

type sdCheckSourceOut struct {
	IfaceFP string   `json:"ifaceFP"`
	SelfFP  string   `json:"selfFP"`
	Diags   []sdDiag `json:"diags"`
}

type sdCheckCacheOut struct {
	CheckKey  string `json:"checkKey"`
	ReuseHit  bool   `json:"reuseHit"`
	ReuseMiss bool   `json:"reuseMiss"`
	StdKey    string `json:"stdKey"`
	ModKey    string `json:"modKey"`
	CleanFP   string `json:"cleanFP"`
	DepFP     string `json:"depFP"`
}

type sdTreePkg struct {
	Path    string   `json:"path"`
	SelfFP  string   `json:"selfFP"`
	CleanFP string   `json:"cleanFP"`
	DepFP   string   `json:"depFP"`
	IfaceFP string   `json:"ifaceFP"`
	Files   []string `json:"files"`
	Imports []string `json:"imports"`
}

type sdTreeOut struct {
	Root     string      `json:"root"`
	Packages []sdTreePkg `json:"packages"`
	Diags    []sdDiag    `json:"diags"`
}

type sdAdvice struct {
	Law    string `json:"law"`
	Status string `json:"status"`
}

type sdLicenceOut struct {
	RowCount     int        `json:"rowCount"`
	Errors       []string   `json:"errors"`
	Advice       []sdAdvice `json:"advice"`
	Corpus       string     `json:"corpus"`
	ForeignPaths []string   `json:"foreignPaths"`
	ShadowOK     bool       `json:"shadowOK"`
	ShadowLaw    string     `json:"shadowLaw"`
	ShadowCase   string     `json:"shadowCase"`
}

type sdGraphNode struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
	Sub  string `json:"sub"`
	Name string `json:"name"`
	Qual string `json:"qual"`
}

type sdGraphEdge struct {
	Kind string `json:"kind"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

type sdGraphOut struct {
	Text  string `json:"text"`
	Graph struct {
		Nodes []sdGraphNode `json:"nodes"`
		Edges []sdGraphEdge `json:"edges"`
	} `json:"graph"`
}

type sdEvalOut struct {
	TargetKey string `json:"targetKey"`
	TestKey   string `json:"testKey"`
	Replay    string `json:"replay"`
	WarmKey   string `json:"warmKey"`
	CachedHit bool   `json:"cachedHit"`
}

type sdInspectOut struct {
	Imports      int    `json:"imports"`
	Functions    int    `json:"functions"`
	RewritesText string `json:"rewritesText"`
	FactsText    string `json:"factsText"`
}

type sdAssetsOut struct {
	PatternErr   string `json:"patternErr"`
	CompanionErr string `json:"companionErr"`
	SplitOK      bool   `json:"splitOK"`
	MapperPkg    string `json:"mapperPkg"`
	MapperFunc   string `json:"mapperFunc"`
	Matched      bool   `json:"matched"`
}

func runSketchfrontCompiler(t *testing.T, ctx context.Context, prog string, args ...string) (string, error) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), 4<<30)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = proclimit.Run(cmd, limit)
	return out.String(), err
}

func sketchfrontDumpBin(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sketchfrontdump")
	if out, err := runSketchfrontCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/sketchfrontdump"); err != nil {
		t.Fatalf("ergo build ./cmd/sketchfrontdump: %v\n%s", err, out)
	}
	return bin
}

func runSketchfrontDumpJSON(t *testing.T, ctx context.Context, bin string, dst any, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sketchfrontdump %v failed: %v\n%s", args, err, string(out))
	}
	if err := json.Unmarshal(out, dst); err != nil {
		t.Fatalf("sketchfrontdump %v JSON decode failed: %v\nraw: %s", args, err, string(out))
	}
}

// TestCompilerSketchfrontAgrees verifies parity between pure-Ergo compiler/sketchfront
// (executed via compiler/cmd/sketchfrontdump) and Go internal/sketchfront across formatting,
// source and directory checking with overlays, check cache keys, inspection formatting,
// law ledger and corpus handling, evaluation keys and replays, graph projection, and asset rules.
func TestCompilerSketchfrontAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()

	root := repoRoot()
	stdDir := filepath.Join(root, "internal", "sketch", "std")
	bin := sketchfrontDumpBin(t, ctx)

	t.Run("FormatAndFailureMessageParity", func(t *testing.T) {
		tmp := t.TempDir()
		unformatted := filepath.Join(tmp, "unfmt.ergo")
		src := "func Add(a int, b int) int {\na + b\n}\n"
		if err := os.WriteFile(unformatted, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		goRes, err := sketchfront.Format(src, 0)
		if err != nil {
			t.Fatalf("sketchfront.Format: %v", err)
		}
		var ergoRes sdFormatOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoRes, "-mode", "format", "-file", unformatted)
		if !ergoRes.OK || ergoRes.Changed != goRes.Changed || ergoRes.InputDigest != goRes.InputDigest || ergoRes.OutputDigest != goRes.OutputDigest || ergoRes.Text != goRes.Text {
			t.Fatalf("format parity mismatch:\nergo=%+v\ngo=%+v", ergoRes, goRes)
		}

		badFile := filepath.Join(tmp, "bad.ergo")
		badSrc := "func broken( {\n"
		if err := os.WriteFile(badFile, []byte(badSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		_, goErr := sketchfront.Format(badSrc, 0)
		var ff report.FormatFailure
		if !errors.As(goErr, &ff) {
			t.Fatalf("expected FormatFailure, got %v", goErr)
		}
		wantMsg := sketchfront.FailureMessage(ff)
		var ergoBad sdFormatOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoBad, "-mode", "format", "-file", badFile)
		if ergoBad.OK || ergoBad.Error != wantMsg {
			t.Fatalf("format failure mismatch: ergo=%q go=%q", ergoBad.Error, wantMsg)
		}
	})

	t.Run("CheckSourceParity", func(t *testing.T) {
		tmp := t.TempDir()
		srcFile := filepath.Join(tmp, "main.ergo")
		src := "import strings\n\nfunc Main() int {\n\tlet unused = 1\n\tstrings.TrimSpace(\"hi\").Len()\n}\n"
		if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		goA := sketchfront.Check(src)
		var ergoOut sdCheckSourceOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoOut, "-mode", "checksource", "-std", stdDir, "-file", srcFile)
		if len(ergoOut.Diags) != len(goA.Diagnostics) {
			t.Fatalf("CheckSource diag count mismatch: ergo=%v go=%v", ergoOut.Diags, goA.Diagnostics)
		}
		for i, gd := range goA.Diagnostics {
			ed := ergoOut.Diags[i]
			if ed.Code != gd.Code || ed.Line != gd.Span.Line || ed.Col != gd.Span.Column || ed.Message != gd.Message {
				t.Fatalf("CheckSource diag[%d] mismatch:\nergo=%+v\ngo=%+v", i, ed, gd)
			}
		}
	})

	t.Run("TreeCheckAndOverlayParity", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "ergo.mod"), []byte("module app\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		subSrc := "func Helper(n int) int {\n\tn + 1\n}\n"
		mainSrc := "import app/sub\n\nfunc Main() int {\n\tsub.Helper(41)\n}\n"
		if err := os.WriteFile(filepath.Join(tmp, "sub", "sub.ergo"), []byte(subSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		mainPath := filepath.Join(tmp, "main.ergo")
		if err := os.WriteFile(mainPath, []byte(mainSrc), 0o644); err != nil {
			t.Fatal(err)
		}

		goA, err := sketchfront.CheckDir(tmp)
		if err != nil {
			t.Fatalf("sketchfront.CheckDir: %v", err)
		}
		var ergoTree sdTreeOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoTree, "-mode", "tree", "-dir", tmp, "-std", stdDir)
		if len(ergoTree.Diags) != len(goA.Diagnostics) {
			t.Fatalf("clean tree diags mismatch: ergo=%v go=%v", ergoTree.Diags, goA.Diagnostics)
		}
		var goPaths, ergoPaths []string
		for _, p := range goA.Packages() {
			goPaths = append(goPaths, p.Path)
		}
		for _, p := range ergoTree.Packages {
			ergoPaths = append(ergoPaths, p.Path)
		}
		if !slices.Equal(ergoPaths, goPaths) {
			t.Fatalf("tree package order mismatch: ergo=%v go=%v", ergoPaths, goPaths)
		}

		overlaySrc := "import app/sub\n\nfunc Main() int {\n\tlet dead = 7\n\tsub.Helper(41)\n}\n"
		ovFile := filepath.Join(t.TempDir(), "overlay.ergo")
		if err := os.WriteFile(ovFile, []byte(overlaySrc), 0o644); err != nil {
			t.Fatal(err)
		}
		goOv, err := sketchfront.CheckDirOverlay(tmp, map[string]string{mainPath: overlaySrc})
		if err != nil {
			t.Fatalf("CheckDirOverlay: %v", err)
		}
		var ergoOv sdTreeOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoOv, "-mode", "tree", "-dir", tmp, "-std", stdDir, "-overlay-rel", "main.ergo", "-overlay-file", ovFile)
		if len(ergoOv.Diags) != len(goOv.Diagnostics) || len(ergoOv.Diags) == 0 {
			t.Fatalf("overlay diag count mismatch: ergo=%v go=%v", ergoOv.Diags, goOv.Diagnostics)
		}
		if ergoOv.Diags[0].Code != goOv.Diagnostics[0].Code || ergoOv.Diags[0].Line != goOv.Diagnostics[0].Span.Line {
			t.Fatalf("overlay diag mismatch: ergo=%+v go=%+v", ergoOv.Diags[0], goOv.Diagnostics[0])
		}
	})

	t.Run("CheckCacheKeyAndInspectFormattingParity", func(t *testing.T) {
		tmp := t.TempDir()
		fPath := filepath.Join(tmp, "a.ergo")
		fSrc := "import strings\n\nfunc Echo(s string) string {\n\tstrings.TrimSpace(s)\n}\n"
		if err := os.WriteFile(fPath, []byte(fSrc), 0o644); err != nil {
			t.Fatal(err)
		}
		var ccA, ccB sdCheckCacheOut
		runSketchfrontDumpJSON(t, ctx, bin, &ccA, "-mode", "checkcache", "-pkg", "app/m", "-compiler", "ergo-test-v1", "-env", "GOARCH=amd64,GOOS=linux", "--", fPath)
		runSketchfrontDumpJSON(t, ctx, bin, &ccB, "-mode", "checkcache", "-pkg", "app/m", "-compiler", "ergo-test-v1", "-env", "GOOS=linux,GOARCH=amd64", "--", fPath)
		if !ccA.ReuseHit || !ccA.ReuseMiss || len(ccA.CheckKey) != 64 || ccA.CheckKey != ccB.CheckKey {
			t.Fatalf("checkcache parity mismatch: A=%+v B=%+v", ccA, ccB)
		}

		goRw := sketchfront.RewritesText([]lower.RewriteSite{
			{File: fPath, Line: 1, Column: 1, Law: "L", Verdict: "validated", By: "eval", State: "current", Applied: true},
		})
		goFacts := sketchfront.FactsText([]lower.Fact{
			{Kind: "fact-pure", Func: "F", At: fPath + ":1:1", Holds: "pure F", Evidence: "proved", Reason: "empty"},
		})
		var insp sdInspectOut
		runSketchfrontDumpJSON(t, ctx, bin, &insp, "-mode", "inspect", "-pkg", "app/m", "-file", fPath)
		if insp.RewritesText != goRw {
			t.Fatalf("RewritesText mismatch:\nergo=%q\ngo=%q", insp.RewritesText, goRw)
		}
		if insp.FactsText != goFacts {
			t.Fatalf("FactsText mismatch:\nergo=%q\ngo=%q", insp.FactsText, goFacts)
		}
		if insp.Functions != 1 {
			t.Fatalf("InspectGo function count mismatch: ergo=%d want 1", insp.Functions)
		}
	})

	t.Run("LedgerCorpusShadowAndEvalParity", func(t *testing.T) {
		stdLawsPath := filepath.Join(stdDir, "ergo.laws")
		rawLaws, err := os.ReadFile(stdLawsPath)
		if err != nil {
			t.Fatal(err)
		}
		goRows, err := lower.ParseLedger(string(rawLaws))
		if err != nil {
			t.Fatalf("lower.ParseLedger: %v", err)
		}
		tmp := t.TempDir()
		casesFile := filepath.Join(tmp, "ergo.cases")
		casesText := "law.Foo\n\t(\"a\", 1)\n"
		if err := os.WriteFile(casesFile, []byte(casesText), 0o644); err != nil {
			t.Fatal(err)
		}
		var lic sdLicenceOut
		runSketchfrontDumpJSON(t, ctx, bin, &lic, "-mode", "licence", "-file", stdLawsPath, "-cases", casesFile, "-goos", "linux", "-goarch", "amd64", "--", "missing.Law")
		if len(lic.Errors) != 0 || lic.RowCount != len(goRows) {
			t.Fatalf("Std ergo.laws parse parity mismatch: ergo rows=%d errs=%v go=%d", lic.RowCount, lic.Errors, len(goRows))
		}
		if len(lic.Advice) != 1 || lic.Advice[0].Law != "missing.Law" || lic.Advice[0].Status != "missing" {
			t.Fatalf("AdviseLicences mismatch: %+v", lic.Advice)
		}
		if lic.Corpus != "law.Foo\n\t(\"a\", 1)\n" {
			t.Fatalf("Corpus format mismatch: %q", lic.Corpus)
		}
		if !slices.Equal(lic.ForeignPaths, []string{"a.go", "b_linux_amd64.go"}) {
			t.Fatalf("ForeignKeys filter mismatch: %v", lic.ForeignPaths)
		}

		sr, ok := sketchfront.ParseShadowLine("ergo-shadow: law law.Rev refuted ([1])\n")
		if !ok || !lic.ShadowOK || lic.ShadowLaw != sr.Law || lic.ShadowCase != sr.Case {
			t.Fatalf("shadow refutation parity mismatch: ergo=(%v,%q,%q) go=(%v,%q,%q)", lic.ShadowOK, lic.ShadowLaw, lic.ShadowCase, ok, sr.Law, sr.Case)
		}

		var evOut sdEvalOut
		runSketchfrontDumpJSON(t, ctx, bin, &evOut, "-mode", "eval", "-pkg", "app/main", "-file", "main.ergo", "-compiler", "ergo-test-v1", "-env", "GOFLAGS=-trimpath,GOPATH=/tmp/gp")
		wantWarm, err := linkedcompiler.KeyWarm(ctx, "ergo-test-v1", []string{"check", "app/main"}, []string{"GOFLAGS=-trimpath", "GOPATH=/tmp/gp"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !evOut.CachedHit || evOut.WarmKey != wantWarm || evOut.Replay != "ergo test main.ergo --law LawA --seed 7" {
			t.Fatalf("eval mode parity mismatch: got=%+v wantWarm=%q", evOut, wantWarm)
		}
	})

	t.Run("GraphAndAssetsParity", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, "util"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "ergo.mod"), []byte("module ex\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "util", "u.ergo"), []byte("func One() int {\n\t1\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "main.ergo"), []byte("import ex/util\n\nfunc Main() int {\n\tutil.One()\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		goA, err := sketchfront.CheckDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		goG := goA.Graph()
		var ergoG sdGraphOut
		runSketchfrontDumpJSON(t, ctx, bin, &ergoG, "-mode", "graph", "-dir", tmp, "-std", stdDir)
		var ergoPkgNames, goPkgNames []string
		for _, n := range ergoG.Graph.Nodes {
			if n.Kind == "package" {
				ergoPkgNames = append(ergoPkgNames, n.Name)
			}
		}
		for _, r := range goG.Nodes {
			if r.Kind.String() == "package" {
				goPkgNames = append(goPkgNames, goG.Strings[r.Name])
			}
		}
		slices.Sort(ergoPkgNames)
		slices.Sort(goPkgNames)
		if !reflect.DeepEqual(ergoPkgNames, goPkgNames) {
			t.Fatalf("Graph package nodes mismatch: ergo=%v go=%v", ergoPkgNames, goPkgNames)
		}

		var astOut sdAssetsOut
		runSketchfrontDumpJSON(t, ctx, bin, &astOut, "-mode", "assets", "-file", "*.svg", "-cases", "../bad.txt", "-pkg", "web/svg.Compile", "-dir", "ui/logo.sql")
		if astOut.PatternErr != "" || astOut.CompanionErr == "" || !astOut.SplitOK || astOut.MapperPkg != "web/svg" || astOut.MapperFunc != "Compile" || !astOut.Matched {
			t.Fatalf("assets mode parity failure: %+v", astOut)
		}
	})
}

// TestCompilerSketchfrontLawsRun executes co-located tests and laws in compiler/sketchfront.
func TestCompilerSketchfrontLawsRun(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	out, err := runSketchfrontCompiler(t, ctx, ergoCLI(t, ctx), "test", "./sketchfront")
	if err != nil {
		t.Fatalf("ergo test ./sketchfront in compiler/: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Fatalf("ergo test ./sketchfront printed no PASS:\n%s", out)
	}
}

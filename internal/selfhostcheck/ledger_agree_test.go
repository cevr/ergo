package selfhostcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/lawledger/parsed"
	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/testwait"
)

var (
	ledgerDumpOnce sync.Once
	ledgerDumpBin  string
	ledgerDumpErr  error
)

func ledgerDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	ergo := ergoCLI(t, ctx)
	ledgerDumpOnce.Do(func() {
		dir, err := os.MkdirTemp("", "selfhost-ledgerdump-*")
		if err != nil {
			ledgerDumpErr = err
			return
		}
		bin := filepath.Join(dir, "ledgerdump")
		cmd := exec.CommandContext(ctx, ergo, "build", "-o", bin, "./cmd/ledgerdump")
		cmd.Dir = filepath.Join(repoRoot(), "compiler")
		if out, err := cmd.CombinedOutput(); err != nil {
			ledgerDumpErr = os.ErrInvalid
			t.Logf("ergo build ./cmd/ledgerdump failed: %v\n%s", err, out)
			return
		}
		ledgerDumpBin = bin
	})
	if ledgerDumpErr != nil {
		t.Fatalf("ledgerdump build failed: %v", ledgerDumpErr)
	}
	return ledgerDumpBin
}

func runLedgerDump(t *testing.T, ctx context.Context, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ledgerdump %v failed: %v\n%s", args, err, out)
	}
	return out
}

func runLedgerDumpErr(t *testing.T, ctx context.Context, bin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCompilerLedgerAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := ledgerDump(t, ctx)
	root := repoRoot()

	t.Run("Runners", func(t *testing.T) {
		runnersPath := filepath.Join(root, lawledger.File)
		data, err := os.ReadFile(runnersPath)
		if err != nil {
			t.Fatal(err)
		}
		goRows, err := lawledger.Parse(string(data))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Count   uint32 `json:"count"`
			Entries []struct {
				Pkg    string `json:"pkg"`
				Law    string `json:"law"`
				Runner string `json:"runner"`
			} `json:"entries"`
		}
		raw := runLedgerDump(t, ctx, bin, "runners", runnersPath)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal runners output: %v\n%s", err, raw)
		}
		if int(out.Count) != len(goRows) {
			t.Fatalf("runner count mismatch: ergo=%d go=%d", out.Count, len(goRows))
		}
		goMap := make(map[string]string, len(goRows))
		for _, r := range goRows {
			goMap[r.Pkg+" "+r.Law] = r.Runner
		}
		for _, e := range out.Entries {
			want, ok := goMap[e.Pkg+" "+e.Law]
			if !ok || want != e.Runner {
				t.Fatalf("runner mismatch for %s %s: ergo=%q go=%q (ok=%v)", e.Pkg, e.Law, e.Runner, want, ok)
			}
		}
	})

	t.Run("StatementAndSame", func(t *testing.T) {
		samples := []struct {
			src string
			law string
			alt string
		}{
			{
				src: "package p\n\n// comment\nlaw AddZero(x int) {\n\tx + 0 == x;\n}\n",
				law: "AddZero",
				alt: "law AddZero(x int){x+0==x}",
			},
			{
				src: "package p\n\nlaw Interp(a int, b int) {\n\t\"a  \\(a + b)  z\" == \"a  \\(a+b)  z\"\n}\n",
				law: "Interp",
				alt: "law Interp(a int, b int) { \"a \\(a+b) z\" == \"a \\(a+b) z\" }",
			},
		}
		tmp := t.TempDir()
		for i, s := range samples {
			p := filepath.Join(tmp, filepath.Base(t.Name())+"_"+string(rune('a'+i))+".ergo")
			if err := os.WriteFile(p, []byte(s.src), 0o600); err != nil {
				t.Fatal(err)
			}
			goStmt, goOK := lawledger.Statement(s.src, s.law)
			var stOut struct {
				Found     bool   `json:"found"`
				Statement string `json:"statement"`
			}
			if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "statement", p, s.law), &stOut); err != nil {
				t.Fatal(err)
			}
			if stOut.Found != goOK || stOut.Statement != goStmt {
				t.Fatalf("statement[%d] mismatch:\nergo=(%v, %q)\ngo=(%v, %q)", i, stOut.Found, stOut.Statement, goOK, goStmt)
			}
			goSame := lawledger.Same(goStmt, s.alt)
			var smOut struct {
				Same bool `json:"same"`
			}
			if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "same", goStmt, s.alt), &smOut); err != nil {
				t.Fatal(err)
			}
			if smOut.Same != goSame {
				t.Fatalf("same[%d] mismatch: ergo=%v go=%v\na=%q\nb=%q", i, smOut.Same, goSame, goStmt, s.alt)
			}
		}
	})

	t.Run("RepoErgoLaws", func(t *testing.T) {
		relLedgers := []string{
			"internal/sketch/std/ergo.laws",
			"examples/cborpack/ergo.laws",
			"examples/counter/ergo.laws",
			"modules/simd/ergo.laws",
		}
		for _, rel := range relLedgers {
			full := filepath.Join(root, rel)
			data, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			goEntries, err := lower.ParseLedger(string(data))
			if err != nil {
				t.Fatalf("go ParseLedger(%s): %v", rel, err)
			}
			goFormatted := lower.FormatLedger(goEntries)
			var out struct {
				Count      uint32 `json:"count"`
				GuardCount uint32 `json:"guardCount"`
				Formatted  string `json:"formatted"`
			}
			raw := runLedgerDump(t, ctx, bin, "ledger", full)
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("%s: unmarshal: %v\n%s", rel, err, raw)
			}
			if out.Formatted != goFormatted {
				t.Fatalf("%s formatted ledger mismatch:\n--- ERGO ---\n%s\n--- GO ---\n%s", rel, out.Formatted[:min(len(out.Formatted), 400)], goFormatted[:min(len(goFormatted), 400)])
			}
		}
	})

	t.Run("Cases", func(t *testing.T) {
		for _, words := range [][]uint64{
			{},
			{0},
			{1, 255, ^uint64(0), 0x123456789abcdef0},
		} {
			goEnc := lawledger.EncodeTape(words)
			goDec, err := lawledger.DecodeTape(goEnc)
			if err != nil {
				t.Fatal(err)
			}
			var decArg []string
			for _, w := range words {
				decArg = append(decArg, jsonNumberString(w))
			}
			arg := strings.Join(decArg, ",")
			if arg == "" {
				arg = "-"
			}
			var tpOut struct {
				Encoded string   `json:"encoded"`
				Words   []uint64 `json:"words"`
			}
			raw := runLedgerDump(t, ctx, bin, "tapert", arg)
			if err := json.Unmarshal(raw, &tpOut); err != nil {
				t.Fatal(err)
			}
			if tpOut.Encoded != goEnc || !slices.Equal(tpOut.Words, goDec) {
				t.Fatalf("tape mismatch for %v: ergo=(%q, %v) go=(%q, %v)", words, tpOut.Encoded, tpOut.Words, goEnc, goDec)
			}
		}

		casesText := strings.Join([]string{
			"law " + lawledger.PropertyKey("pkg/b", "PropB") + " (1, 2)",
			"law " + lawledger.PropertyKey("pkg/a", "PropA") + " (\"hi\")",
			"property " + lawledger.PropertyKey("pkg/b", "PropB") + " tape h1:d2 -",
			"property " + lawledger.PropertyKey("pkg/a", "PropA") + " tape h1:d1 1,ff",
		}, "\n") + "\n"
		goCS, err := lawledger.ParseCases(casesText)
		if err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		cp := filepath.Join(tmp, "ergo.cases")
		if err := lawledger.WriteCasesFile(cp, goCS); err != nil {
			t.Fatal(err)
		}
		wantBytes, err := os.ReadFile(cp)
		if err != nil {
			t.Fatal(err)
		}
		var csOut struct {
			Formatted string `json:"formatted"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "cases", cp), &csOut); err != nil {
			t.Fatal(err)
		}
		if csOut.Formatted != string(wantBytes) {
			t.Fatalf("cases formatting mismatch:\n--- ERGO ---\n%s\n--- GO ---\n%s", csOut.Formatted, string(wantBytes))
		}
	})

	t.Run("KillerAndParsed", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, "pkg", "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(tmp, "pkg", "run"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module ex.local/m\n\ngo 1.27\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "runners.txt"), []byte("ex.local/m/pkg/a LawDemo\tex.local/m/pkg/run\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "pkg", "a", "a.ergo"), []byte("law LawDemo(n int) { n == n }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmp, "pkg", "run", "run_test.go"), []byte("package run\nimport \"testing\"\nfunc TestRun(t *testing.T) { _ = \"LawDemo\" }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		realSlicesDir := filepath.Join(root, "internal", "sketch", "std", "slices")
		goFound := lawledger.LawDeclared(root, "internal/sketch/std/slices", "FilterFuses")
		goMissing := lawledger.LawDeclared(root, "internal/sketch/std/slices", "NoSuchLawAnywhere")
		var kOut struct {
			Status       uint8  `json:"status"`
			DeclaredFile string `json:"declaredFile"`
		}
		raw := runLedgerDump(t, ctx, bin, "killer", filepath.Join(tmp, "pkg", "a"), filepath.Join(tmp, "runners.txt"), "ex.local/m/pkg/a", "LawDemo")
		if err := json.Unmarshal(raw, &kOut); err != nil {
			t.Fatal(err)
		}
		if kOut.Status != 1 {
			t.Fatalf("expected killer resolved on LawDemo fixture, got status=%d (%s)", kOut.Status, raw)
		}
		tmpRunners := filepath.Join(tmp, "std_runners.txt")
		if err := os.WriteFile(tmpRunners, []byte("slices FilterFuses\tTestStdSlicesLaws\nslices NoSuchLawAnywhere\tTestStdSlicesLaws\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var kReal struct {
			Status uint8 `json:"status"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "killer", realSlicesDir, tmpRunners, "slices", "FilterFuses"), &kReal); err != nil {
			t.Fatal(err)
		}
		if (kReal.Status == 1) != goFound {
			t.Fatalf("real std/slices FilterFuses LawDeclared mismatch: ergoStatus=%d goFound=%v", kReal.Status, goFound)
		}
		var kMiss struct {
			Status uint8 `json:"status"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "killer", realSlicesDir, tmpRunners, "slices", "NoSuchLawAnywhere"), &kMiss); err != nil {
			t.Fatal(err)
		}
		if (kMiss.Status == 1) != goMissing {
			t.Fatalf("real std/slices NoSuchLawAnywhere LawDeclared mismatch: ergoStatus=%d goFound=%v", kMiss.Status, goMissing)
		}

		for _, stdPkg := range []string{"slices", "option", "hex", "sha256", "strconv", "filepath", "json", "time", "civil", "session", "term", "flag"} {
			parsedFiles, err := parsed.Parse(stdPkg)
			if err != nil {
				t.Fatalf("parsed.Parse(%q): %v", stdPkg, err)
			}
			goLaws, _ := parsed.Laws(stdPkg, parsedFiles)
			stdErgoFiles, err := filepath.Glob(filepath.Join(root, "internal", "sketch", "std", stdPkg, "*.ergo"))
			if err != nil {
				t.Fatal(err)
			}
			var pOut struct {
				Count uint32 `json:"count"`
				Laws  []struct {
					Name      string `json:"name"`
					Receiver  string `json:"receiver"`
					Statement string `json:"statement"`
					Derived   bool   `json:"derived"`
				} `json:"laws"`
			}
			if err := json.Unmarshal(runLedgerDump(t, ctx, bin, append([]string{"parsed"}, stdErgoFiles...)...), &pOut); err != nil {
				t.Fatal(err)
			}
			ergoKeys := make([]string, 0, len(pOut.Laws))
			for _, l := range pOut.Laws {
				if l.Receiver == "" {
					ergoKeys = append(ergoKeys, stdPkg+" "+l.Name)
				} else {
					ergoKeys = append(ergoKeys, stdPkg+" "+l.Receiver+"."+l.Name)
				}
			}
			slices.Sort(ergoKeys)
			goKeys := make([]string, 0, len(goLaws))
			for k := range goLaws {
				goKeys = append(goKeys, k)
			}
			slices.Sort(goKeys)
			if !slices.Equal(ergoKeys, goKeys) {
				t.Fatalf("parsed law keys mismatch for std/%s (len ergo=%d go=%d):\n%s\nergo=%v\ngo=%v",
					stdPkg, len(ergoKeys), len(goKeys), setDiff(goKeys, ergoKeys), ergoKeys, goKeys)
			}
		}
	})

	t.Run("AccessClosureAndRecord", func(t *testing.T) {
		tmp := t.TempDir()
		inPath := filepath.Join(tmp, "access.json")
		inPayload := map[string]any{
			"pkg":          "ergo.local/prototype/compiler/lawledger",
			"testName":     "TestDemo",
			"workDir":      tmp,
			"modRoot":      tmp,
			"tmpDir":       filepath.Join(tmp, "tmp"),
			"toolVersions": "go1.27.4",
			"env":          []string{"PATH=/bin", "SECRET_TOKEN=xyz", "GOTOOLCHAIN=local"},
			"args":         []string{"-v", "--", "-quiet"},
			"ignores":      []string{"*.tmp"},
			"actions": []map[string]any{
				{"kind": "open", "path": "/proc/self/stat", "digest": "h1:p", "existed": true, "isDir": false, "present": true, "size": 1},
				{"kind": "open", "path": filepath.Join(tmp, "a.ergo"), "digest": "h1:a", "existed": true, "isDir": false, "present": true, "size": 42},
				{"kind": "open", "path": filepath.Join(tmp, "skip.tmp"), "digest": "h1:s", "existed": true, "isDir": false, "present": true, "size": 3},
				{"kind": "exec", "path": "/usr/bin/env", "digest": "h1:e", "existed": true, "isDir": false, "present": true, "size": 0},
			},
		}
		b, _ := json.Marshal(inPayload)
		if err := os.WriteFile(inPath, b, 0o600); err != nil {
			t.Fatal(err)
		}
		var aOut struct {
			FilteredEnv    []string `json:"filteredEnv"`
			TestLogFlag    bool     `json:"testLogFlag"`
			WritablePlaces []string `json:"writablePlaces"`
			ManifestLines  []string `json:"manifestLines"`
			ManifestDigest string   `json:"manifestDigest"`
			RecordText     string   `json:"recordText"`
			RecordJSON     string   `json:"recordJSON"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "access", inPath), &aOut); err != nil {
			t.Fatal(err)
		}
		if !aOut.TestLogFlag {
			t.Fatalf("expected TestLogFlag true before --")
		}
		if len(aOut.FilteredEnv) != 2 || slices.Contains(aOut.FilteredEnv, "SECRET_TOKEN=xyz") {
			t.Fatalf("unexpected FilteredEnv: %v", aOut.FilteredEnv)
		}
		if len(aOut.ManifestLines) != 3 || !strings.HasPrefix(aOut.ManifestDigest, "h1:") {
			t.Fatalf("unexpected ManifestLines/Digest: lines=%v digest=%q", aOut.ManifestLines, aOut.ManifestDigest)
		}
		if !strings.Contains(aOut.RecordText, "inputs h1:") || !strings.Contains(aOut.RecordJSON, "ergo-testinputs/1") {
			t.Fatalf("unexpected Record output: text=%q json=%q", aOut.RecordText, aOut.RecordJSON)
		}
	})

	t.Run("ReceiptLocateAndFlags", func(t *testing.T) {
		tmp := t.TempDir()
		realDir := filepath.Join(tmp, "real", "sub")
		srcDir := filepath.Join(tmp, "src")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			t.Fatal(err)
		}
		targetFile := filepath.Join(tmp, "real", "data.txt")
		if err := os.WriteFile(targetFile, []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, filepath.Join(srcDir, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(tmp, "does-not-exist"), filepath.Join(srcDir, "dangling")); err != nil {
			t.Fatal(err)
		}
		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(oldWD) }()
		if err := os.Chdir(srcDir); err != nil {
			t.Fatal(err)
		}

		req := map[string]any{
			"workDir": srcDir,
			"spelled": "link/../data.txt",
			"race":    false,
			"cover":   false,
			"links":   []any{},
		}
		reqPath := filepath.Join(tmp, "req.json")
		reqBytes, _ := json.Marshal(req)
		if err := os.WriteFile(reqPath, reqBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		goLoc, err := receipt.Locate("link/../data.txt")
		if err != nil {
			t.Fatal(err)
		}
		goSrcLoc, err := receipt.LocateSource("link/../missing.txt")
		if err != nil {
			t.Fatal(err)
		}
		var rOut struct {
			Flags  []string `json:"flags"`
			Locate struct {
				Resolved string `json:"resolved"`
				Exists   bool   `json:"exists"`
			} `json:"locate"`
			LocateErr string `json:"locateErr"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "receipt", reqPath), &rOut); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rOut.Flags, receipt.GoBuildFlags) {
			t.Fatalf("GoBuildFlags mismatch: ergo=%v go=%v", rOut.Flags, receipt.GoBuildFlags)
		}
		if rOut.LocateErr != "" || rOut.Locate.Resolved != goLoc.Path() || !rOut.Locate.Exists {
			t.Fatalf("Locate mismatch: ergo=(%q, exists=%v, err=%q) go=%q", rOut.Locate.Resolved, rOut.Locate.Exists, rOut.LocateErr, goLoc.Path())
		}
		reqSrc := map[string]any{
			"workDir": srcDir,
			"spelled": "link/../missing.txt",
			"race":    false,
			"cover":   false,
			"links":   []any{},
		}
		reqSrcBytes, _ := json.Marshal(reqSrc)
		if err := os.WriteFile(reqPath, reqSrcBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		var rSrcOut struct {
			LocateSource struct {
				Resolved string `json:"resolved"`
				Exists   bool   `json:"exists"`
			} `json:"locateSource"`
			SourceErr string `json:"sourceErr"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "receipt", reqPath), &rSrcOut); err != nil {
			t.Fatal(err)
		}
		if rSrcOut.SourceErr != "" || rSrcOut.LocateSource.Resolved != goSrcLoc.Path() {
			t.Fatalf("LocateSource mismatch: ergo=(%q, err=%q) go=%q", rSrcOut.LocateSource.Resolved, rSrcOut.SourceErr, goSrcLoc.Path())
		}
		reqDangle := map[string]any{
			"workDir": srcDir,
			"spelled": "dangling/child",
			"race":    false,
			"cover":   false,
			"links":   []any{},
		}
		reqDangleBytes, _ := json.Marshal(reqDangle)
		if err := os.WriteFile(reqPath, reqDangleBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		_, goDangleErr := receipt.Locate("dangling/child")
		var rDangleOut struct {
			LocateErr string `json:"locateErr"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "receipt", reqPath), &rDangleOut); err != nil {
			t.Fatal(err)
		}
		if (goDangleErr != nil) != (rDangleOut.LocateErr != "") {
			t.Fatalf("dangling link error mismatch: ergo=%q go=%v", rDangleOut.LocateErr, goDangleErr)
		}
	})

	t.Run("PinsetLockKeyAndArchive", func(t *testing.T) {
		lockPath := filepath.Join(root, "toolchain", "pin", "lock")
		lockBytes, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatal(err)
		}
		goLock, err := pinset.ParseLock(lockBytes)
		if err != nil {
			t.Fatalf("go ParseLock(%s): %v", lockPath, err)
		}
		var lkOut struct {
			Count   uint32 `json:"count"`
			Entries []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"entries"`
		}
		rawLk, lkErr := runLedgerDumpErr(t, ctx, bin, "pinlock", lockPath)
		if lkErr != nil {
			t.Fatalf("ergo pinlock(%s) failed: %v\n%s\ngoLock=%+v", lockPath, lkErr, rawLk, goLock)
		}
		if err := json.Unmarshal([]byte(rawLk), &lkOut); err != nil {
			t.Fatalf("unmarshal pinlock: %v\n%s", err, rawLk)
		}
		if int(lkOut.Count) != len(goLock.Sets) {
			t.Fatalf("pinlock set count mismatch: ergo=%d go=%d", lkOut.Count, len(goLock.Sets))
		}

		tmp := t.TempDir()
		kGo := pinset.Key{
			Kind:    "std",
			Release: "go1.27.4",
			Patch:   "",
			GOOS:    "linux",
			GOARCH:  "amd64",
			Level:   "v1",
			Flags:   "",
		}
		kJson := map[string]string{
			"schema":  "ergo pin set 1",
			"kind":    kGo.Kind,
			"release": kGo.Release,
			"patch":   kGo.Patch,
			"goos":    kGo.GOOS,
			"goarch":  kGo.GOARCH,
			"level":   kGo.Level,
			"flags":   kGo.Flags,
		}
		kPath := filepath.Join(tmp, "key.json")
		kBytes, _ := json.Marshal(kJson)
		if err := os.WriteFile(kPath, kBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		var kOut struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "pinkey", kPath), &kOut); err != nil {
			t.Fatal(err)
		}
		if kOut.Name != kGo.Name() {
			t.Fatalf("pinset.Key.Name mismatch: ergo=%q go=%q", kOut.Name, kGo.Name())
		}

		srcTree := filepath.Join(tmp, "tree")
		if err := os.MkdirAll(filepath.Join(srcTree, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		toolBytes := []byte("hello-pinset")
		toolPath := filepath.Join(srcTree, "bin", "tool")
		if err := os.WriteFile(toolPath, toolBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		outDir := filepath.Join(tmp, "out")
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(toolBytes)
		digHex := hex.EncodeToString(sum[:])
		goProd, err := pinset.Write(outDir, kGo.Name(), []pinset.Entry{
			{Name: "bin/tool", Digest: digHex, Export: "abc"},
		}, map[string]string{"bin/tool": toolPath})
		if err != nil {
			t.Fatal(err)
		}
		itemsPath := filepath.Join(tmp, "items.json")
		itemsBytes, _ := json.Marshal([]map[string]any{
			{"path": "bin/tool", "data": "hello-pinset", "export": "abc", "mode": 0o644},
		})
		if err := os.WriteFile(itemsPath, itemsBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		ergoGz := filepath.Join(tmp, "ergo.tar.gz")
		var bOut struct {
			TarDigest string `json:"tarDigest"`
			FileCount uint32 `json:"fileCount"`
			RoundTrip uint32 `json:"roundTrip"`
		}
		if err := json.Unmarshal(runLedgerDump(t, ctx, bin, "pinbundle", kGo.Name(), itemsPath, ergoGz), &bOut); err != nil {
			t.Fatal(err)
		}
		if bOut.TarDigest != goProd.Digest {
			t.Fatalf("pinset TarDigest mismatch: ergo=%q go=%q", bOut.TarDigest, goProd.Digest)
		}
	})
}

func jsonNumberString(v uint64) string {
	return json.Number(uint64ToDec(v)).String()
}

func uint64ToDec(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

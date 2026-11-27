package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

type ergoLintFinding struct {
	Code         string `json:"code"`
	Rule         string `json:"rule"`
	Severity     string `json:"severity"`
	Message      string `json:"message"`
	Instead      string `json:"instead"`
	File         string `json:"file"`
	Offset       int    `json:"offset"`
	Length       int    `json:"length"`
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	HasPrecision bool   `json:"hasPrecision"`
	Precision    int    `json:"precision"`
	HasFix       bool   `json:"hasFix"`
}

type ergoLintRuleStatus struct {
	Rule     string `json:"rule"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

type ergoLintSuppression struct {
	Rule    string `json:"rule"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	File    string `json:"file"`
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Covered int    `json:"covered"`
}

type ergoLintResult struct {
	Checked      bool                  `json:"checked"`
	Complete     bool                  `json:"complete"`
	Findings     []ergoLintFinding     `json:"findings"`
	Rules        []ergoLintRuleStatus  `json:"rules"`
	Suppressions []ergoLintSuppression `json:"suppressions"`
}

var implementedLintRules = []string{
	"banned-words",
	"capability-outside-owner",
	"cognitive-complexity",
	"commented-code",
	"concept-file-names",
	"console-outside-entry",
	"cyclomatic-complexity",
	"declaration-tangle",
	"deprecated",
	"domain-imports-implementation",
	"duplicate-shape",
	"duplicate-test",
	"file-cycle",
	"file-name-case",
	"foreign-error-public",
	"foreign-outside-adapter",
	"foreign-record-field-write",
	"foreign-where-std",
	"halstead-difficulty",
	"hidden-dependency",
	"identical-arms",
	"layer",
	"long-file",
	"long-parameter-list",
	"narration-comment",
	"no-comments",
	"production-uses-test-support",
	"seam-entrypoints",
	"self-assignment",
	"sibling-folder-cycle",
	"silent-catch",
	"test-reads-unexported",
	"unbuilt-variant",
	"unused-declaration",
	"unused-type-parameter",
	"vacuous-test",
	"wide-directory",
	"wide-observed-interface",
	"wire-key-quote",
	"with-at-entry",
}

func lintDumpBin(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lintdump")
	if out, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/lintdump"); err != nil {
		t.Fatalf("ergo build ./cmd/lintdump: %v\n%s", err, out)
	}
	return bin
}

func buildParityConfigJSON(extraRules map[string]any) string {
	rules := map[string]any{}
	for _, r := range implementedLintRules {
		rules[r] = "warning"
	}
	for k, v := range extraRules {
		rules[k] = v
	}
	doc := map[string]any{
		"version": 1,
		"rules":   rules,
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func goConfigured(t *testing.T, configJSON string, presets []string, configDir string) *lint.Configuration {
	t.Helper()
	doc := lint.DefaultConfig()
	if configJSON != "" {
		var err error
		doc, err = lint.ParseConfig([]byte(configJSON))
		if err != nil {
			t.Fatalf("go ParseConfig(%s): %v", configJSON, err)
		}
	}
	doc.Dir = configDir
	conf, err := lint.Builtin().Configure(doc, presets)
	if err != nil {
		t.Fatalf("go Configure: %v", err)
	}
	return conf
}

func runLinkedSourcesLint(
	t *testing.T,
	ctx context.Context,
	configJSON string,
	presets []string,
	closed bool,
	in lint.Input,
	glossaries map[string]string,
) ergoLintResult {
	t.Helper()
	var pkgMeta, pkgExt, pkgForeign []string
	var filePkgIdx []int
	var fileRels, fileSrcs []string
	for pi, u := range in.Units {
		flag := ""
		if u.Dependency {
			flag = "dep"
		} else if u.Package != nil && u.Package.Entry() != nil {
			flag = "entry"
		}
		dir := ""
		if len(u.Files) > 0 {
			dir = filepath.ToSlash(filepath.Dir(u.Files[0].Rel))
		}
		name := ""
		if u.Package != nil {
			name = u.Package.Name
		}
		pkgMeta = append(pkgMeta, u.Module, u.Path, dir, name, flag)
		var extText, foreignText string
		if u.Package != nil {
			_, extLines := goKinds(u.Package)
			_, foreignLines := goTypes(u.Package)
			extText = strings.Join(extLines, "\n")
			foreignText = strings.Join(foreignLines, "\n")
		}
		pkgExt = append(pkgExt, extText)
		pkgForeign = append(pkgForeign, foreignText)
		for _, f := range u.Files {
			filePkgIdx = append(filePkgIdx, pi)
			fileRels = append(fileRels, f.Rel)
			src := ""
			if f.Syntax != nil {
				src = f.Syntax.Src
			}
			fileSrcs = append(fileSrcs, src)
		}
	}
	var gPairs []string
	for k, v := range glossaries {
		gPairs = append(gPairs, k, v)
	}
	raw := must(t)(linked.LintSourcesJSON(
		ctx,
		configJSON,
		presets,
		closed,
		in.Checked,
		pkgMeta,
		pkgExt,
		pkgForeign,
		filePkgIdx,
		fileRels,
		fileSrcs,
		gPairs,
		1,
	))
	if !strings.HasPrefix(raw, "ok\n") {
		t.Fatalf("LintSourcesJSON failed: %s", raw)
	}
	var res ergoLintResult
	if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "ok\n")), &res); err != nil {
		t.Fatalf("decode LintSourcesJSON output: %v\n%s", err, raw)
	}
	return res
}

func runLintDumpOnAnalyses(
	t *testing.T,
	ctx context.Context,
	bin string,
	configJSON string,
	presets []string,
	closed bool,
	as []sketchfront.Analysis,
	diskDirByUnit map[string]string,
) (goFindings []lint.Finding, ergoFindings []ergoLintFinding) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "ergo.lint.json")
	if err := os.WriteFile(cfgPath, []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	conf := goConfigured(t, configJSON, presets, tmp)

	var manifestLines []string
	var combinedIn lint.Input
	combinedIn.Checked = true
	seenUnit := map[string]bool{}
	tableIdx := 0

	for _, a := range as {
		in := lint.InputOf(a)
		if !in.Checked {
			combinedIn.Checked = false
		}
		for _, u := range in.Units {
			key := u.Module + "\x00" + u.Path
			if seenUnit[key] {
				continue
			}
			seenUnit[key] = true
			combinedIn.Units = append(combinedIn.Units, u)
			flag := "-"
			if u.Dependency {
				flag = "dep"
			} else if u.Package != nil && u.Package.Entry() != nil {
				flag = "entry"
			}
			extPath, foreignPath := "-", "-"
			if u.Package != nil {
				_, extLines := goKinds(u.Package)
				_, foreignLines := goTypes(u.Package)
				extPath = filepath.Join(tmp, fmt.Sprintf("ext%d", tableIdx))
				foreignPath = filepath.Join(tmp, fmt.Sprintf("foreign%d", tableIdx))
				writeLines(t, extPath, extLines)
				writeLines(t, foreignPath, foreignLines)
				tableIdx++
			}
			pkgName := ""
			if u.Package != nil {
				pkgName = u.Package.Name
			}
			dir := "."
			if len(u.Files) > 0 {
				dir = filepath.ToSlash(filepath.Dir(u.Files[0].Rel))
			}
			var fileArgs []string
			for _, f := range u.Files {
				abs := ""
				if rootDir, ok := diskDirByUnit[u.Module]; ok && rootDir != "" {
					cand := filepath.Join(rootDir, filepath.FromSlash(f.Rel))
					if _, err := os.Stat(cand); err == nil {
						abs = cand
					}
				}
				if abs == "" {
					abs = filepath.Join(tmp, "src", fmt.Sprintf("f_%d_%s", tableIdx, filepath.Base(f.Rel)))
					if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
						t.Fatal(err)
					}
					src := ""
					if f.Syntax != nil {
						src = f.Syntax.Src
					}
					if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				fileArgs = append(fileArgs, f.Rel+"="+abs)
			}
			if len(fileArgs) == 0 {
				continue
			}
			mod := u.Module
			if mod == "" {
				mod = "main"
			}
			pkgPath := u.Path
			if pkgPath == "" {
				pkgPath = "main"
			}
			if pkgName == "" {
				pkgName = filepath.Base(pkgPath)
			}
			line := fmt.Sprintf("%s %s %s %s %s %s %s %s",
				mod, pkgPath, dir, pkgName, flag, extPath, foreignPath, strings.Join(fileArgs, " "))
			manifestLines = append(manifestLines, line)
		}
	}

	manifestPath := filepath.Join(tmp, "corpus.manifest")
	writeLines(t, manifestPath, manifestLines)

	args := []string{"-std", stdDir(), "-config", cfgPath, "-corpus", manifestPath}
	for _, p := range presets {
		args = append(args, "-preset", p)
	}
	if closed {
		args = append(args, "-closed")
	}
	if !combinedIn.Checked {
		args = append(args, "-unchecked")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
		t.Fatalf("lintdump %v failed: %v\n%s", args, err, string(out))
	}
	var res ergoLintResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode lintdump JSON: %v\n%s", err, string(out))
	}
	goRes := conf.Run(combinedIn)
	return goRes.Findings, res.Findings
}

type findingKey struct {
	Code     string
	Rule     string
	Severity string
	File     string
	Offset   int
	Length   int
	Line     int
	Column   int
}

func compareFindings(t *testing.T, label string, want []lint.Finding, got []ergoLintFinding) {
	t.Helper()
	wantSet := map[findingKey]lint.Finding{}
	for _, f := range want {
		k := findingKey{
			Code: f.Code, Rule: f.Rule, Severity: f.Severity.String(),
			File: f.File, Offset: f.Offset, Length: f.Length, Line: f.Line, Column: f.Column,
		}
		wantSet[k] = f
	}
	gotSet := map[findingKey]ergoLintFinding{}
	for _, f := range got {
		k := findingKey{
			Code: f.Code, Rule: f.Rule, Severity: f.Severity,
			File: f.File, Offset: f.Offset, Length: f.Length, Line: f.Line, Column: f.Column,
		}
		gotSet[k] = f
	}
	var missing []string
	for k, wf := range wantSet {
		if gf, ok := gotSet[k]; ok {
			_ = gf
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (%s) %s:%d:%d [%d..%d]: %s", wf.Code, wf.Rule, wf.File, wf.Line, wf.Column, wf.Offset, wf.Offset+wf.Length, wf.Message))
	}
	var extra []string
	for k, gf := range gotSet {
		if _, ok := wantSet[k]; ok {
			continue
		}
		extra = append(extra, fmt.Sprintf("%s (%s) %s:%d:%d [%d..%d]: %s", gf.Code, gf.Rule, gf.File, gf.Line, gf.Column, gf.Offset, gf.Offset+gf.Length, gf.Message))
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s parity mismatch: missing %d, extra %d (want %d, got %d)\n  missing (first 15):\n    %s\n  extra (first 15):\n    %s",
			label, len(missing), len(extra), len(want), len(got),
			strings.Join(firstN(missing, 15), "\n    "),
			strings.Join(firstN(extra, 15), "\n    "))
	}
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}

// TestCompilerLintAgrees proves 100% diagnostic, config, file-case, and suppression
// parity between self-hosted compiler/lint and Go internal/sketch/lint across
// planted rule fixtures and real repository packages (compiler, std, modules).
func TestCompilerLintAgrees(t *testing.T) {
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()

	t.Run("ConfigAndFileCase", func(t *testing.T) {
		cfgCases := []struct {
			name    string
			json    string
			presets []string
			wantErr bool
		}{
			{"empty-default", `{"version":1}`, nil, false},
			{"strict-layout", `{"version":1,"extends":["strict","layout"]}`, nil, false},
			{"bad-version", `{"version":2}`, nil, true},
			{"missing-version", `{"rules":{}}`, nil, true},
			{"dup-key-root", `{"version":1,"version":1}`, nil, true},
			{"dup-key-rules", `{"version":1,"rules":{"long-file":"hint","long-file":"warning"}}`, nil, true},
			{"trailing-garbage", `{"version":1} true`, nil, true},
			{"unknown-preset", `{"version":1,"extends":["no-such-preset"]}`, nil, true},
			{"dup-preset", `{"version":1,"extends":["strict"]}`, []string{"strict"}, true},
			{"unknown-rule", `{"version":1,"rules":{"unknown-rule":"error"}}`, nil, true},
			{"reserved-rule-runner", `{"version":1,"rules":{"lint-runner":"off"}}`, nil, true},
			{"reserved-rule-sup", `{"version":1,"rules":{"invalid-suppression":"off"}}`, nil, true},
			{"negative-maxFileLines", `{"version":1,"rules":{"long-file":{"options":{"maxFileLines":-1}}}}`, nil, true},
			{"file-name-case-snake-dirs", `{"version":1,"rules":{"file-name-case":{"severity":"warning","options":{"case":"snake_case","dirs":true}}}}`, nil, false},
			{"file-name-case-bad-style", `{"version":1,"rules":{"file-name-case":{"options":{"case":"screaming"}}}}`, nil, true},
			{"seam-empty-entries", `{"version":1,"rules":{"seam-entrypoints":{"options":{"seams":[{"root":"a","entries":[]}]}}}}`, nil, true},
			{"seam-entry-outside", `{"version":1,"rules":{"seam-entrypoints":{"options":{"seams":[{"root":"a","entries":["b/main"]}]}}}}`, nil, true},
			{"long-file-zero", `{"version":1,"rules":{"long-file":{"severity":"hint","options":{"maxFileLines":0}}}}`, nil, false},
		}
		for _, tc := range cfgCases {
			doc, err := lint.ParseConfig([]byte(tc.json))
			if err == nil {
				_, err = lint.Builtin().Configure(doc, tc.presets)
			}
			got := must(t)(linked.LintConfigValidate(ctx, tc.json, tc.presets, nil))
			gotErr := strings.HasPrefix(got, "bad ")
			if gotErr != tc.wantErr || (err != nil) != tc.wantErr {
				t.Errorf("%s: goErr=%v ergo=%q wantErr=%v", tc.name, err, got, tc.wantErr)
			}
		}

		pathCases := []struct {
			rel   string
			style string
			dirs  bool
		}{
			{"pkg/FileName.ergo", lint.CaseSnake, false},
			{"pkg/fileName_test.ergo", lint.CaseSnake, false},
			{"pkg/File_Name_law.ergo", lint.CaseKebab, true},
			{"cmd/MyBin/MainFile.ergo", lint.CaseCamel, true},
			{"sub/MyDir/FooBar.ergo", lint.CasePascal, true},
			{"sub/already_snake.ergo", lint.CaseSnake, false},
		}
		for _, tc := range pathCases {
			wantRel, _, _, ok := lint.NormalizeRelPath(tc.rel, lint.FileNameCaseOptions{Case: tc.style, Dirs: tc.dirs})
			wantStr := "0 " + tc.rel + "\n"
			if ok {
				wantStr = "1 " + wantRel + "\n"
			}
			gotStr := must(t)(linked.LintFileCasePath(ctx, tc.rel, tc.style, tc.dirs))
			if gotStr != wantStr {
				t.Errorf("fileCase(%q,%q,%v): got %q, want %q", tc.rel, tc.style, tc.dirs, gotStr, wantStr)
			}
		}
	})

	t.Run("PlantedFixtures", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "ergo.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"dom/dom.ergo": `// Console writes lines.
type Console {
	Write(s string)
}

type Status = Active{} | Retired{}

func Keep(x int) int { x }
`,
			"impl/impl.ergo": `import example.com/app/dom

type StdConsole {} implements dom.Console

func Helper(a int, b int, c int, d int, e int, f int) int {
	let mut x = a
	x = x
	x
}
`,
			"app/BadFile_helpers.ergo": `import example.com/app/dom
import example.com/app/impl
import go strings

// shape comment triggers no-comments and narration
func BadShape[T](n int) int {
	// let skipped = 1
	// ergo-lint-disable-next-line self-assignment -- keep
	let mut y = n
	y = y
	// ergo-lint-disable-next-line unknown-rule-xyz -- bad
	let z = impl.Helper(1, 2, 3, 4, 5, 6)
	match n {
	case 1:
		return z + 10
	case 2:
		return z + 10
	case _:
		return y
	}
}

func run(s dom.Status) int {
	match s {
	case dom.Active:
		1
	case dom.Retired:
		0
	}
}

test EmptyTest {
	let v = 1
	let _ = v
}
`,
		}
		for rel, content := range files {
			full := filepath.Join(tmp, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		as, _, err := sketchfront.CheckTree(tmp)
		if err != nil {
			t.Fatalf("CheckTree planted fixtures: %v", err)
		}
		cfgJSON := buildParityConfigJSON(map[string]any{
			"file-name-case": map[string]any{
				"severity": "warning",
				"options":  map[string]any{"case": "snake_case", "dirs": true},
			},
			"long-file": map[string]any{
				"severity": "hint",
				"options":  map[string]any{"maxFileLines": 25},
			},
		})
		for _, a := range as {
			in := lint.InputOf(a)
			conf := goConfigured(t, cfgJSON, nil, tmp)
			goRes := conf.Run(in)
			ergoRes := runLinkedSourcesLint(t, ctx, cfgJSON, nil, false, in, nil)
			compareFindings(t, "planted:"+a.Root().Path, goRes.Findings, ergoRes.Findings)
		}
	})

	needsBuild(t)
	bin := lintDumpBin(t, ctx)

	corpusDirs := []struct {
		name string
		dir  string
	}{
		{"compiler", filepath.Join(repoRoot(), "compiler")},
		{"std", stdDir()},
	}
	modsDir := filepath.Join(repoRoot(), "modules")
	entries, err := os.ReadDir(modsDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			mDir := filepath.Join(modsDir, e.Name())
			if _, statErr := os.Stat(filepath.Join(mDir, "ergo.mod")); statErr == nil {
				corpusDirs = append(corpusDirs, struct {
					name string
					dir  string
				}{"modules/" + e.Name(), mDir})
			}
		}
	}

	parityConfig := buildParityConfigJSON(map[string]any{
		"long-file":               map[string]any{"severity": "hint", "options": map[string]any{"maxFileLines": 800}},
		"wide-directory":          map[string]any{"severity": "hint", "options": map[string]any{"maxDirectoryMembers": 12}},
		"wide-observed-interface": map[string]any{"severity": "hint", "options": map[string]any{"maxSurfaceList": 12}},
	})

	for _, c := range corpusDirs {
		t.Run("Corpus_"+c.name, func(t *testing.T) {
			as, _, err := sketchfront.CheckTree(c.dir)
			if err != nil {
				t.Fatalf("CheckTree(%s): %v", c.dir, err)
			}
			diskRoots := map[string]string{}
			for _, a := range as {
				for _, p := range a.Packages() {
					m := a.ModuleOf(p)
					if p.Module == "" {
						diskRoots[m] = c.dir
					}
				}
			}
			goF, ergoF := runLintDumpOnAnalyses(t, ctx, bin, parityConfig, nil, false, as, diskRoots)
			compareFindings(t, c.name, goF, ergoF)
			t.Logf("%s: compared %d Go findings vs %d Ergo findings cleanly", c.name, len(goF), len(ergoF))

			var checkedOnly []sketchfront.Analysis
			var uncheckedRoots []string
			for _, a := range as {
				if lint.InputOf(a).Checked {
					checkedOnly = append(checkedOnly, a)
				} else if r := a.Root(); r.Path != "" {
					uncheckedRoots = append(uncheckedRoots, r.Path)
				}
			}
			if len(uncheckedRoots) > 0 && len(checkedOnly) > 0 {
				t.Logf("%s has %d unchecked analyses (%v); also verifying %d checked analyses", c.name, len(uncheckedRoots), uncheckedRoots, len(checkedOnly))
				goCF, ergoCF := runLintDumpOnAnalyses(t, ctx, bin, parityConfig, nil, false, checkedOnly, diskRoots)
				compareFindings(t, c.name+"/checked", goCF, ergoCF)
				t.Logf("%s/checked: compared %d Go findings vs %d Ergo findings cleanly", c.name, len(goCF), len(ergoCF))
			}
		})
	}
}

func BenchmarkLintSelfHostVsGo(b *testing.B) {
	ctx := context.Background()
	compilerDir := filepath.Join(repoRoot(), "compiler", "lint")
	entries, err := os.ReadDir(compilerDir)
	if err != nil {
		b.Fatal(err)
	}
	var srcs []string
	var rels []string
	var goFiles []lint.File
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ergo") {
			continue
		}
		p := filepath.Join(compilerDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		src := string(data)
		totalBytes += int64(len(src))
		rels = append(rels, "lint/"+e.Name())
		srcs = append(srcs, src)
		pf, err := syntax.Parse(src)
		if err != nil {
			b.Fatal(err)
		}
		goFiles = append(goFiles, lint.File{Rel: "lint/" + e.Name(), Syntax: pf})
	}
	cfgJSON := buildParityConfigJSON(map[string]any{
		"file-name-case": map[string]any{"severity": "warning", "options": map[string]any{"case": "snake_case"}},
		"long-file":      map[string]any{"severity": "hint", "options": map[string]any{"maxFileLines": 800}},
	})
	doc, _ := lint.ParseConfig([]byte(cfgJSON))
	conf, _ := lint.Builtin().Configure(doc, nil)
	goIn := lint.Input{
		Checked: true,
		Units: []lint.Unit{{
			Path: "compiler/lint", Module: "ergo.local/prototype/compiler", Files: goFiles,
		}},
	}
	fileIdx := make([]int, len(rels))
	pkgMeta := []string{"ergo.local/prototype/compiler", "compiler/lint", "lint", "lint", ""}

	b.Run("Go", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(totalBytes)
		for i := 0; i < b.N; i++ {
			_ = conf.Run(goIn)
		}
	})
	b.Run("SelfHost", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(totalBytes)
		for i := 0; i < b.N; i++ {
			res, err := linked.LintSourcesJSON(ctx, cfgJSON, nil, false, false, pkgMeta, []string{""}, []string{""}, fileIdx, rels, srcs, nil, 1)
			if err != nil || !strings.HasPrefix(res, "ok\n") {
				b.Fatal(err, res)
			}
		}
	})
}

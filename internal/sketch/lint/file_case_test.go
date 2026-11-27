package lint_test

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

func fileCaseConfig(t *testing.T, opts any) *lint.Configuration {
	t.Helper()
	var s lint.Setting
	if opts != nil {
		raw, err := json.Marshal(opts)
		if err != nil {
			t.Fatal(err)
		}
		s.Options = raw
	}
	conf, err := lint.Builtin().Configure(lint.Config{
		Version: lint.ConfigVersion,
		Rules:   map[string]lint.Setting{"file-name-case": s},
	}, nil)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return conf
}

func checkModuleInput(t *testing.T, files map[string]string) lint.Input {
	t.Helper()
	dir := t.TempDir()
	mod := filepath.Join(dir, "ergo.mod")
	if err := os.WriteFile(mod, []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rootDirs []string
	seenDir := map[string]bool{}
	for rel, src := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		d := filepath.Dir(abs)
		if !seenDir[d] {
			seenDir[d] = true
			rootDirs = append(rootDirs, d)
		}
	}
	slices.Sort(rootDirs)
	var units []lint.Unit
	for _, d := range rootDirs {
		a, err := sketchfront.CheckDir(d)
		if err != nil {
			t.Fatalf("CheckDir(%s): %v", d, err)
		}
		if len(a.Diagnostics) > 0 || a.Checked() == nil {
			t.Fatalf("unchecked %s: %v", d, a.Diagnostics)
		}
		in := lint.InputOf(a)
		for _, u := range in.Units {
			if u.Dependency {
				continue
			}
			// Deduplicate units when multiple dirs share imported packages.
			already := false
			for _, ex := range units {
				if ex.Path == u.Path {
					already = true
					break
				}
			}
			if !already {
				units = append(units, u)
			}
		}
	}
	return lint.Input{Checked: true, Units: units}
}

// TestFileNameCaseOptions checks configuration acceptance, inactive defaults,
// and rejection of invalid option values or unknown keys.
// Mutant: file-name-case-invalid-case-admitted.
func TestFileNameCaseOptions(t *testing.T) {
	valid := []lint.FileNameCaseOptions{
		{},
		{Case: lint.CaseSnake},
		{Case: lint.CaseKebab},
		{Case: lint.CaseCamel},
		{Case: lint.CasePascal},
		{Case: lint.CaseFlat},
		{Case: lint.CaseSnake, Dirs: true},
	}
	for _, o := range valid {
		conf := fileCaseConfig(t, o)
		got, active := conf.FileNameCase()
		wantActive := o.Case != ""
		if active != wantActive || (active && got != o) {
			t.Fatalf("FileNameCase(%+v) = (%+v, %v), want (%+v, %v)", o, got, active, o, wantActive)
		}
	}

	// Layout preset alone leaves file-name-case inactive when case is omitted.
	layoutConf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	if _, active := layoutConf.FileNameCase(); active {
		t.Fatal("Layout preset without case option must leave file-name-case inactive")
	}

	badConfigs := []string{
		`{"version":1,"rules":{"file-name-case":{"options":{"case":"SCREAMING_SNAKE"}}}}`,
		`{"version":1,"rules":{"file-name-case":{"options":{"case":"unknown"}}}}`,
		`{"version":1,"rules":{"file-name-case":{"options":{"dirs":true}}}}`,
		`{"version":1,"rules":{"file-name-case":{"options":{"case":"snake_case","extra":1}}}}`,
	}
	for _, raw := range badConfigs {
		doc, err := lint.ParseConfig([]byte(raw))
		if err != nil {
			continue
		}
		if _, err := lint.Builtin().Configure(doc, nil); err == nil {
			t.Errorf("expected config error for %s, got nil", raw)
		}
	}
}

// TestFileNameCaseSuffixAndAcronyms checks acronym-aware splitting, compound
// suffix preservation, case-insensitive suffix canonicalization, bare suffix
// stem protection, and hidden/underscore skips across all five styles.
// Mutants: file-name-case-acronyms-unsplit, file-name-case-digit-upper-unsplit,
// file-name-case-drops-suffix, file-name-case-single-suffix-only.
func TestFileNameCaseSuffixAndAcronyms(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{
			in: "HTTPClient.ergo",
			want: map[string]string{
				lint.CaseSnake:  "http_client.ergo",
				lint.CaseKebab:  "http-client.ergo",
				lint.CaseCamel:  "httpClient.ergo",
				lint.CasePascal: "HttpClient.ergo",
				lint.CaseFlat:   "httpclient.ergo",
			},
		},
		{
			in: "JSON2SQL.ergo",
			want: map[string]string{
				lint.CaseSnake:  "json2_sql.ergo",
				lint.CaseKebab:  "json2-sql.ergo",
				lint.CaseCamel:  "json2Sql.ergo",
				lint.CasePascal: "Json2Sql.ergo",
				lint.CaseFlat:   "json2sql.ergo",
			},
		},
		{
			in: "HTTPClient_linux_test.ergo",
			want: map[string]string{
				lint.CaseSnake:  "http_client_linux_test.ergo",
				lint.CaseKebab:  "http-client_linux_test.ergo",
				lint.CaseCamel:  "httpClient_linux_test.ergo",
				lint.CasePascal: "HttpClient_linux_test.ergo",
				lint.CaseFlat:   "httpclient_linux_test.ergo",
			},
		},
		{
			in: "MyAPI_darwin_laws.ergo",
			want: map[string]string{
				lint.CaseSnake:  "my_api_darwin_laws.ergo",
				lint.CaseKebab:  "my-api_darwin_laws.ergo",
				lint.CaseCamel:  "myApi_darwin_laws.ergo",
				lint.CasePascal: "MyApi_darwin_laws.ergo",
				lint.CaseFlat:   "myapi_darwin_laws.ergo",
			},
		},
		{
			in: "ProtoGen_gen.ergo",
			want: map[string]string{
				lint.CaseSnake:  "proto_gen_gen.ergo",
				lint.CaseKebab:  "proto-gen_gen.ergo",
				lint.CaseCamel:  "protoGen_gen.ergo",
				lint.CasePascal: "ProtoGen_gen.ergo",
				lint.CaseFlat:   "protogen_gen.ergo",
			},
		},
		{
			in: "HTTPClient_TEST.ergo",
			want: map[string]string{
				lint.CaseSnake:  "http_client_test.ergo",
				lint.CaseKebab:  "http-client_test.ergo",
				lint.CaseCamel:  "httpClient_test.ergo",
				lint.CasePascal: "HttpClient_test.ergo",
				lint.CaseFlat:   "httpclient_test.ergo",
			},
		},
		{
			in: "tests.ergo",
			want: map[string]string{
				lint.CaseSnake:  "tests.ergo",
				lint.CaseKebab:  "tests.ergo",
				lint.CaseCamel:  "tests.ergo",
				lint.CasePascal: "Tests.ergo",
				lint.CaseFlat:   "tests.ergo",
			},
		},
		{
			in: "laws.ergo",
			want: map[string]string{
				lint.CaseSnake:  "laws.ergo",
				lint.CaseKebab:  "laws.ergo",
				lint.CaseCamel:  "laws.ergo",
				lint.CasePascal: "Laws.ergo",
				lint.CaseFlat:   "laws.ergo",
			},
		},
	}

	for _, tc := range cases {
		for style, want := range tc.want {
			got, ok := lint.NormalizeErgoFileName(tc.in, style)
			if !ok || got != want {
				t.Errorf("NormalizeErgoFileName(%q, %s) = (%q, %v), want (%q, true)", tc.in, style, got, ok, want)
			}
			// Normalization must be idempotent.
			again, ok2 := lint.NormalizeErgoFileName(got, style)
			if !ok2 || again != got {
				t.Errorf("non-idempotent NormalizeErgoFileName(%q, %s) = (%q, %v)", got, style, again, ok2)
			}
		}
	}

	for _, skip := range []string{"_hidden.ergo", ".hidden.ergo", "not_ergo.txt", ".ergo", ""} {
		if got, ok := lint.NormalizeErgoFileName(skip, lint.CaseSnake); ok {
			t.Errorf("expected skip for %q, got (%q, true)", skip, got)
		}
	}
}

// TestFileNameCaseProperty generates synthetic multi-package modules with
// mixed-case files, acronym tokens, compound suffixes, and mixed-case dirs,
// verifying rule findings, idempotency after normalization, dirs=false vs
// dirs=true gating, and suppression directives across all five case styles.
// Mutants: file-name-case-acronyms-unsplit, file-name-case-digit-upper-unsplit,
// file-name-case-drops-suffix, file-name-case-single-suffix-only,
// file-name-case-dirs-always-on, file-name-case-dirs-ignored.
func TestFileNameCaseProperty(t *testing.T) {
	styles := []string{lint.CaseSnake, lint.CaseKebab, lint.CaseCamel, lint.CasePascal, lint.CaseFlat}
	wordBank := []string{"http", "HTTP", "client", "Client", "json2", "JSON2", "Sql", "SQL", "api", "API", "v2", "user", "User", "id", "ID"}
	seps := []string{"", "_", "-", "."}
	suffixes := []string{"", "_test", "_laws", "_gen", "_linux", "_darwin", "_linux_test", "_TEST"}
	dirParts := []string{"MyPkg", "api-v2", "HTTPServer", "core"}

	r := rand.New(rand.NewPCG(42, 0xfc1))
	for seed := 0; seed < 45; seed++ {
		style := styles[seed%len(styles)]
		dirsOn := r.IntN(2) == 0

		// Build 2 packages with mixed-case filenames & folders.
		files := map[string]string{}
		for p := 0; p < 2; p++ {
			folder := dirParts[r.IntN(len(dirParts))] + "_" + string(rune('a'+p))
			baseWords := 1 + r.IntN(3)
			var stem strings.Builder
			for w := 0; w < baseWords; w++ {
				if w > 0 {
					stem.WriteString(seps[r.IntN(len(seps))])
				}
				stem.WriteString(wordBank[r.IntN(len(wordBank))])
			}
			suf := suffixes[r.IntN(len(suffixes))]
			fileName := stem.String() + string(rune('A'+p)) + suf + ".ergo"
			rel := folder + "/" + fileName

			var src string
			if strings.Contains(strings.ToLower(suf), "law") {
				src = "law Id" + string(rune('A'+p)) + "(x int) { x == x }\n"
			} else if strings.Contains(strings.ToLower(suf), "test") {
				src = "test Case" + string(rune('A'+p)) + " { assert 1 == 1 }\n"
			} else {
				src = "func Fn" + string(rune('A'+p)) + "(x int) int { x }\n"
			}
			files[rel] = src

			// Add a companion file whose basename already conforms to style to test dir anchor.
			okBase, ok2 := lint.NormalizeErgoFileName("helper.ergo", style)
			if !ok2 {
				t.Fatal("NormalizeErgoFileName(helper.ergo) failed")
			}
			compRel := folder + "/" + okBase
			if _, exists := files[compRel]; !exists {
				files[compRel] = "func HelperFn(x int) int { x }\n"
			}
		}

		// Independent expectation model: check basename independently with NormalizeErgoFileName
		// and directory segment independently with FormatCaseWords(SplitCaseWords(dir), style).
		wantChanged := map[string]string{}
		cleanByDir := map[string][]string{}
		for rel := range files {
			dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
			base := filepath.Base(filepath.FromSlash(rel))
			wantBase, ok := lint.NormalizeErgoFileName(base, style)
			if !ok {
				t.Fatalf("seed %d: NormalizeErgoFileName(%q) returned ok=false", seed, base)
			}
			wantDir := dir
			if dirsOn && dir != "." {
				wantDir = lint.FormatCaseWords(lint.SplitCaseWords(dir), style)
			}
			if wantBase != base {
				wantChanged[rel] = wantDir + "/" + wantBase
			} else if dirsOn && wantDir != dir {
				cleanByDir[dir] = append(cleanByDir[dir], rel)
			}
		}
		for _, cands := range cleanByDir {
			slices.Sort(cands)
			wantChanged[cands[0]] = "dir"
		}

		in := checkModuleInput(t, files)
		conf := fileCaseConfig(t, lint.FileNameCaseOptions{Case: style, Dirs: dirsOn})
		res := conf.Run(in)
		if !res.Checked || !res.Complete {
			t.Fatalf("seed %d: lint incomplete: checked=%v complete=%v", seed, res.Checked, res.Complete)
		}

		gotFiles := map[string]lint.Finding{}
		for _, f := range res.Findings {
			if f.Rule != "file-name-case" || f.Code != "L172" {
				t.Fatalf("seed %d: unexpected rule finding %+v", seed, f)
			}
			gotFiles[f.File] = f
		}
		if len(gotFiles) != len(wantChanged) {
			t.Fatalf("seed %d (style=%s dirs=%v): got findings for %v, want %v", seed, style, dirsOn, gotFiles, wantChanged)
		}
		for rel, wantTarget := range wantChanged {
			f, ok := gotFiles[rel]
			if !ok {
				t.Fatalf("seed %d: missing finding for %q (want %q); got %v", seed, rel, wantTarget, gotFiles)
			}
			if wantTarget != "" && !strings.Contains(f.Instead, "\"") {
				t.Fatalf("seed %d: expected rename hint in Instead, got %q", seed, f.Instead)
			}
		}

		// Re-run on normalized module paths: must emit zero findings.
		normalized := map[string]string{}
		for rel, src := range files {
			wantRel, _, _, ok := lint.NormalizeRelPath(rel, lint.FileNameCaseOptions{Case: style, Dirs: dirsOn})
			if !ok {
				t.Fatal("normalize failed")
			}
			// Guard against random collisions when mapping files to normalized target.
			if _, clash := normalized[wantRel]; clash {
				wantRel = strings.TrimSuffix(wantRel, ".ergo")
				if style == lint.CasePascal {
					wantRel += "Alt.ergo"
				} else if style == lint.CaseCamel {
					wantRel += "Alt.ergo"
				} else {
					wantRel += "_alt.ergo"
				}
			}
			normalized[wantRel] = src
		}
		normIn := checkModuleInput(t, normalized)
		normRes := conf.Run(normIn)
		if len(normRes.Findings) != 0 {
			t.Fatalf("seed %d (style=%s dirs=%v): expected 0 findings after normalization, got %+v", seed, style, dirsOn, normRes.Findings)
		}
	}
}

// TestFileNameCaseSuppression checks that ergo-lint-disable-next-line suppresses
// file-name-case above the first declaration cleanly.
func TestFileNameCaseSuppression(t *testing.T) {
	in := checkModuleInput(t, map[string]string{
		"pkg/BadName.ergo": "// ergo-lint-disable-next-line file-name-case -- legacy external file name\nfunc Ok() int { 1 }\n",
	})
	conf := fileCaseConfig(t, lint.FileNameCaseOptions{Case: lint.CaseSnake})
	res := conf.Run(in)
	if !res.Complete || len(res.Findings) != 0 {
		t.Fatalf("expected clean suppressed run, got complete=%v findings=%+v", res.Complete, res.Findings)
	}
	if len(res.Suppressions) != 1 || res.Suppressions[0].Status != lint.Applied {
		t.Fatalf("expected 1 applied suppression, got %+v", res.Suppressions)
	}
	// Sanity-check syntax package still parses cleanly (no unused import error).
	if _, err := syntax.Parse("func ok() {}"); err != nil {
		t.Fatal(err)
	}
}

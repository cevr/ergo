package fix_test

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func writeFixModFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return abs
}

func assertModuleChecksClean(t *testing.T, root string) {
	t.Helper()
	targets, err := sketchfront.DiscoverPackages(root)
	if err != nil {
		t.Fatalf("DiscoverPackages(%s): %v", root, err)
	}
	if len(targets) == 0 {
		t.Fatalf("no packages discovered under %s", root)
	}
	for _, tgt := range targets {
		a, err := sketchfront.CheckDir(tgt.Dir)
		if err != nil {
			t.Fatalf("CheckDir(%s): %v", tgt.Dir, err)
		}
		if len(a.Diagnostics) > 0 || a.Checked() == nil {
			t.Fatalf("diagnostics in %s: %+v", tgt.Dir, a.Diagnostics)
		}
	}
}

// TestFixFileNameCaseProperty checks deterministic collision-safe file renaming
// across case styles, suffixes, dry-run/check immutability, pre-existing disk
// collisions (both multi-source and existing non-source targets), suppression
// respect, and second-run idempotency.
// Mutants: file-name-case-fix-overwrites-collision,
// file-name-case-fix-overwrites-existing-target, file-name-case-fix-writes-on-check.
func TestFixFileNameCaseProperty(t *testing.T) {
	styles := []string{lint.CaseSnake, lint.CaseKebab, lint.CaseCamel, lint.CasePascal, lint.CaseFlat}
	wordPairs := [][2]string{
		{"HTTP", "Client"},
		{"JSON", "Parser"},
		{"Api", "Routes"},
		{"User", "Auth"},
		{"V2", "Index"},
	}
	suffixes := []string{"", "_test", "_laws", "_linux_test", "_darwin"}

	r := rand.New(rand.NewPCG(99, 0xca5e))
	for seed := 0; seed < 25; seed++ {
		style := styles[seed%len(styles)]
		root := t.TempDir()
		writeFixModFile(t, root, "ergo.mod", "module example.com/prop\n")
		writeFixModFile(t, root, "ergo.lint.json", `{"version":1,"rules":{"file-name-case":{"options":{"case":"`+style+`"}}}}`)

		pair := wordPairs[r.IntN(len(wordPairs))]
		suf := suffixes[r.IntN(len(suffixes))]
		origName := pair[0] + pair[1] + "Item" + suf + ".ergo"
		wantName, ok := lint.NormalizeErgoFileName(origName, style)
		if !ok {
			t.Fatalf("seed %d: NormalizeErgoFileName(%q) failed", seed, origName)
		}
		var src string
		if strings.Contains(suf, "law") {
			src = "law PropLaw(x int) { x == x }\n"
		} else if strings.Contains(suf, "test") {
			src = "test PropTest { assert 1 == 1 }\n"
		} else {
			src = "func PropFn(x int) int { x }\n"
		}
		writeFixModFile(t, root, "pkg/"+origName, src)

		// Suppressed file must stay untouched.
		suppressedName := "LegacyKeepName.ergo"
		writeFixModFile(t, root, "pkg/"+suppressedName, "// ergo-lint-disable-next-line file-name-case -- keep legacy name\nfunc KeptFn() int { 1 }\n")

		// Two non-conforming files that collide onto the same normalized target must BOTH be skipped.
		colTarget, _ := lint.NormalizeErgoFileName("DupWord.ergo", style)
		if colTarget != "DupWord.ergo" {
			writeFixModFile(t, root, "pkg/DupWord.ergo", "func DupA() int { 1 }\n")
			altCol := "dup_word.ergo"
			if style == lint.CaseSnake {
				altCol = "dup-word.ergo"
			}
			writeFixModFile(t, root, "pkg/"+altCol, "func DupB() int { 2 }\n")
		}

		// Pre-existing directory or non-regular entry at a target filename must block rename.
		diskBlockOrig := "DiskBlock.ergo"
		diskBlockWant, _ := lint.NormalizeErgoFileName(diskBlockOrig, style)
		if diskBlockWant != diskBlockOrig {
			writeFixModFile(t, root, "other/"+diskBlockOrig, "func BlockFn() int { 1 }\n")
			if err := os.MkdirAll(filepath.Join(root, "other", diskBlockWant), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		// Dry-run (Write: false) plans without mutating disk.
		dryRep, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: false})
		if err != nil {
			t.Fatalf("seed %d: dry run: %v", seed, err)
		}
		if origName != wantName && len(dryRep.Renames) == 0 {
			t.Fatalf("seed %d: expected planned rename for %s -> %s", seed, origName, wantName)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg", origName)); err != nil {
			t.Fatalf("seed %d: dry run mutated disk (%s missing): %v", seed, origName, err)
		}
		if diskBlockWant != diskBlockOrig {
			if _, err := os.Stat(filepath.Join(root, "other", diskBlockOrig)); err != nil {
				t.Fatalf("seed %d: dry run moved %s", seed, diskBlockOrig)
			}
		}

		// Write: true applies safe rename and skips both kinds of collisions.
		rep, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: true})
		if err != nil {
			t.Fatalf("seed %d: apply: %v", seed, err)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg", wantName)); err != nil {
			t.Fatalf("seed %d: missing renamed file %s: %v", seed, wantName, err)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg", suppressedName)); err != nil {
			t.Fatalf("seed %d: suppressed file disappeared: %v", seed, err)
		}
		if colTarget != "DupWord.ergo" {
			if _, err := os.Stat(filepath.Join(root, "pkg", "DupWord.ergo")); err != nil {
				t.Fatalf("seed %d: colliding DupWord.ergo was lost: %v", seed, err)
			}
		}
		if diskBlockWant != diskBlockOrig {
			if _, err := os.Stat(filepath.Join(root, "other", diskBlockOrig)); err != nil {
				t.Fatalf("seed %d: DiskBlock.ergo overwritten onto directory: %v", seed, err)
			}
			foundDiskSkip := false
			for _, sk := range rep.Skipped {
				if filepath.Base(sk.From) == diskBlockOrig {
					foundDiskSkip = true
					break
				}
			}
			if !foundDiskSkip {
				t.Fatalf("seed %d: expected Skipped entry for %s, got %+v", seed, diskBlockOrig, rep.Skipped)
			}
		}

		assertModuleChecksClean(t, root)

		// Second run: 0 renames (idempotent).
		rep2, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: true})
		if err != nil {
			t.Fatalf("seed %d: second run: %v", seed, err)
		}
		if len(rep2.Renames) != 0 {
			t.Fatalf("seed %d: expected 0 renames on second run, got %+v", seed, rep2.Renames)
		}
	}
}

// TestFixFileNameCaseDirsAndImports checks folder renaming under dirs=true,
// deepest-first dir renames, intra-repo import path and qualifier updates,
// required aliasing for kebab-case package paths, and dropping an import alias
// that becomes redundant after directory renaming, all checking cleanly.
// Mutants: file-name-case-fix-drops-dirs, file-name-case-fix-skips-imports-on-dir-rename,
// file-name-case-fix-drops-kebab-alias, file-name-case-fix-keeps-redundant-alias.
func TestFixFileNameCaseDirsAndImports(t *testing.T) {
	t.Run("snake_case_dirs_updates_imports_and_qualifiers", func(t *testing.T) {
		root := t.TempDir()
		writeFixModFile(t, root, "ergo.mod", "module example.com/app\n")
		writeFixModFile(t, root, "ergo.lint.json", `{"version":1,"rules":{"file-name-case":{"options":{"case":"snake_case","dirs":true}}}}`)
		writeFixModFile(t, root, "Net/HTTPClient/ClientCore.ergo", "func Fetch() int { 42 }\n")
		// Package where SubWorker originally clashed with type SubWorker, needing alias sw;
		// after renaming SubWorker -> sub_worker, alias sw is no longer allowed by decl.go and must drop.
		writeFixModFile(t, root, "Sub/SubWorker/Worker.ergo", "func Work() int { 7 }\n")
		writeFixModFile(t, root, "cmd/MyApp/MainEntry.ergo", strings.Join([]string{
			"import example.com/app/Net/HTTPClient",
			"import sw example.com/app/Sub/SubWorker",
			"",
			"type SubWorker { ID int }",
			"",
			"func main() {",
			"\tlet _ = HTTPClient.Fetch() + sw.Work()",
			"}",
			"",
		}, "\n"))

		assertModuleChecksClean(t, root)

		rep, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: true})
		if err != nil {
			t.Fatalf("RunFileCase: %v", err)
		}
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skipped: %+v", rep.Skipped)
		}
		if len(rep.ImportUpdates) != 2 {
			t.Fatalf("expected 2 import updates, got %+v", rep.ImportUpdates)
		}
		// Verify new file & folder paths exist.
		for _, wantRel := range []string{
			"net/http_client/client_core.ergo",
			"sub/sub_worker/worker.ergo",
			"cmd/my_app/main_entry.ergo",
		} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(wantRel))); err != nil {
				t.Fatalf("missing %s: %v", wantRel, err)
			}
		}
		assertModuleChecksClean(t, root)

		// Second run changes nothing.
		rep2, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(rep2.Renames) != 0 || len(rep2.ImportUpdates) != 0 {
			t.Fatalf("expected idempotent second run, got %+v", rep2)
		}
	})

	t.Run("kebab_case_dirs_adds_required_alias", func(t *testing.T) {
		root := t.TempDir()
		writeFixModFile(t, root, "ergo.mod", "module example.com/kebab\n")
		writeFixModFile(t, root, "ergo.lint.json", `{"version":1,"rules":{"file-name-case":{"options":{"case":"kebab-case","dirs":true}}}}`)
		writeFixModFile(t, root, "HTTPClient/Helper.ergo", "func Ping() int { 1 }\n")
		writeFixModFile(t, root, "cmd/app/Main.ergo", strings.Join([]string{
			"import example.com/kebab/HTTPClient",
			"",
			"func main() {",
			"\tlet _ = HTTPClient.Ping()",
			"}",
			"",
		}, "\n"))

		assertModuleChecksClean(t, root)
		rep, err := fix.RunFileCase(fix.FileCaseOptions{Paths: []string{root}, Write: true})
		if err != nil {
			t.Fatalf("RunFileCase: %v", err)
		}
		if len(rep.Skipped) != 0 {
			t.Fatalf("unexpected skips: %+v", rep.Skipped)
		}
		mainSrc, err := os.ReadFile(filepath.Join(root, "cmd/app/main.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(mainSrc), "import HTTPClient example.com/kebab/http-client") {
			t.Fatalf("expected aliased kebab-case import, got:\n%s", string(mainSrc))
		}
		assertModuleChecksClean(t, root)
	})
}

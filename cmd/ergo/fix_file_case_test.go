package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/fix"
)

func TestFixLintFileNameCaseE2E(t *testing.T) {
	t.Run("dirs_false_and_check_dry_run_idempotent", func(t *testing.T) {
		root := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("ergo.mod", "module example.com/e2e\n")
		write("ergo.lint.json", `{"version":1,"rules":{"file-name-case":{"severity":"error","options":{"case":"snake_case"}}}}`)
		write("pkg/HTTPClient_linux_test.ergo", "test PingLinux { assert 1 == 1 }\n")
		write("pkg/APIRoutes.ergo", "func Route() int { 1 }\n")

		// Before fix: lint fails due to file-name-case errors.
		if _, code := commandInProcess(t, "lint", filepath.Join(root, "...")); code == 0 {
			t.Fatalf("expected ergo lint to fail before fix, exited 0")
		}

		// --check reports planned renames, exits 1, and leaves disk untouched.
		out, code := commandInProcess(t, "fix", "--lint", "--check", "--json", filepath.Join(root, "..."))
		if code != 1 {
			t.Fatalf("fix --lint --check exit = %d, want 1; out:\n%s", code, out)
		}
		var rep fixReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("unmarshal check json: %v\nout:\n%s", err, out)
		}
		if rep.Written || len(rep.Linted) != 2 {
			t.Fatalf("unexpected check report: %+v", rep)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg/APIRoutes.ergo")); err != nil {
			t.Fatalf("fix --check modified disk: %v", err)
		}

		// --dry-run exits 0 and writes nothing.
		out, code = commandInProcess(t, "fix", "--lint", "--dry-run", "--json", filepath.Join(root, "..."))
		if code != 0 {
			t.Fatalf("fix --lint --dry-run exit = %d, want 0; out:\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg/APIRoutes.ergo")); err != nil {
			t.Fatalf("fix --dry-run modified disk: %v", err)
		}

		// Apply renames cleanly.
		out, code = commandInProcess(t, "fix", "--lint", "--json", filepath.Join(root, "..."))
		if code != 0 {
			t.Fatalf("fix --lint exit = %d, want 0; out:\n%s", code, out)
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("unmarshal fix json: %v", err)
		}
		if !rep.Written || len(rep.Linted) != 2 {
			t.Fatalf("unexpected fix report: %+v", rep)
		}
		for _, want := range []string{"pkg/api_routes.ergo", "pkg/http_client_linux_test.ergo"} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(want))); err != nil {
				t.Fatalf("missing renamed file %s: %v", want, err)
			}
		}

		// Lint and check both pass cleanly now.
		if lout, lcode := commandInProcess(t, "lint", filepath.Join(root, "...")); lcode != 0 {
			t.Fatalf("ergo lint after fix failed (%d):\n%s", lcode, lout)
		}
		if cout, ccode := commandInProcess(t, "check", filepath.Join(root, "...")); ccode != 0 {
			t.Fatalf("ergo check after fix failed (%d):\n%s", ccode, cout)
		}

		// Second fix run is idempotent with 0 changes.
		out, code = commandInProcess(t, "fix", "--lint", "--check", "--json", filepath.Join(root, "..."))
		if code != 0 {
			t.Fatalf("second fix --lint --check exit = %d, want 0; out:\n%s", code, out)
		}
		var rep2 struct {
			Written bool         `json:"written"`
			Linted  []fix.Linted `json:"linted"`
		}
		if err := json.Unmarshal([]byte(out), &rep2); err != nil {
			t.Fatal(err)
		}
		if rep2.Written || len(rep2.Linted) != 0 {
			t.Fatalf("expected 0 changes on second run, got %+v", rep2)
		}
	})

	t.Run("dirs_true_updates_imports_and_refuses_collisions", func(t *testing.T) {
		root := t.TempDir()
		write := func(rel, src string) {
			t.Helper()
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("ergo.mod", "module example.com/e2edirs\n")
		write("ergo.lint.json", `{"version":1,"rules":{"file-name-case":{"options":{"case":"snake_case","dirs":true}}}}`)
		write("Services/AuthAPI/TokenStore.ergo", "func Issue() int { 9 }\n")
		write("cmd/AppEntry/MainFile.ergo", "import example.com/e2edirs/Services/AuthAPI\n\nfunc main() {\n\tlet _ = AuthAPI.Issue()\n}\n")

		if err := command([]string{"fix", "--lint", "--dry-run", root}); err != nil {
			t.Fatalf("fix --lint --dry-run dirs=true err: %v", err)
		}
		out, code := commandInProcess(t, "fix", "--lint", root)
		if code != 0 {
			t.Fatalf("fix --lint dirs=true exit = %d:\n%s", code, out)
		}
		if !strings.Contains(string(out), "fixed file-name-case") {
			t.Fatalf("expected human output naming file-name-case, got:\n%s", out)
		}
		if lout, lcode := commandInProcess(t, "lint", "--strict", filepath.Join(root, "...")); lcode != 0 {
			t.Fatalf("lint --strict after dir rename failed (%d):\n%s", lcode, lout)
		}
		if cout, ccode := commandInProcess(t, "check", filepath.Join(root, "...")); ccode != 0 {
			t.Fatalf("check after dir rename failed (%d):\n%s", ccode, cout)
		}

		// Add a collision and ensure colliding files are left untouched and reported with exit 1.
		write("pkg/CollideMe.ergo", "func A() int { 1 }\n")
		write("pkg/collide_me.ergo", "func B() int { 2 }\n")
		cout, ccode := commandInProcess(t, "fix", "--lint", "--json", root)
		if ccode != 1 {
			t.Fatalf("expected exit 1 on collision, got %d:\n%s", ccode, cout)
		}
		var rep fixReport
		if err := json.Unmarshal([]byte(cout), &rep); err != nil {
			t.Fatalf("unmarshal collision report: %v\nout:\n%s", err, cout)
		}
		if len(rep.Remaining) == 0 || rep.Remaining[0].Code != "L172" {
			t.Fatalf("expected remaining L172 collision diagnostic, got %+v", rep.Remaining)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg/CollideMe.ergo")); err != nil {
			t.Fatalf("colliding source missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "pkg/collide_me.ergo")); err != nil {
			t.Fatalf("colliding target missing: %v", err)
		}
	})
}

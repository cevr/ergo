package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestErgoRunContextOnlyPackage (196b U4 round 1 B1, the verifier's CLI
// repros): a program that names a provider package only through one of
// its contexts checks, and then builds and runs; before the fix `ergo run`
// stopped with an internal compiler error from the naming pass.
//   - shop: `with mem.Paying { p.Use() }` (cmd/app) and `let Live =
//     context { mem.Paying }` (cmd/app2); main names mem nowhere else.
//   - examples/checkout without Declining and the tests: main names the
//     memory package only in `let Live = context { …, memory.Paying }`.
func TestErgoRunContextOnlyPackage(t *testing.T) {
	binary := buildTestCLI(t)
	workspace := smokeWorkspace(t, "cmd/ergo/testdata/ctxreach", readmeSmokeProgram)
	shop := filepath.Join(workspace, "cmd", "ergo", "testdata", "ctxreach", "shop")
	for _, app := range []string{"cmd/app", "cmd/app2"} {
		stdout, stderr, code := runSmokeCLI(t, packagesDeadline, binary, shop, "", "run", app)
		if code != 0 || string(stdout) != "mem\n" {
			t.Errorf("shop: ergo run %s: exit %d stdout %q, want mem\n%s", app, code, stdout, stderr)
		}
	}

	checkout := filepath.Join(workspace, filepath.FromSlash(readmeSmokeProgram))
	mainPath := filepath.Join(checkout, "cmd", "checkout", "main.ergo")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	const declining = "let Declining = context { Live, replace payments.Payments: memory.Decliner }\n"
	tests := strings.Index(src, "\ntest ChargeSucceeds")
	if strings.Count(src, declining) != 1 || tests < 0 {
		t.Fatal("examples/checkout/cmd/checkout/main.ergo no longer holds Declining and its tests; update this repro")
	}
	src = strings.Replace(src[:tests+1], declining, "", 1)
	if strings.Contains(src, "memory.Decliner") || !strings.Contains(src, "memory.Paying") {
		t.Fatalf("the repro still names memory outside Live:\n%s", src)
	}
	if err := os.WriteFile(mainPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSmokeCLI(t, packagesDeadline, binary, checkout, "", "check", "cmd/checkout")
	if code != 0 || !strings.Contains(string(stdout), `"diagnostics":[]`) {
		t.Fatalf("checkout without Declining: ergo check: exit %d stdout %s\n%s", code, stdout, stderr)
	}
	stdout, stderr, code = runSmokeCLI(t, packagesDeadline, binary, checkout, "", "run", "cmd/checkout")
	if code != 0 || string(stdout) != "paid\n" {
		t.Errorf("checkout without Declining: ergo run: exit %d stdout %q, want paid\n%s", code, stdout, stderr)
	}
}

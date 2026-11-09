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
//   - twoop: `with Mine, n.Q { both() }`; n is named only by the second
//     operand of the with (round 2 M1).
//   - examples/checkout without Declining and the tests: main names the
//     memory package only in `let Memory = context { …, memory.Paying }`,
//     and main reaches Memory through its local context `live`.
func TestErgoRunContextOnlyPackage(t *testing.T) {
	binary := buildTestCLI(t)
	workspace := smokeWorkspace(t, "cmd/ergo/testdata/ctxreach", readmeSmokeProgram)
	shop := filepath.Join(workspace, "cmd", "ergo", "testdata", "ctxreach", "shop")
	for _, app := range []string{"cmd/app", "cmd/app2"} {
		stdout, stderr, code := runSmokeCLI(t, binary, shop, "", "run", app)
		if code != 0 || string(stdout) != "mem\n" {
			t.Errorf("shop: ergo run %s: exit %d stdout %q, want mem\n%s", app, code, stdout, stderr)
		}
	}
	// twoop (round 2 M1, the verifier's probe): `with Mine, n.Q { … }`,
	// where the second operand is the only name of n.
	twoop := filepath.Join(workspace, "cmd", "ergo", "testdata", "ctxreach", "twoop")
	if stdout, stderr, code := runSmokeCLI(t, binary, twoop, "", "run", "cmd/twoop"); code != 0 || string(stdout) != "pl hi\n" {
		t.Errorf("twoop: ergo run cmd/twoop: exit %d stdout %q, want pl hi\n%s", code, stdout, stderr)
	}

	checkout := filepath.Join(workspace, filepath.FromSlash(readmeSmokeProgram))
	mainPath := filepath.Join(checkout, "cmd", "checkout", "main.ergo")
	data, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	const declining = "let Declining = context { Memory, replace payments.Payments: memory.Decliner }\n"
	tests := strings.Index(src, "\ntest ChargeSucceeds")
	if strings.Count(src, declining) != 1 || tests < 0 {
		t.Fatal("examples/checkout/cmd/checkout/main.ergo no longer holds Declining and its tests; update this repro")
	}
	src = strings.Replace(src[:tests+1], declining, "", 1)
	// The cut main names the memory package only inside the context Memory,
	// which main reaches through the local context `live` (Memory embedded,
	// memory.Paying embedded in Memory: two embeddings deep).
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "memory.") && !strings.HasPrefix(line, "let Memory = context {") {
			t.Fatalf("the repro names memory outside the context Memory: %q\n%s", line, src)
		}
	}
	if !strings.Contains(src, "memory.Paying") || !strings.Contains(src, "context { Memory,") {
		t.Fatalf("the repro no longer reaches memory.Paying through Memory:\n%s", src)
	}
	if err := os.WriteFile(mainPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSmokeCLI(t, binary, checkout, "", "check", "cmd/checkout")
	if code != 0 || !strings.Contains(string(stdout), `"diagnostics":[]`) {
		t.Fatalf("checkout without Declining: ergo check: exit %d stdout %s\n%s", code, stdout, stderr)
	}
	stdout, stderr, code = runSmokeCLI(t, binary, checkout, "", "run", "cmd/checkout")
	if code != 0 || string(stdout) != "paid\n" {
		t.Errorf("checkout without Declining: ergo run: exit %d stdout %q, want paid\n%s", code, stdout, stderr)
	}
}

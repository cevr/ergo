package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/sketch/fix"
)

// TestGenCheckReadsThePackage catches r3b-spell (the generated marker
// records how the command spelled the package, so --check from another
// folder reports current files stale). Smallest repro of the X-rpc1 bug:
// `ergo gen rpc --check examples/limiter/limits.Limiter` from the repo root
// failed on files generated as `limits.Limiter` from the example's folder.
// No other test runs --check from a second spelling; it runs in process.
func TestGenCheckReadsThePackage(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "mod")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "ergo.mod"), "module mod\n")
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	for _, kind := range []string{"api", "rpc"} {
		if err := genCommand([]string{kind, "api.API"}); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	for _, spelling := range []string{"mod/api.API", "./mod/api/.API", filepath.Join(mod, "api") + ".API"} {
		for _, kind := range []string{"api", "rpc"} {
			if err := genCommand([]string{kind, "--check", spelling}); err != nil {
				t.Errorf("%s --check %s: %v", kind, spelling, err)
			}
		}
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int, Note string }\ntype API { Read(key string) Item }\n")
	if err := genCommand([]string{"rpc", "--check", "mod/api.API"}); err == nil {
		t.Error("--check passed a contract that changed")
	}
}

// TestGenCliCheckIsRedOnAStaleFile catches ap1-cli-check-blind (ergo gen
// cli --check passes a cli.ergo the contract no longer generates),
// ap1-cli-stale-rpc (--check compares no file's text, so a changed
// contract passes), af-register-transport-skips-hub (ergo gen cli writes
// no hub, so the cli package names a missing one; and --check passes a
// hub edited by hand) and ap1-cli-own-request (the
// cli package is written over no hub, so a caller holds two types for one
// call). It runs in process: the question is which files the command
// compares, not what they do.
func TestGenCliCheckIsRedOnAStaleFile(t *testing.T) {
	mod := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "ergo.mod"), "module mod\n")
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	// No hub yet: ergo gen cli writes the hub it is written over too, so
	// no order of gen commands leaves it naming a missing hub.
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mod, "api", "apiapi", "api.ergo")); err != nil {
		t.Errorf("ergo gen cli wrote no hub: %v", err)
	}
	if err := genCommand([]string{"cli", "--check", "api.API"}); err != nil {
		t.Fatalf("--check of a current cli package: %v", err)
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string, all bool) Item }\n")
	if err := genCommand([]string{"cli", "--check", "api.API"}); err == nil {
		t.Error("--check passed a cli package whose contract changed")
	}
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	// A hub edited by hand: the cli package's --check checks the hub too.
	hub := filepath.Join(mod, "api", "apiapi", "api.ergo")
	text, err := os.ReadFile(hub)
	if err != nil {
		t.Fatal(err)
	}
	write(hub, string(text)+"\n// edited\n")
	if err := genCommand([]string{"cli", "--check", "api.API"}); err == nil || !strings.Contains(err.Error(), "apiapi") {
		t.Errorf("--check of a cli package over a hub edited by hand: %v", err)
	}
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(filepath.Join(mod, "api", "apicli", "cli.ergo"))
	if err != nil || !strings.Contains(string(cli), "type Request = apiapi.Request") {
		t.Errorf("the cli package does not name the hub's Request: %v\n%s", err, cli)
	}
}

// TestGenSidecarKeepsSchemasAndOnlySkipsGeneratedFolders covers two
// GEN-SIDECAR regressions: a second schema replaces the first in the
// marker, and the format/comment walkers mistake a copied marker for
// ownership. It also checks that schema output keeps public docs but no
// removable comments, and that --check treats the sidecar as output.
func TestGenSidecarKeepsSchemasAndOnlySkipsGeneratedFolders(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFiles(t, root, map[string]string{
		"ergo.mod":         "module mod\n",
		"wire/types.ergo":  "type Alpha { Value string }\ntype Beta { Count int }\n",
		"gostd/probe.ergo": "// hand-written\n\nlet x = 1\n",
	})
	check := func(args ...string) {
		t.Helper()
		if err := genCommand(args); err != nil {
			t.Fatal(err)
		}
	}
	check("schema", "wire.Alpha")
	check("schema", "wire.Beta")
	check("schema", "--check", "wire.Alpha")
	check("schema", "--check", "wire.Beta")
	owned := filepath.Join(root, "wire", "wireschema")
	if !genmark.Held(owned) {
		t.Fatal("the two generated schemas do not share one current marker")
	}
	file := filepath.Join(owned, "alpha.ergo")
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, removed, err := fix.Comments(string(src), file); err != nil || len(removed) != 0 || !strings.Contains(string(src), "// Alpha is the JSON schema") {
		t.Fatalf("generated schema should have only public docs: removed %v, error %v\n%s", removed, err, src)
	}
	sidecar, err := os.ReadFile(filepath.Join(owned, genmark.Name))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"gostd/" + genmark.Name: string(sidecar)})
	copied := filepath.Join(root, "gostd", "probe.ergo")
	for _, operand := range []string{root, filepath.Join(root, "...")} {
		opts := fmtOptions{files: []string{operand}}
		if err := expandFormatFolders(&opts); err != nil {
			t.Fatal(err)
		}
		for _, path := range opts.files {
			if filepath.Dir(path) == owned {
				t.Errorf("fmt %s entered generated folder %s", operand, owned)
			}
		}
		if !slices.Contains(opts.files, copied) {
			t.Errorf("fmt %s skipped hand-written file with a copied marker %s", operand, copied)
		}
	}
	var taskFiles []string
	if err := walkGateSources(root, nil, func(file string) error {
		taskFiles = append(taskFiles, file)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(taskFiles, copied) || slices.Contains(taskFiles, file) {
		t.Errorf("task fmt must skip the held marker but not a copied one: %v", taskFiles)
	}
	// Put a real, stamped binding tree under a non-reserved name so the
	// traversal itself, not binding-folder name policy, must skip it.
	writeFiles(t, root, map[string]string{"bindings/go/probe.ergo": "// binding header\n\nlet x = 1\n"})
	if err := genmark.Stamp(filepath.Join(root, "bindings"), genmark.Marker{Kind: "bind", Generator: "ergo bind go"}); err != nil {
		t.Fatal(err)
	}
	rep, err := fix.RunComments(fix.Options{Paths: []string{root}})
	if err != nil || len(rep.Files) != 1 || rep.Files[copied] != "let x = 1\n" {
		t.Fatalf("fix --comments should visit only the hand-written probe: %v, %v", rep.Files, err)
	}

	if err := os.WriteFile(file, append(src, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := genCommand([]string{"schema", "--check", "wire.Beta"}); err == nil || !strings.Contains(err.Error(), "alpha.ergo") {
		t.Fatalf("schema --check should see a stale sibling: %v", err)
	}
	check("schema", "wire.Beta")
	stale := strings.Replace(string(sidecar), "generator ergo gen schema", "generator stale", 1)
	if err := os.WriteFile(filepath.Join(owned, genmark.Name), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := genCommand([]string{"schema", "--check", "wire.Beta"}); err == nil || !strings.Contains(err.Error(), genmark.Name) {
		t.Fatalf("schema --check should see a stale marker: %v", err)
	}
}

// TestGenSidecarGateReadsTheContract catches genmark-gate-ignores-sidecar:
// a generated package with no header would otherwise leave the gate
// task with no generation check. Use its shared member selection seam.
func TestGenSidecarGateReadsTheContract(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeFiles(t, root, map[string]string{
		"ergo.mod":     "module mod\n",
		"api/api.ergo": "type API { Read(key string) string }\n",
	})
	if err := genCommand([]string{"rpc", "api.API"}); err != nil {
		t.Fatal(err)
	}
	var elsewhere []Elsewhere
	generated, err := memberGens(root, ".", nil, &elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"api", "rpc"} {
		if !slices.ContainsFunc(generated, func(g memberGen) bool {
			return g.kind == kind && g.name == "API" && g.contract == "api"
		}) {
			t.Errorf("gate found no %s.API in the generated markers: %v", kind, generated)
		}
	}
}

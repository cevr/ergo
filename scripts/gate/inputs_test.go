package main

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// syncGoSum is tools/provenance/go.sum's lines for golang.org/x/sync
// v0.24.0, as the Go checksum database gives them: the hashes seedModules
// must reproduce from the module's files.
const syncGoSum = "golang.org/x/sync v0.24.0 h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=\n" +
	"golang.org/x/sync v0.24.0/go.mod h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=\n"

// moduleHost is a host module cache with x/sync v0.24.0's download, copied
// from this host's, or the test is skipped.
func moduleHost(t *testing.T) string {
	t.Helper()
	goTool := realTools["go"]
	if goTool == "" {
		t.Skip("no go on PATH")
	}
	output, err := exec.Command(goTool, "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(strings.TrimSpace(string(output)), "cache", "download", "golang.org", "x", "sync", "@v")
	host := t.TempDir()
	to := filepath.Join(host, "cache", "download", "golang.org", "x", "sync", "@v")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v0.24.0.zip", "v0.24.0.mod"} {
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			t.Skipf("this host's module cache lacks x/sync v0.24.0: %v", err)
		}
		if err := os.WriteFile(filepath.Join(to, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return host
}

// moduleCommit commits files into a fixture repository and returns its
// object store and head.
func moduleCommit(t *testing.T, files map[string]string) (source, head string) {
	t.Helper()
	root := fixture(t)
	for name, content := range files {
		write(t, root, name, content)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "modules")
	return filepath.Join(root, ".git"), strings.TrimSpace(gitIn(t, root, "rev-parse", "HEAD"))
}

// TestSeedModulesVerifiesGoSum: the run's module cache gets a module's
// files only when they hash to go.sum's lines; a changed go.mod or a
// changed file in the zip fails closed and names the file.
func TestSeedModulesVerifiesGoSum(t *testing.T) {
	host := moduleHost(t)
	source, head := moduleCommit(t, map[string]string{
		"tool/go.mod": "module example.com/tool\n\ngo 1.21\n\nrequire golang.org/x/sync v0.24.0\n",
		"tool/go.sum": syncGoSum,
	})
	dir := filepath.Join(host, "cache", "download", "golang.org", "x", "sync", "@v")
	dest := t.TempDir()
	seeded, err := seedModules(source, head, host, dest)
	if err != nil || seeded != 2 {
		t.Fatalf("seeded %d files: %v; want both", seeded, err)
	}
	for _, name := range []string{"v0.24.0.zip", "v0.24.0.mod"} {
		want, _ := os.ReadFile(filepath.Join(dir, name))
		got, err := os.ReadFile(filepath.Join(dest, "cache", "download", "golang.org", "x", "sync", "@v", name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s not copied whole: %v", name, err)
		}
	}
	t.Run("changed go.mod", func(t *testing.T) {
		mod := filepath.Join(dir, "v0.24.0.mod")
		data, _ := os.ReadFile(mod)
		if err := os.WriteFile(mod, append(data, "\n// changed\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(mod, data, 0o644)
		if _, err := seedModules(source, head, host, t.TempDir()); err == nil || !strings.Contains(err.Error(), mod) || !strings.Contains(err.Error(), "has hash") {
			t.Fatalf("a changed go.mod: %v; want refused naming %s", err, mod)
		}
	})
	t.Run("changed zip", func(t *testing.T) {
		archive := filepath.Join(dir, "v0.24.0.zip")
		data, _ := os.ReadFile(archive)
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		var changed bytes.Buffer
		writer := zip.NewWriter(&changed)
		for i, file := range reader.File {
			content, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			body.ReadFrom(content)
			content.Close()
			if i == 0 {
				body.WriteString("// changed\n")
			}
			out, err := writer.Create(file.Name)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(body.Bytes())
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, changed.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(archive, data, 0o644)
		if _, err := seedModules(source, head, host, t.TempDir()); err == nil || !strings.Contains(err.Error(), archive) || !strings.Contains(err.Error(), "has hash") {
			t.Fatalf("a changed zip: %v; want refused naming %s", err, archive)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if seeded, err := seedModules(source, head, t.TempDir(), t.TempDir()); err != nil || seeded != 0 {
			t.Fatalf("a host without the module: seeded %d: %v; want none, and no error (the build fails without it)", seeded, err)
		}
	})
}

// TestSeedModulesRefusesUnverifiedRequirements: a go.mod that requires a
// module, in a line or a block, with no go.sum beside it fails closed; a
// go.mod without requirements needs none.
func TestSeedModulesRefusesUnverifiedRequirements(t *testing.T) {
	for _, goMod := range []string{
		"module example.com/a\n\ngo 1.21\n\nrequire golang.org/x/sync v0.24.0\n",
		"module example.com/a\n\ngo 1.21\n\nrequire (\n\tgolang.org/x/sync v0.24.0\n)\n",
	} {
		source, head := moduleCommit(t, map[string]string{"nested/go.mod": goMod, "go.sum": syncGoSum})
		if _, err := seedModules(source, head, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "nested/go.mod requires modules and has no go.sum") {
			t.Errorf("%q without a go.sum: %v; want refused", goMod, err)
		}
	}
	source, head := moduleCommit(t, map[string]string{"nested/go.mod": "module example.com/a\n\ngo 1.21\n"})
	if _, err := seedModules(source, head, t.TempDir(), t.TempDir()); err != nil {
		t.Errorf("a go.mod without requirements: %v", err)
	}
}

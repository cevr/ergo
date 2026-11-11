package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoBindingPlatforms catches a binding admitted outside its stated
// platforms, check ignoring GOOS/GOARCH, build ignoring --platform, a missing
// header admitted, and substring platform membership. Other interop tests
// check declaration shapes and module versions, not platform provenance.
// It calls the commands in process. bind reads Go export data; no generated
// binary is built or run, including on the refusal path of buildSource.
func TestGoBindingPlatforms(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"ergo.mod":         "module app\n",
		"go.mod":           "module example.test/platform\n\ngo 1.27\n",
		"greet/greet.go":   "package greet\n\nfunc Hello() string { return \"hello\" }\n",
		"main.ergo":        "import app/helper\n\nfunc main() needs go.Foreign { helper.Hello() }\n",
		"helper/main.ergo": "import go example.test/platform/greet\n\nfunc Hello() string needs go.Foreign { greet.Hello() }\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	// Command output is small and written to a regular file, not a pipe.
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = stdout })
	refused := func(t *testing.T, platforms string, command func() error) {
		t.Helper()
		if err := out.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if err := command(); err == nil {
			t.Error("accepted a binding without linux/arm64")
		}
		text, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"example.test/platform/greet", platforms, "linux/arm64", "ergo bind go"} {
			if !strings.Contains(string(text), want) {
				t.Errorf("diagnostic omits %q:\n%s", want, text)
			}
		}
	}
	bindingPath := filepath.Join(root, "ergo.bind/example.test/platform/greet/greet.ergo")
	if err := os.MkdirAll(filepath.Dir(bindingPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "// ergo:module example.test/platform\n\nextern go example.test/platform/greet.Hello func Hello() string needs go.Foreign\n"
	const old = "// ergo:bind example.test/platform/greet go1.27 darwin/arm64 linux/amd64\n"
	for _, tc := range []struct{ name, header, platforms string }{
		{"old", old, "darwin/arm64 linux/amd64"},
		{"missing", "", "(none)"},
		{"substring", "// ergo:bind example.test/platform/greet go1.27 linux/arm64-extra\n", "linux/arm64-extra"},
		{"wrong-package", "// ergo:bind example.test/other go1.27 linux/arm64\n", "(none)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(bindingPath, []byte(tc.header+body), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOOS", "linux")
			t.Setenv("GOARCH", "arm64")
			refused(t, tc.platforms, func() error { return checkCommand([]string{root}) })
			// The explicit build target must beat a host-compatible environment.
			t.Setenv("GOARCH", "amd64")
			opts, err := parseFileOptions("build", []string{"--platform", "linux/arm64", root})
			if err != nil {
				t.Fatal(err)
			}
			refused(t, tc.platforms, func() error {
				_, err := buildSource(opts, func(string, string) string {
					t.Fatal("build placed an artifact before refusing its binding")
					return ""
				})
				return err
			})
		})
	}
	// Regenerate an old binding in place with the real bind command.
	if err := os.WriteFile(bindingPath, []byte(old+body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"darwin/arm64", "linux/amd64"} {
		goos, goarch, _ := strings.Cut(platform, "/")
		t.Setenv("GOOS", goos)
		t.Setenv("GOARCH", goarch)
		if err := checkCommand([]string{root}); err != nil {
			t.Errorf("old binding states %s but was refused: %v", platform, err)
		}
	}
	if err := bindCommand([]string{"go", "example.test/platform/greet"}); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"} {
		if !strings.Contains(string(text), " "+platform) {
			t.Errorf("regenerated binding omits %s", platform)
		}
		goos, goarch, _ := strings.Cut(platform, "/")
		t.Setenv("GOOS", goos)
		t.Setenv("GOARCH", goarch)
		if err := checkCommand([]string{root}); err != nil {
			t.Errorf("regenerated binding for %s: %v", platform, err)
		}
	}
}

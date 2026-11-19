package build_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
)

// TestCoverBuildCountsInTheTestsDir builds an emitted-shaped module (module
// ergo.local/program, the runtime replaced at ./ergo, //line directives to
// a .ergo file) with Toolchain.Cover set, and runs it with no GOCOVERDIR
// under a parent that has one, as the examples runner starts a case. The
// program must print no cover warning and see no GOCOVERDIR, its counters
// must land in the parent's GOCOVERDIR, and their blocks must be named
// under the copy's own module path (ergo.local/cover/KEY), with the copy
// at Cover/KEY. Mutants (testdata/mutants.json): gt7-no-ancestor,
// gt7-not-unset, gt7-shared-module-path, gt7-replace-relative.
// It builds Go because the facts are the go command's coverage build and the cover
// runtime's counters; no in-process check runs them.
func TestCoverBuildCountsInTheTestsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	if runtime.GOOS != "linux" {
		t.Skip("covdir finds the ancestor's GOCOVERDIR through /proc (Linux)")
	}
	module := t.TempDir()
	writeFiles(t, module, map[string]string{
		"go.mod":        "module ergo.local/program\n\ngo 1.22\n\nrequire ergo.local/prototype v0.0.0\n\nreplace ergo.local/prototype => ./ergo\n",
		"ergo/go.mod":   "module ergo.local/prototype\n\ngo 1.22\n",
		"ergo/rt/rt.go": "package rt\n\nfunc Name() string { return \"rt\" }\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\n\t\"ergo.local/prototype/rt\"\n)\n\nfunc main() {\n" +
			"//line /src/app/thing.ergo:10:1\n\tif len(os.Args) > 1 {\n//line /src/app/thing.ergo:11:1\n\t\tfmt.Println(rt.Name())\n\t}\n" +
			"\tfmt.Printf(\"GOCOVERDIR=%q\\n\", os.Getenv(\"GOCOVERDIR\"))\n}\n",
	})
	cover := t.TempDir()
	output := filepath.Join(t.TempDir(), "program")
	var stderr bytes.Buffer
	tc := build.Toolchain{Go: "go", Cover: cover}
	if _, err := tc.Build(build.Cache{}, build.Module{Dir: module, Key: "0123456789abcdef0123"}, output, &stderr); err != nil {
		t.Fatalf("coverage build: %v\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(cover, "0123456789abcdef", "main.go")); err != nil {
		t.Fatalf("the copy is not at Cover/KEY: %v", err)
	}
	counters := t.TempDir()
	// The parent (sh) has GOCOVERDIR; env -u starts the program without it.
	cmd := exec.Command("sh", "-c", `env -u GOCOVERDIR "$0" run; true`, output)
	cmd.Env = append(cleanEnv(), "GOCOVERDIR="+counters)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := proclimit.Run(cmd, 512<<20); err != nil {
		t.Fatalf("run: %v\n%s", err, errOut.String())
	}
	if errOut.Len() > 0 {
		t.Errorf("the program printed on stderr:\n%s", errOut.String())
	}
	if got := out.String(); got != "rt\nGOCOVERDIR=\"\"\n" {
		t.Errorf("the program printed %q, want its own output and no GOCOVERDIR", got)
	}
	profile := filepath.Join(t.TempDir(), "profile.txt")
	text, err := exec.Command("go", "tool", "covdata", "textfmt", "-i="+counters, "-o="+profile).CombinedOutput()
	if err != nil {
		t.Fatalf("covdata: %v\n%s", err, text)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\nergo.local/cover/0123456789abcdef/main.go:") && !strings.Contains(string(data), "\nergo.local/cover/0123456789abcdef/thing.ergo:") {
		t.Errorf("no block of the copy's module in the parent's GOCOVERDIR:\n%s", data)
	}
}

// cleanEnv is the test's environment without GOCOVERDIR.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOCOVERDIR=") {
			out = append(out, kv)
		}
	}
	return out
}

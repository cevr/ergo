package bindc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// liveChecks are the lines the live driver (testdata/live/main.go) prints,
// one per check, in order.
var liveChecks = []string{
	"not loaded before Load",
	"abi mismatch",
	"not loaded after the abi mismatch",
	"missing symbol",
	"not loaded after the missing symbol",
	"load",
	"autofill checked by C",
	"node",
	"input buffer round trip",
	"empty input buffer",
	"empty output buffer",
	"out-parameter",
	"struct round trip",
	"struct by value",
	"wrong thread",
	"stale handle",
	"destroy",
	"call after destroy",
	"destroy after destroy",
}

// TestLive (ERGO_BINDC_LIVE=1; it needs cc and the network): in a
// temporary module with the real purego v0.11.3 (testdata/purego.sum),
// both golden Go packages build with CGO_ENABLED=0 for linux and darwin on
// amd64 and arm64 and pass go vet, and a CGO_ENABLED=0 program drives the
// fixture library through the typed layer (testdata/live/main.go).
func TestLive(t *testing.T) {
	if os.Getenv("ERGO_BINDC_LIVE") != "1" {
		t.Skip("set ERGO_BINDC_LIVE=1 to build against the real purego and drive the fixture library")
	}
	cc := requireCC(t)
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	root := t.TempDir()
	mod := filepath.Join(root, "mod")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "go.mod"), []byte("module ergo.local/bind\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	write(filepath.Join(mod, "go.sum"), readInput(t, "testdata/purego.sum"))
	for _, h := range headers {
		m := parseHeader(t, h.dir, h.header)
		b := bindHeader(t, h.dir, h.header, m)
		rel := strings.TrimPrefix(b.Contract.Go.ImportPath, "ergo.local/bind/")
		for name, src := range goldenGoFiles(t, h.dir) {
			write(filepath.Join(mod, rel, name), src)
		}
	}
	write(filepath.Join(mod, "driver", "main.go"), readInput(t, "testdata/live/main.go"))

	env := append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=-mod=readonly -modcacherw", "GOMODCACHE="+filepath.Join(root, "modcache"), "GOWORK=off")
	run := func(dir string, extra []string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(append([]string{}, env...), extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s %v: %v\n%s", name, strings.Join(args, " "), extra, err, out)
		}
		return string(out)
	}
	for _, goos := range []string{"linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			target := []string{"GOOS=" + goos, "GOARCH=" + goarch}
			run(mod, target, goTool, "build", "./...")
			run(mod, target, goTool, "vet", "./...")
		}
	}

	// The fixture library: the good one, one with ABI version 3, and one
	// without fx_node_count.
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	src, _ := filepath.Abs("testdata/fixture/fixture.c")
	libs := map[string][]string{
		"good": nil, "abi3": {"-DFIXTURE_ABI=3"}, "omit": {"-DFIXTURE_OMIT=fx_node_count"},
	}
	paths := map[string]string{}
	for name, flags := range libs {
		paths[name] = filepath.Join(root, "libfixture-"+name+ext)
		args := append([]string{"-std=c11", "-shared", "-fPIC", "-o", paths[name]}, flags...)
		run(root, nil, cc, append(args, src, "-lm", "-lpthread")...)
	}
	driver := filepath.Join(root, "driver-bin")
	run(mod, nil, goTool, "build", "-o", driver, "./driver")
	out := run(root, nil, driver, paths["good"], paths["abi3"], paths["omit"])
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if c, ok := strings.CutPrefix(line, "ok: "); ok {
			got = append(got, c)
		}
	}
	if strings.Join(got, "\n") != strings.Join(liveChecks, "\n") {
		t.Fatalf("the driver's checks:\n%s\nwant:\n%s", out, strings.Join(liveChecks, "\n"))
	}
}

package irspike

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestStockIRSharesTheBuildCache: an IR build with ergo's patched compile
// (Stock) runs in the source path's GOCACHE, so a test process compiles
// the standard library for it only when no process before it did, and the
// IR file copy its -ergoir flag names lies in IRFiles, never beside that
// shared cache (which the closure sweep's sandbox does not let it write).
// A derivative that is not Stock keeps a cache keyed by its compile's
// content. In process: the go command's environment and flags are the
// question. Mutants: heavy-cost-stock-keyed (a Stock compile keyed by
// content again), heavy-cost-ir-beside-cache (the IR copies beside GOCACHE).
func TestStockIRSharesTheBuildCache(t *testing.T) {
	root := t.TempDir()
	goroot := filepath.Join(root, "goroot")
	tool := filepath.Join(goroot, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH, "compile")
	if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("a compile"), 0o755); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(root, "module")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, lower.IRFileName), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, files := filepath.Join(root, "gocache"), filepath.Join(root, "ir")
	envCache := func(env []string) string {
		i := slices.IndexFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "GOCACHE=") })
		return strings.TrimPrefix(env[i], "GOCACHE=")
	}

	stock := Toolchain{GOROOT: goroot, GOCACHE: cache, Stock: true, IRFiles: files}
	for _, p := range []Path{IR, Text} {
		cmd, err := Command(stock, p, module, filepath.Join(root, "bin"), BuildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := envCache(cmd.Env); got != cache {
			t.Errorf("%s build with a Stock compile: GOCACHE %s; want the shared %s", p, got, cache)
		}
	}
	cmd, err := Command(stock, IR, module, filepath.Join(root, "bin"), BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	flag := strings.Join(cmd.Args, " ")
	if !strings.Contains(flag, "-ergoir="+files+string(filepath.Separator)) {
		t.Errorf("the IR build's flags %q do not name a copy in %s", flag, files)
	}
	if _, err := os.Stat(cache + "-ir"); err == nil {
		t.Errorf("a Stock IR build wrote %s-ir beside the shared cache", cache)
	}

	derivative := Toolchain{GOROOT: goroot, GOCACHE: cache}
	cmd, err = Command(derivative, IR, module, filepath.Join(root, "bin"), BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := envCache(cmd.Env); got == cache || !strings.HasPrefix(got, cache+"-") {
		t.Errorf("IR build with a derivative: GOCACHE %s; want one keyed by its content beside %s", got, cache)
	}
}

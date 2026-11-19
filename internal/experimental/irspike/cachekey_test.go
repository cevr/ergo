package irspike

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRCacheKeysOnCompiler: a build by the derivative uses a build cache
// keyed by its compile binary's content (compileID), so a rebuilt
// derivative (a decoder change, another patch) never reuses the objects
// or the stable IR copies an older one made. The derivative prints the
// stock release to -V=full, so the go command's own action IDs cannot tell
// two builds of it apart. Two fake installations that differ only in
// their compile binary must get different caches, and the same binary the
// same cache. It checks the go command Command writes, in process: no
// build. Mutant f7k-cache-unkeyed (the compile ID left out of the cache
// path) fails it; TestIRCacheKeysOnIR, which edits the IR under one
// compiler, does not.
func TestIRCacheKeysOnCompiler(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mod")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lower.IRFileName), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	goroot := func(name, compile string) string {
		g := filepath.Join(root, name)
		tool := filepath.Join(g, "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH)
		if err := os.MkdirAll(tool, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tool, "compile"), []byte(compile), 0o755); err != nil {
			t.Fatal(err)
		}
		return g
	}
	cacheOf := func(goroot string, p Path) string {
		cmd, err := Command(Toolchain{GOROOT: goroot, GOCACHE: filepath.Join(root, "cache", "ir")}, p, dir, filepath.Join(root, "bin"), BuildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, "GOCACHE="); ok {
				return v
			}
		}
		t.Fatal("the go command has no GOCACHE")
		return ""
	}
	older, newer, again := goroot("older", "decoder 1"), goroot("newer", "decoder 2"), goroot("again", "decoder 1")
	for _, p := range []Path{IR, Text} {
		a, b, c := cacheOf(older, p), cacheOf(newer, p), cacheOf(again, p)
		if a == b {
			t.Errorf("%s: two derivatives that differ share the build cache %s: a rebuilt decoder would reuse the older one's objects", p, a)
		}
		if a != c {
			t.Errorf("%s: one compile binary got two caches, %s and %s", p, a, c)
		}
	}
}

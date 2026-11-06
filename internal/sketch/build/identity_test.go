package build

import (
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/producer"
)

// TestDefaultRoot: an absolute XDG_CACHE_HOME holds the cache; a relative
// one is ignored, as the XDG base directory specification requires, and
// the cache falls back to the platform default, which on Linux is
// $HOME/.cache.
func TestDefaultRoot(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", xdg)
	if got, err := DefaultRoot(); err != nil || got != filepath.Join(xdg, "ergo") {
		t.Fatalf("absolute XDG_CACHE_HOME: %s %v", got, err)
	}
	if runtime.GOOS != "linux" {
		t.Skip("the fallback below is Linux's")
	}
	for _, value := range []string{"relative/cache", ""} {
		t.Setenv("XDG_CACHE_HOME", value)
		if got, err := DefaultRoot(); err != nil || got != filepath.Join(home, ".cache", "ergo") {
			t.Fatalf("XDG_CACHE_HOME=%q: %s %v, want %s", value, got, err, filepath.Join(home, ".cache", "ergo"))
		}
	}
}

// contentIdentity reports whether the kernel names the executing image's
// bytes here, so the producer identity is the executable's digest:
// procfs on Linux, the code signature record on darwin/arm64, where the Go
// linker signs every binary. Elsewhere the identity is process-scoped.
func contentIdentity() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

// identityOf runs binary as the identity helper and returns the marker and
// identity it prints.
func identityOf(t *testing.T, binary string) (string, string) {
	t.Helper()
	child := exec.Command(binary, "-test.run=^$")
	child.Env = append(os.Environ(), helperVariable+"=identity")
	out, err := child.Output()
	if err != nil {
		t.Fatalf("identity helper %s: %v", binary, err)
	}
	marker, identity, ok := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !ok {
		t.Fatalf("identity helper printed %q", out)
	}
	return marker, identity
}

// identityDigest is the producer identity rule applied to the file at path,
// written out here as an independent oracle: on darwin, SHA-256 over the
// domain tag and the authenticated code, the bytes below the offset
// LC_CODE_SIGNATURE names; elsewhere, SHA-256 over the whole file.
func identityDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if runtime.GOOS == "darwin" {
		file, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		end := -1
		for _, load := range file.Loads {
			if raw := load.Raw(); len(raw) >= 16 && file.ByteOrder.Uint32(raw) == 0x1d { // LC_CODE_SIGNATURE
				end = int(file.ByteOrder.Uint32(raw[8:]))
			}
		}
		if end < 0 || end > len(data) {
			t.Fatalf("%s has no code signature", path)
		}
		h.Write([]byte("ergo-producer-darwin-code-1\x00"))
		data = data[:end]
	}
	h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// sameBytes reports whether the files at a and b hold the same bytes.
func sameBytes(t *testing.T, a, b string) bool {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(x, y)
}

// linkVariant links this package's test binary into dir with marker as
// identityMarker: a real, signed compiler that differs from the running
// one only in that string.
func linkVariant(t *testing.T, dir, marker string) string {
	t.Helper()
	output := filepath.Join(dir, marker)
	link := exec.Command("go", "test", "-c", "-o", output,
		"-ldflags=-X=ergo.local/prototype/internal/sketch/build.identityMarker="+marker, ".")
	if out, err := link.CombinedOutput(); err != nil {
		t.Fatalf("link the %s variant: %v\n%s", marker, err, out)
	}
	return output
}

// copyFile copies src to dst as an executable.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestCompilerIdentityMapping: the emission key's compiler field is the
// producer's digest for a content identity of the executing artifact and
// its qualifier for a process-scoped identity; anything else stops the
// build with the producer's reason.
func TestCompilerIdentityMapping(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	process := "process:" + strings.Repeat("b", 64)
	cases := []struct {
		name string
		id   producer.Identity
		want string
		err  string
	}{
		{"artifact", producer.Identity{Strength: "executing-artifact", ReuseScope: "artifact", Digest: digest, Qualifier: digest}, digest, ""},
		{"process", producer.Identity{Strength: "unavailable", ReuseScope: "process", Qualifier: process, Reason: "executing image does not match the kernel's code signature record"}, process, ""},
		{"unavailable", producer.Identity{Strength: "unavailable", ReuseScope: "none", Reason: "executing image cannot be opened"}, "", "executing image cannot be opened"},
		{"artifact without its digest as qualifier", producer.Identity{ReuseScope: "artifact", Digest: digest, Qualifier: process}, "", "no usable identity"},
		{"process without its prefix", producer.Identity{ReuseScope: "process", Qualifier: digest}, "", "no usable identity"},
		{"empty process qualifier", producer.Identity{ReuseScope: "process", Qualifier: "process:"}, "", "no usable identity"},
	}
	for _, c := range cases {
		got, err := compilerIdentity(c.id)
		if got != c.want || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %q, %v; want %q and an error naming %q", c.name, got, err, c.want, c.err)
		}
		if got != "" && processScoped(got) != (c.id.ReuseScope == "process") {
			t.Errorf("%s: processScoped(%q) = %v", c.name, got, processScoped(got))
		}
	}
}

// TestCompilerIdentity: where the kernel names the executing image, the
// identity is the digest of the executable's bytes. Two processes of one
// binary share it, so an unchanged build reuses a module across processes.
// Two binaries the linker builds and signs from the same package, differing
// only in the value -X gives one string, have different identities, each
// its own digest; this runs on darwin/arm64 too, where an unsigned page is
// killed. On Linux a copy patched in exactly one byte also runs and
// differs. Elsewhere the identity is process-scoped: never shared.
func TestCompilerIdentity(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := identityDigest(t, binary)
	_, first := identityOf(t, binary)
	_, second := identityOf(t, binary)
	inProcess, err := CompilerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !contentIdentity() {
		if !processScoped(first) || !processScoped(inProcess) || first == second {
			t.Fatalf("identities %s, %s and %s, want a distinct process-scoped identity per process", first, second, inProcess)
		}
		return
	}
	if first != want || second != want || inProcess != want {
		t.Fatalf("identities %s, %s and %s, want the executable digest %s", first, second, inProcess, want)
	}

	dir := t.TempDir()
	built := map[string][]byte{}
	identities := map[string]string{}
	for _, marker := range []string{"ergo-identity-marker-0", "ergo-identity-marker-1"} {
		output := linkVariant(t, dir, marker)
		printed, identity := identityOf(t, output)
		if printed != marker || identity != identityDigest(t, output) {
			t.Fatalf("variant %s printed marker %s and identity %s, want its digest %s", marker, printed, identity, identityDigest(t, output))
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		built[marker], identities[marker] = data, identity
	}
	if identities["ergo-identity-marker-0"] == identities["ergo-identity-marker-1"] {
		t.Fatalf("two linked variants share identity %s", identities["ergo-identity-marker-0"])
	}
	differing := 0
	a, b := built["ergo-identity-marker-0"], built["ergo-identity-marker-1"]
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			differing++
		}
	}
	t.Logf("linked variants: %d and %d bytes, %d differing bytes in the common prefix", len(a), len(b), differing)

	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		at := bytes.Index(data, []byte(identityMarker))
		if at < 0 {
			t.Fatal("the marker is not in the binary")
		}
		changed := bytes.Clone(data)
		changed[at+len(identityMarker)-1] = '1'
		patched := filepath.Join(dir, "patched")
		if err := os.WriteFile(patched, changed, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, other := identityOf(t, patched); other == want || other != identityDigest(t, patched) {
			t.Fatalf("a one-byte change gave identity %s (original %s)", other, want)
		}
	}
}

// replaced is what the replace helper prints.
type replaced struct {
	Identity string `json:"identity"`
	Dir      string `json:"dir"`
	Private  bool   `json:"private"`
}

// TestNoPublicationUnderAReplacementsKey: a compiler process whose
// executable file is replaced while it runs (another real compiler renamed
// over its path, as go install or a package manager does) never publishes
// a module under the replacement's key. The helper builds after the
// replacement; in mode "replace" it first acquires its identity after the
// replacement too, in mode "replace-early" before it. On Linux the identity
// is still the running image's digest, and the module is published under
// it. On darwin/arm64 an identity acquired after the replacement fails the
// kernel's cdhash check, so it is process-scoped and the module is private;
// one acquired before is the running image's digest. Without a kernel
// record (darwin/amd64 and the rest) the identity is always process-scoped.
//
// Two replacements run: another compiler (a -X variant, whose signed code
// differs), and the running compiler's own bytes plus one byte after its
// code signature, whose signed pages and cdhash equal the running image's.
// On darwin the identity is the authenticated code, so the second has the
// running compiler's identity: it runs the same code, and the module is
// published under that identity in both modes. On Linux its whole-file
// digest differs, and the running image's digest keys the module.
func TestNoPublicationUnderAReplacementsKey(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	running := identityDigest(t, binary)
	dir := t.TempDir()
	variant := linkVariant(t, dir, "ergo-identity-marker-1")
	trailing := filepath.Join(dir, "trailing-compiler")
	copyFile(t, binary, trailing)
	if f, err := os.OpenFile(trailing, os.O_APPEND|os.O_WRONLY, 0); err != nil {
		t.Fatal(err)
	} else if _, err := f.Write([]byte{0}); err != nil || f.Close() != nil {
		t.Fatal("append a byte to the trailing replacement")
	}
	for _, replacing := range []struct {
		name, path string
		sameCode   bool
	}{{"variant", variant, false}, {"trailing", trailing, true}} {
		replacement := replacing.path
		replacementDigest := identityDigest(t, replacement)
		// Only on darwin does a same-code replacement share the identity.
		sharesIdentity := replacing.sameCode && runtime.GOOS == "darwin"
		if (replacementDigest == running) != sharesIdentity {
			t.Fatalf("the replacement's digest %s against the running %s: shared %v, want %v", replacementDigest, running, replacementDigest == running, sharesIdentity)
		}
		for _, mode := range []string{"replace", "replace-early"} {
			t.Run(replacing.name+"/"+mode, func(t *testing.T) {
				work := filepath.Join(dir, replacing.name, mode)
				installed := filepath.Join(work, "compiler")
				staged := filepath.Join(work, "staged")
				root := filepath.Join(work, "cache")
				copyFile(t, binary, installed)
				copyFile(t, replacement, staged)
				child := exec.Command(installed, "-test.run=^$")
				child.Env = append(os.Environ(), helperVariable+"="+mode, rootVariable+"="+root, stagedVariable+"="+staged)
				out, err := child.Output()
				if err != nil {
					t.Fatalf("replace helper: %v", err)
				}
				var got replaced
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("replace helper printed %q: %v", out, err)
				}
				if !sameBytes(t, installed, replacement) {
					t.Fatal("the compiler's path does not name the replacement")
				}
				if !sharesIdentity {
					if got.Identity == replacementDigest {
						t.Fatalf("the running compiler took the replacement's identity %s", got.Identity)
					}
					for _, compiler := range publishedCompilers(t, root) {
						if compiler == replacementDigest {
							t.Fatalf("a module is published under the replacement's identity %s", compiler)
						}
					}
				}
				kernelNamesImage := runtime.GOOS == "linux" || contentIdentity() && (mode == "replace-early" || replacing.sameCode)
				switch {
				case kernelNamesImage:
					if got.Identity != running || got.Private || !strings.HasPrefix(got.Dir, filepath.Join(root, "modules")+string(filepath.Separator)) {
						t.Fatalf("got %+v; want the running digest %s, published", got, running)
					}
				default:
					if !processScoped(got.Identity) || !got.Private || len(publishedCompilers(t, root)) != 0 {
						t.Fatalf("got %+v, published %v; want a process-scoped identity and a private module", got, publishedCompilers(t, root))
					}
				}
				t.Logf("%s/%s on %s/%s: identity %s, private %v", replacing.name, mode, runtime.GOOS, runtime.GOARCH, got.Identity, got.Private)
			})
		}
	}
}

// publishedCompilers is the compiler identity of each published module
// under root.
func publishedCompilers(t *testing.T, root string) []string {
	t.Helper()
	var compilers []string
	for _, name := range entries(t, root, "modules") {
		data, err := os.ReadFile(filepath.Join(root, "modules", name, ManifestName))
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		compilers = append(compilers, m.Input.Compiler)
	}
	return compilers
}

// TestPrivateModule: a process-scoped compiler identity builds its module in
// the process's private root and never publishes it. The process reuses
// its private module; another process identity, and a content identity,
// never see it. A private build removes the private roots unused for
// privateLifetime and keeps younger ones.
func TestPrivateModule(t *testing.T) {
	root := cacheRoot(t)
	c := Cache{Root: root}
	var calls atomic.Int32
	in := baseInput()
	in.Compiler = "process:" + strings.Repeat("1", 64)

	stale := filepath.Join(root, "private", "stale")
	young := filepath.Join(root, "private", "young")
	for path, age := range map[string]time.Duration{stale: privateLifetime + time.Hour, young: time.Hour} {
		if err := os.MkdirAll(filepath.Join(path, "modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		then := time.Now().Add(-age)
		if err := os.Chtimes(path, then, then); err != nil {
			t.Fatal(err)
		}
	}

	m, err := released(t)(c.Module(in, emitFor(in, &calls)))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Private || m.Reused || !strings.HasPrefix(m.Dir, filepath.Join(root, "private")+string(filepath.Separator)) {
		t.Fatalf("module %+v; want a fresh private module under %s/private", m, root)
	}
	if names := entries(t, root, "modules"); len(names) != 0 {
		t.Fatalf("a process-scoped module was published: %v", names)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale private root is still there: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("a young private root was removed: %v", err)
	}
	summary, _ := in.Summarize()
	if _, err := verify(m.Dir, m.Key, summary); err != nil || m.Key != summary.Key() {
		t.Fatalf("the private module does not verify: %v", err)
	}
	program, err := link(t, hostToolchain(), m)
	if err != nil {
		t.Fatal(err)
	}
	if got := runProgram(t, program); got != expectedOutput(t, in, "r1", "unset") {
		t.Fatalf("the private module's program printed %q", got)
	}

	again, err := released(t)(c.Module(in, emitFor(in, &calls)))
	if err != nil || !again.Reused || !again.Private || again.Dir != m.Dir || calls.Load() != 1 {
		t.Fatalf("the same process: %+v %v after %d emissions; want its private module reused", again, err, calls.Load())
	}
	other := in
	other.Compiler = "process:" + strings.Repeat("2", 64)
	elsewhere, err := released(t)(c.Module(other, emitFor(other, &calls)))
	if err != nil || elsewhere.Reused || !elsewhere.Private || filepath.Dir(filepath.Dir(elsewhere.Dir)) == filepath.Dir(filepath.Dir(m.Dir)) || calls.Load() != 2 {
		t.Fatalf("another process: %+v %v; want its own private module", elsewhere, err)
	}
	content := in
	content.Compiler = "sha256:compiler-1"
	shared, err := released(t)(c.Module(content, emitFor(content, &calls)))
	if err != nil || shared.Reused || shared.Private || shared.Dir != filepath.Join(root, "modules", shared.Key) || calls.Load() != 3 {
		t.Fatalf("a content identity: %+v %v; want a published module of its own", shared, err)
	}
	if err := filepath.WalkDir(filepath.Join(root, "modules"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == ManifestName {
			data, _ := os.ReadFile(p)
			if bytes.Contains(data, []byte("process:")) {
				t.Errorf("%s names a process-scoped identity", p)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// cacheRoot is a cache root for one test, checked at the test's end: every
// module the test took from it was released. Then no descriptor of this
// process stays open under root (an entry's shared lock or a private root's
// lease file), and no private root under it keeps this process's lease.
// Garbage collection is off until the check, so a module that a test drops
// without Release keeps its descriptor open (an os.File finalizer would
// close it during a collection) and the check sees it every time.
func cacheRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gc := debug.SetGCPercent(-1)
	t.Cleanup(func() {
		defer debug.SetGCPercent(gc)
		if _, err := os.Stat("/proc/self/fd"); err == nil {
			if open := openUnder(t, root); len(open) != 0 {
				t.Errorf("the test left modules unreleased: descriptors open under the cache root: %v", open)
			}
		}
		leases.Lock()
		defer leases.Unlock()
		for held := range leases.held {
			if held == root || strings.HasPrefix(held, root+string(filepath.Separator)) {
				t.Errorf("the test left modules unreleased: the private root %s keeps its lease", held)
			}
		}
	})
	return root
}

// released returns the module and error of a Module call as they are, and
// releases the module when the test ends, on every path: a failed test
// releases it too. The test may Release it earlier; Release is idempotent.
func released(t *testing.T) func(Module, error) (Module, error) {
	return func(m Module, err error) (Module, error) {
		t.Cleanup(m.Release)
		return m, err
	}
}

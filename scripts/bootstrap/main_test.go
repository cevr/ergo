package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// needsBuild marks a test that starts a go command: the fast tier (-short)
// skips it, and the full tier runs it, as lower's needsBuild does.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// TestCapstoneRowsOfLandedStages runs the capstone probe over this
// checkout: every row is reported, and a red row fails the test only when
// its stage is at or before landedStage.
func TestCapstoneRowsOfLandedStages(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	red, owed := reportCapstone(&report, capstoneRows(root))
	t.Logf("capstone probe (%d red):\n%s", red, report.String())
	for _, r := range owed {
		t.Errorf("stage S%d has landed, but its row is red: %s", r.stage, r.name)
	}
}

// TestBuildIsAFunctionOfSourceAndGo is a seeded property: two builds of the
// same source with the same Go are byte-identical, whatever else differs
// between them (the checkout's path, its files' times, its git state, a
// go.work above it, the user's go env file, GOFLAGS, GOAMD64, CGO_ENABLED,
// TZ, a C compiler on PATH). A change of the source changes the binary.
// ERGO_SEED=<n> replays one seed.
//
// It builds Go because the property is about the bytes go build writes: no
// in-process check (go/types, the lowered text) links a binary.
func TestBuildIsAFunctionOfSourceAndGo(t *testing.T) {
	needsBuild(t)
	base := baseEnviron(t)
	for _, seed := range seeds(t, 4) {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 0x5eed))
			literal := fmt.Sprintf("probe-%d", rng.Uint32())
			a := newVariant(t, rng, base, literal)
			b := newVariant(t, rng, base, literal)
			sa, sb := a.build(t), b.build(t)
			replay := fmt.Sprintf("ERGO_SEED=%d go test -run 'TestBuildIsAFunctionOfSourceAndGo' ./scripts/bootstrap", seed)
			if sa != sb {
				t.Fatalf("one source and one Go, two binaries:\n  %s\n    %s\n  %s\n    %s\nreplay: %s", sa, a, sb, b, replay)
			}
			a.write(t, literal+"-changed")
			if sc := a.build(t); sc == sa {
				t.Fatalf("a changed source built the same binary %s:\n  %s\nreplay: %s", sa, a, replay)
			}
		})
	}
}

// TestSeedIsReproducedNotTrusted: a seed is taken from the cache only with
// the lock's digest, rebuilt from its source commit otherwise, and refused
// when the rebuilt digest, the Go release or the tag of its name disagrees
// with the lock.
func TestSeedIsReproducedNotTrusted(t *testing.T) {
	needsBuild(t)
	environ := baseEnviron(t)
	repo := filepath.Join(t.TempDir(), "repo")
	writeModule(t, filepath.Join(repo, "cmd", "ergo"), "first")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.local/seedrepo\n\ngo 1.27\n")
	gitRun(t, repo, "init", "-q")
	first := commitAll(t, repo, "first")
	writeModule(t, filepath.Join(repo, "cmd", "ergo"), "second")
	second := commitAll(t, repo, "second")

	host := hostPlatform()
	tool := goTool{cmd: "go", toolchain: "local"}
	d := driver{repo: repo, environ: environ, tool: tool, cacheRoot: t.TempDir()}
	l := seedLock{Seed: "seed-t", Source: first, Go: localGoVersion(t, environ), Package: "./cmd/ergo", Platforms: []string{host}}
	sums, err := d.reproduce(l, l.Platforms, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	l.Sums = sums
	if err := d.verifySeed(l, t.TempDir(), false); err != nil {
		t.Fatalf("the lock that seed-lock printed does not verify: %v", err)
	}

	path, err := d.ensureSeed(l, host)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := fileSum(path); got != sums[host] {
		t.Fatalf("the seed in the cache has sha256 %s, the lock %s", got, sums[host])
	}
	writeFile(t, path, "a binary that is not the seed")
	if path, err = d.ensureSeed(l, host); err != nil {
		t.Fatal(err)
	}
	if got, _ := fileSum(path); got != sums[host] {
		t.Errorf("a cached seed whose digest is not the lock's was used: sha256 %s, lock %s", got, sums[host])
	}

	wrong := l
	wrong.Sums = map[string]string{host: strings.Repeat("0", 64)}
	if _, err := d.ensureSeed(wrong, host); err == nil {
		t.Error("a seed whose rebuilt digest is not the lock's was accepted")
	}
	if err := d.verifySeed(wrong, t.TempDir(), false); err == nil {
		t.Error("verify-seed accepted a lock whose digest the source does not build")
	}

	otherGo := l
	otherGo.Go = "go1.99.0"
	clearCache(t, d, l, host)
	if _, err := d.ensureSeed(otherGo, host); err == nil {
		t.Error("a lock that names another Go release than the one that built the seed was accepted")
	}

	gitRun(t, repo, "tag", l.Seed, second)
	clearCache(t, d, l, host)
	if _, err := d.ensureSeed(l, host); err == nil || !strings.Contains(err.Error(), "tag") {
		t.Errorf("a tag %s that names another commit than the lock's source was not refused: %v", l.Seed, err)
	}
}

// TestStagesRefuseErgoCompilerPackages: the stages build the tree after the
// seed, and a tree with Ergo compiler packages is refused until the
// mixed-binary step exists, never built without them.
func TestStagesRefuseErgoCompilerPackages(t *testing.T) {
	needsBuild(t)
	environ := baseEnviron(t)
	repo := filepath.Join(t.TempDir(), "repo")
	writeModule(t, filepath.Join(repo, "cmd", "ergo"), "tree")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.local/stagerepo\n\ngo 1.27\n")
	gitRun(t, repo, "init", "-q")
	source := commitAll(t, repo, "tree")
	host := hostPlatform()
	d := driver{repo: repo, environ: environ, tool: goTool{cmd: "go", toolchain: "local"}, cacheRoot: t.TempDir()}
	l := seedLock{Seed: "seed-t", Source: source, Go: localGoVersion(t, environ), Package: "./cmd/ergo", Platforms: []string{host}}
	sums, err := d.reproduce(l, l.Platforms, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	l.Sums = sums
	if _, err := d.stages(l, t.TempDir(), 2); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "compiler", "ergo.mod"), "module compiler\n")
	if _, err := d.stages(l, t.TempDir(), 2); err == nil || !strings.Contains(err.Error(), "S1") {
		t.Errorf("a tree with compiler/ was built without lowering it: %v", err)
	}
}

// variant is one checkout of the probe module and one caller environment.
type variant struct {
	dir     string
	environ []string
	notes   []string // what this variant changed, for the failure message
}

func (v variant) String() string { return v.dir + ": " + strings.Join(v.notes, ", ") }

func (v variant) write(t *testing.T, literal string) {
	writeFile(t, filepath.Join(v.dir, "go.mod"), "module example.local/seedprobe\n\ngo 1.27\n")
	writeModule(t, v.dir, literal)
}

func (v variant) build(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "probe")
	b := goBuild{environ: v.environ, tool: goTool{cmd: "go", toolchain: "local"}, dir: v.dir, pkg: ".", platform: hostPlatform(), out: out}
	if err := b.run(); err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	sum, err := fileSum(out)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// newVariant writes the probe module at a new path and draws the inputs that
// must not reach the binary.
func newVariant(t *testing.T, rng *rand.Rand, base []string, literal string) variant {
	t.Helper()
	parent := t.TempDir()
	dir := parent
	for range 1 + rng.IntN(3) {
		dir = filepath.Join(dir, fmt.Sprintf("d%x", rng.Uint32()))
	}
	v := variant{dir: dir, environ: append([]string(nil), base...)}
	v.write(t, literal)
	if rng.IntN(2) == 0 {
		at := time.Unix(int64(rng.IntN(1<<30)), 0)
		for _, f := range []string{"go.mod", "main.go"} {
			if err := os.Chtimes(filepath.Join(dir, f), at, at); err != nil {
				t.Fatal(err)
			}
		}
		v.notes = append(v.notes, "file times "+at.UTC().Format("2006-01-02"))
	}
	switch rng.IntN(3) {
	case 1:
		gitRun(t, dir, "init", "-q")
		commitAll(t, dir, "probe")
		v.notes = append(v.notes, "git, clean")
	case 2:
		gitRun(t, dir, "init", "-q")
		commitAll(t, dir, "probe")
		writeFile(t, filepath.Join(dir, "notes.txt"), "dirty\n")
		v.notes = append(v.notes, "git, dirty")
	}
	if rng.IntN(2) == 0 {
		writeFile(t, filepath.Join(filepath.Dir(dir), "go.work"), "go 1.27\n\nuse ./"+filepath.Base(dir)+"\n")
		v.notes = append(v.notes, "go.work above")
	}
	extra := []struct{ kv, note string }{
		{"GOFLAGS=-ldflags=-X=main.leak=flags", "GOFLAGS"},
		{"GOAMD64=v3", "GOAMD64=v3"},
		{"GOARM64=v8.1", "GOARM64=v8.1"},
		{"CGO_ENABLED=1", "CGO_ENABLED=1"},
		{"TZ=Pacific/Chatham", "TZ"},
		{"SOURCE_DATE_EPOCH=86400", "SOURCE_DATE_EPOCH"},
	}
	for _, e := range extra {
		if rng.IntN(2) == 0 {
			v.environ = append(v.environ, e.kv)
			v.notes = append(v.notes, e.note)
		}
	}
	if rng.IntN(2) == 0 {
		home := filepath.Join(parent, "home")
		writeFile(t, filepath.Join(home, ".config", "go", "env"), "GOFLAGS=-ldflags=-X=main.leak=goenv\n")
		v.environ = append(without(v.environ, "XDG_CONFIG_HOME"), "HOME="+home)
		v.notes = append(v.notes, "a go env file in HOME")
	}
	if rng.IntN(2) == 0 {
		bin := filepath.Join(parent, "bin")
		for _, prog := range []string{"go", "git"} {
			p, err := exec.LookPath(prog)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p, filepath.Join(bin, prog)); err != nil {
				t.Fatal(err)
			}
		}
		v.environ = append(v.environ, "PATH="+bin)
		v.notes = append(v.notes, "no C compiler on PATH")
	}
	if len(v.notes) == 0 {
		v.notes = []string{"nothing else"}
	}
	return v
}

func writeModule(t *testing.T, dir, literal string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport \"fmt\"\n\nvar leak string\n\nfunc main() { fmt.Println("+strconv.Quote(literal)+", leak) }\n")
}

// baseEnviron is the test's environment with GOCACHE stated, so a variant
// that moves HOME still builds from the shared build cache.
func baseEnviron(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(), "GOCACHE="+strings.TrimSpace(string(out)))
}

func localGoVersion(t *testing.T, environ []string) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Env = append(environ, "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func clearCache(t *testing.T, d driver, l seedLock, platform string) {
	t.Helper()
	if err := os.Remove(d.seedPath(l, platform)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func without(environ []string, key string) []string {
	var out []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k != key {
			out = append(out, kv)
		}
	}
	return out
}

func seeds(t *testing.T, n int) []uint64 {
	t.Helper()
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED=%q: %v", s, err)
		}
		return []uint64{v}
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(i + 1)
	}
	return out
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "-c", "user.name=probe", "-c", "user.email=probe@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", msg)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

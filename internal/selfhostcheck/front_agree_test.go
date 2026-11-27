package selfhostcheck

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	linkedcompiler "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

type frontHostFS struct{}

func (frontHostFS) ReadDir(dir string) ([]fs.DirEntry, error) { return os.ReadDir(dir) }
func (frontHostFS) ReadFile(name string) ([]byte, error)      { return os.ReadFile(name) }
func (frontHostFS) Stat(name string) (fs.FileInfo, error)     { return os.Stat(name) }
func (frontHostFS) Lstat(name string) (fs.FileInfo, error)    { return os.Lstat(name) }

type fdDiscoverPkg struct {
	Path   string   `json:"path"`
	Dir    string   `json:"dir"`
	Rel    string   `json:"rel"`
	Module string   `json:"module"`
	Root   string   `json:"root"`
	Deps   []string `json:"deps"`
}

type fdDiscoverOut struct {
	StdRoot     string          `json:"stdRoot"`
	IsWorkspace bool            `json:"isWorkspace"`
	Packages    []fdDiscoverPkg `json:"packages"`
	Diags       []string        `json:"diags"`
}

type fdModRule struct {
	Pattern    string   `json:"pattern"`
	MapperPkg  string   `json:"mapperPkg"`
	MapperFunc string   `json:"mapperFunc"`
	Companions []string `json:"companions"`
}

type fdModOut struct {
	Root        string      `json:"root"`
	Name        string      `json:"name"`
	ErgoVersion string      `json:"ergoVersion"`
	TrustGo     []string    `json:"trustGo"`
	Rules       []fdModRule `json:"rules"`
	Diags       []string    `json:"diags"`
}

type fdWsMember struct {
	Rel       string `json:"rel"`
	Dir       string `json:"dir"`
	Name      string `json:"name"`
	GoModPath string `json:"goModPath"`
}

type fdWsEdge struct {
	From      uint32 `json:"from"`
	To        uint32 `json:"to"`
	GoModPath string `json:"goModPath"`
}

type fdWsOut struct {
	Root    string       `json:"root"`
	Skip    []string     `json:"skip"`
	Members []fdWsMember `json:"members"`
	Edges   []fdWsEdge   `json:"edges"`
	Order   []uint32     `json:"order"`
	Diags   []string     `json:"diags"`
}

type fdLoadPkg struct {
	Path    string   `json:"path"`
	Dir     string   `json:"dir"`
	Rel     string   `json:"rel"`
	Module  string   `json:"module"`
	IsAsset bool     `json:"isAsset"`
	Files   []string `json:"files"`
	Imports []string `json:"imports"`
}

type fdLoadOut struct {
	Roots      []string    `json:"roots"`
	Order      []fdLoadPkg `json:"order"`
	ModuleSegs []string    `json:"moduleSegs"`
	Diags      []string    `json:"diags"`
}

type fdWarmKeyOut struct {
	Schema            string `json:"schema"`
	Key               string `json:"key"`
	StdSnapshotKey    string `json:"stdSnapshotKey"`
	ModuleSnapshotKey string `json:"moduleSnapshotKey"`
}

func frontDumpBin(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "frontdump")
	if out, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/frontdump"); err != nil {
		t.Fatalf("ergo build ./cmd/frontdump: %v\n%s", err, out)
	}
	return bin
}

func runFrontDumpJSON(t *testing.T, ctx context.Context, bin string, dst any, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frontdump %v failed: %v\n%s", args, err, string(out))
	}
	if err := json.Unmarshal(out, dst); err != nil {
		t.Fatalf("frontdump %v JSON decode failed: %v\nraw: %s", args, err, string(out))
	}
}

func TestCompilerFrontAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()

	root := repoRoot()
	stdDir := filepath.Join(root, "internal", "sketch", "std")
	bin := frontDumpBin(t, ctx)

	t.Run("ModResolution", func(t *testing.T) {
		relDirs := []string{
			"compiler",
			"compiler/front",
			"modules/web",
			"modules/domain",
			"modules/board",
			"modules/sqlite",
			"modules/orchestrate",
			"examples/board",
			"examples/orchestrate",
			"examples/regex",
			"examples/counter",
		}
		for _, rel := range relDirs {
			absDir := filepath.Join(root, filepath.FromSlash(rel))
			goModRoot, goModName, err := sketchfront.FindModule(absDir)
			if err != nil {
				t.Fatalf("sketchfront.FindModule(%s): %v", rel, err)
			}
			var got fdModOut
			runFrontDumpJSON(t, ctx, bin, &got, "-mode", "mod", "-root", absDir)
			if len(got.Diags) != 0 {
				t.Fatalf("frontdump mod(%s) diags: %v", rel, got.Diags)
			}
			if got.Root != filepath.Clean(goModRoot) {
				t.Fatalf("mod(%s) root: ergo=%q go=%q", rel, got.Root, goModRoot)
			}
			if got.Name != goModName {
				t.Fatalf("mod(%s) name: ergo=%q go=%q", rel, got.Name, goModName)
			}
		}
	})

	t.Run("PackageDiscoveryAndWorkspace", func(t *testing.T) {
		discRoots := []string{
			"compiler",
			"modules/web",
			"modules/domain",
			"modules/board",
			"modules/sqlite",
			"examples/board",
			"examples/orchestrate",
			"examples/regex",
			"examples/counter",
			"examples/kvstore",
		}
		for _, rel := range discRoots {
			absDir := filepath.Join(root, filepath.FromSlash(rel))
			goPkgs, err := sketchfront.DiscoverPackages(absDir)
			if err != nil {
				t.Fatalf("sketchfront.DiscoverPackages(%s): %v", rel, err)
			}
			var got fdDiscoverOut
			runFrontDumpJSON(t, ctx, bin, &got, "-mode", "discover", "-std", stdDir, "-root", absDir)
			if len(got.Diags) != 0 {
				t.Fatalf("frontdump discover(%s) diags: %v", rel, got.Diags)
			}
			type pkgKey struct {
				Path string
				Dir  string
				Root string
			}
			var goKeys []pkgKey
			for _, gp := range goPkgs {
				goKeys = append(goKeys, pkgKey{Path: gp.Path, Dir: filepath.Clean(gp.Dir), Root: filepath.Clean(gp.Root)})
			}
			var ergoKeys []pkgKey
			for _, ep := range got.Packages {
				ergoKeys = append(ergoKeys, pkgKey{Path: ep.Path, Dir: filepath.Clean(ep.Dir), Root: filepath.Clean(ep.Root)})
			}
			sortKey := func(a, b pkgKey) int {
				if c := strings.Compare(a.Dir, b.Dir); c != 0 {
					return c
				}
				return strings.Compare(a.Path, b.Path)
			}
			slices.SortFunc(goKeys, sortKey)
			slices.SortFunc(ergoKeys, sortKey)
			if !reflect.DeepEqual(ergoKeys, goKeys) {
				t.Fatalf("DiscoverPackages(%s) mismatch:\nergo=%v\ngo=%v", rel, ergoKeys, goKeys)
			}
		}

		wsRoots := []string{
			filepath.Join(root, "examples"),
			root,
		}
		for _, wsRoot := range wsRoots {
			goWs, err := sketchfront.DiscoverWorkspace(frontHostFS{}, wsRoot)
			if err != nil {
				t.Fatalf("sketchfront.DiscoverWorkspace(%s): %v", wsRoot, err)
			}
			var got fdWsOut
			runFrontDumpJSON(t, ctx, bin, &got, "-mode", "workspace", "-root", wsRoot)
			if !slices.Equal(got.Skip, goWs.Skip) {
				t.Fatalf("Workspace(%s) Skip mismatch: ergo=%v go=%v", wsRoot, got.Skip, goWs.Skip)
			}
			var goMembers, ergoMembers []string
			for _, m := range goWs.Members {
				goMembers = append(goMembers, m.Rel+"|"+m.Dir+"|"+m.Module)
			}
			for _, m := range got.Members {
				ergoMembers = append(ergoMembers, m.Rel+"|"+m.Dir+"|"+m.Name)
			}
			if !slices.Equal(ergoMembers, goMembers) {
				t.Fatalf("Workspace(%s) Members mismatch:\nergo=%v\ngo=%v", wsRoot, ergoMembers, goMembers)
			}
			var goEdges, ergoEdges []string
			for _, e := range goWs.Edges {
				goEdges = append(goEdges, goWs.Members[e.From].Rel+"->"+goWs.Members[e.To].Rel)
			}
			for _, e := range got.Edges {
				ergoEdges = append(ergoEdges, got.Members[e.From].Rel+"->"+got.Members[e.To].Rel)
			}
			slices.Sort(goEdges)
			slices.Sort(ergoEdges)
			if !slices.Equal(ergoEdges, goEdges) {
				t.Fatalf("Workspace(%s) Edges mismatch:\nergo=%v\ngo=%v", wsRoot, ergoEdges, goEdges)
			}
			goOrderIdx := goWs.Order()
			var goOrder, ergoOrder []string
			for _, idx := range goOrderIdx {
				goOrder = append(goOrder, goWs.Members[idx].Rel)
			}
			for _, idx := range got.Order {
				ergoOrder = append(ergoOrder, got.Members[idx].Rel)
			}
			if !slices.Equal(ergoOrder, goOrder) {
				t.Fatalf("Workspace(%s) Order mismatch:\nergo=%v\ngo=%v", wsRoot, ergoOrder, goOrder)
			}
		}
	})

	t.Run("ImportGraphAndTopologicalOrder", func(t *testing.T) {
		targetRels := []string{
			"compiler/front",
			"compiler/format",
			"compiler/syntax/dump",
			"compiler/check/task",
			"modules/web",
			"modules/board",
		}
		for _, subRoot := range []string{"examples/board", "examples/orchestrate", "examples/regex", "examples/counter"} {
			pkgs, err := sketchfront.DiscoverPackages(filepath.Join(root, filepath.FromSlash(subRoot)))
			if err != nil {
				t.Fatalf("DiscoverPackages(%s): %v", subRoot, err)
			}
			for _, p := range pkgs {
				rel, err := filepath.Rel(root, p.Dir)
				if err != nil {
					t.Fatal(err)
				}
				targetRels = append(targetRels, filepath.ToSlash(rel))
			}
		}

		for _, rel := range targetRels {
			absDir := filepath.Join(root, filepath.FromSlash(rel))
			analysis, err := sketchfront.CheckDir(absDir)
			if err != nil {
				t.Fatalf("sketchfront.CheckDir(%s): %v", rel, err)
			}
			goPkgs := analysis.Packages()

			var got fdLoadOut
			runFrontDumpJSON(t, ctx, bin, &got, "-mode", "load", "-std", stdDir, "-work", root, rel)
			if len(got.Diags) != 0 {
				t.Fatalf("frontdump load(%s) diags: %v", rel, got.Diags)
			}

			var goOrderPaths []string
			goImports := map[string][]string{}
			goFiles := map[string][]string{}
			for _, gp := range goPkgs {
				canon := sourceImportPath(gp.Dir, gp.Path)
				goOrderPaths = append(goOrderPaths, canon)
				imps := slices.Clone(gp.Imports)
				goImports[canon] = imps
				var fns []string
				for _, gf := range gp.Files {
					fns = append(fns, filepath.Base(gf.Rel))
				}
				goFiles[canon] = fns
			}

			var ergoOrderPaths []string
			ergoPos := map[string]int{}
			ergoImports := map[string][]string{}
			ergoFiles := map[string][]string{}
			for i, ep := range got.Order {
				ergoOrderPaths = append(ergoOrderPaths, ep.Path)
				ergoPos[ep.Path] = i
				ergoImports[ep.Path] = slices.Clone(ep.Imports)
				ergoFiles[ep.Path] = slices.Clone(ep.Files)
			}

			if !slices.Equal(ergoOrderPaths, goOrderPaths) {
				t.Fatalf("Load(%s) topological order mismatch:\nergo=%v\ngo=%v", rel, ergoOrderPaths, goOrderPaths)
			}
			for _, p := range goOrderPaths {
				if !slices.Equal(ergoFiles[p], goFiles[p]) {
					t.Fatalf("Load(%s) pkg %s files mismatch: ergo=%v go=%v", rel, p, ergoFiles[p], goFiles[p])
				}
				if !slices.Equal(ergoImports[p], goImports[p]) {
					t.Fatalf("Load(%s) pkg %s imports mismatch: ergo=%v go=%v", rel, p, ergoImports[p], goImports[p])
				}
				for _, dep := range ergoImports[p] {
					if dp, ok := ergoPos[dep]; ok && dp >= ergoPos[p] {
						t.Fatalf("Load(%s) dependency %s (%d) not before importer %s (%d)", rel, dep, dp, p, ergoPos[p])
					}
				}
			}
		}
	})

	t.Run("WarmCacheKeyAgreement", func(t *testing.T) {
		envA := []string{
			"ERGO_BUILD=check",
			"ERGO_CACHE=/tmp/cache-one",
			"GOPATH=/tmp/gopath-one",
			"GOCACHE=/tmp/gocache-one",
			"GOTMPDIR=/tmp/gotmp-one",
			"GOFLAGS=-trimpath",
			"UNRELATED=alpha",
		}
		envB := []string{
			"ERGO_BUILD=check",
			"ERGO_CACHE=/tmp/cache-two",
			"GOPATH=/tmp/gopath-two",
			"GOCACHE=/tmp/gocache-two",
			"GOTMPDIR=/tmp/gotmp-two",
			"GOFLAGS=-trimpath",
			"UNRELATED=beta",
		}
		exps := []string{"arenas", "boringcrypto"}
		parts := []string{"check", "compiler/front", "linux/amd64"}

		var gotA, gotB fdWarmKeyOut
		runFrontDumpJSON(t, ctx, bin, &gotA,
			"-mode", "warmkey",
			"-compiler", "ergo-test-v1",
			"-env", strings.Join(envA, ","),
			"-exp", strings.Join(exps, ","),
			"--", parts[0], parts[1], parts[2],
		)
		runFrontDumpJSON(t, ctx, bin, &gotB,
			"-mode", "warmkey",
			"-compiler", "ergo-test-v1",
			"-env", strings.Join(envB, ","),
			"-exp", strings.Join(exps, ","),
			"--", parts[0], parts[1], parts[2],
		)

		wantA, err := linkedcompiler.KeyWarm(ctx, "ergo-test-v1", parts, envA, exps)
		if err != nil {
			t.Fatalf("linkedcompiler.KeyWarm: %v", err)
		}
		if gotA.Key == "" || gotA.Key != wantA {
			t.Fatalf("WarmKey mismatch: ergo=%q linked=%q", gotA.Key, wantA)
		}
		if gotA.Key != gotB.Key {
			t.Fatalf("WarmKey location/unrelated env leak: A=%q B=%q", gotA.Key, gotB.Key)
		}

		// Also verify direct process identity agreement with sketchfront.WarmKey when empty.
		goTestKey := sketchfront.WarmKey("check", "compiler/front")
		if goTestKey == "" {
			var gotEmpty fdWarmKeyOut
			runFrontDumpJSON(t, ctx, bin, &gotEmpty, "-mode", "warmkey", "-compiler", "", "--", "check", "compiler/front")
			if gotEmpty.Key != "" {
				t.Fatalf("WarmKey with empty compiler identity: ergo=%q want empty", gotEmpty.Key)
			}
		}
	})
}

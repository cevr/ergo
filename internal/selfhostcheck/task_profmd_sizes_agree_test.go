package selfhostcheck

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/profmd"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sizes"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/task"
	"ergo.local/prototype/internal/testwait"
)

func buildTaskProfmdDump(t *testing.T, ctx context.Context, outDir string) string {
	t.Helper()
	ergo := ergoCLI(t, ctx)
	bin := filepath.Join(outDir, "taskprofmddump")
	cmd := exec.CommandContext(ctx, ergo, "build", "-o", bin, "./cmd/taskprofmddump")
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ergo build ./cmd/taskprofmddump failed: %v\n%s", err, out)
	}
	return bin
}

func runTaskProfmdDump(t *testing.T, ctx context.Context, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("taskprofmddump %v failed: %v\n%s", args, err, out)
	}
	return out
}

func TestCompilerTaskProfmdAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()

	work := t.TempDir()
	bin := buildTaskProfmdDump(t, ctx, work)
	root := repoRoot()

	t.Run("ErgoBuild", func(t *testing.T) {
		type buildIn struct {
			Version string   `json:"version"`
			Out     string   `json:"out"`
			Pkg     string   `json:"pkg"`
			Flags   []string `json:"flags"`
			Env     []string `json:"env"`
		}
		type buildOut struct {
			Install string   `json:"install"`
			Flags   []string `json:"flags"`
			Program string   `json:"program"`
			Out     string   `json:"out"`
			Pkg     string   `json:"pkg"`
			Args    []string `json:"args"`
			Env     []string `json:"env"`
		}
		cases := []buildIn{
			{
				Version: "latest",
				Out:     "/tmp/out-ergo",
				Pkg:     "./cmd/ergo",
				Flags:   []string{},
				Env:     []string{"PATH=/usr/bin", "HOME=/home/user"},
			},
			{
				Version: "v0.9.1",
				Out:     "/build/ergo-bin",
				Pkg:     "./cmd/ergo",
				Flags:   []string{"-v", "-race"},
				Env:     []string{"CGO_ENABLED=1", "PATH=/bin", "CGO_ENABLED=0", "FOO=bar"},
			},
		}
		for i, tc := range cases {
			inPath := filepath.Join(work, "ergobuild-"+strconv.Itoa(i)+".json")
			raw, err := json.Marshal(tc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			gotBytes := runTaskProfmdDump(t, ctx, bin, "ergobuild", inPath)
			var got buildOut
			if err := json.Unmarshal(gotBytes, &got); err != nil {
				t.Fatalf("unmarshal ergobuild output: %v\n%s", err, gotBytes)
			}
			goCmd := ergobuild.Command(tc.Out, tc.Pkg, tc.Flags...)
			goEnv := ergobuild.Env(tc.Env)
			if !slices.Equal(got.Flags, ergobuild.Flags) {
				t.Fatalf("Flags mismatch: ergo=%v go=%v", got.Flags, ergobuild.Flags)
			}
			if got.Program != "go" || !slices.Equal(got.Args, goCmd.Args[1:]) {
				t.Fatalf("Args mismatch: ergo=%v go=%v", got.Args, goCmd.Args[1:])
			}
			if !slices.Equal(got.Env, goEnv) {
				t.Fatalf("Env mismatch: ergo=%v go=%v", got.Env, goEnv)
			}
			wantInstall := ergobuild.Cgo + " go install github.com/cevr/ergo/cmd/ergo@" + tc.Version
			if got.Install != wantInstall {
				t.Fatalf("Install mismatch: ergo=%q go=%q", got.Install, wantInstall)
			}
		}
	})

	t.Run("TestWait", func(t *testing.T) {
		type waitOut struct {
			HasEnd           bool  `json:"hasEnd"`
			ReserveNs        int   `json:"reserveNs"`
			LeftNs           int   `json:"leftNs"`
			ExpiredAtEnd     bool  `json:"expiredAtEnd"`
			ExpiredBeforeEnd bool  `json:"expiredBeforeEnd"`
			PollsNs          []int `json:"pollsNs"`
		}
		var noDL waitOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "testwait", "1700000000", "-1", "10"), &noDL); err != nil {
			t.Fatal(err)
		}
		if noDL.HasEnd || noDL.LeftNs != 0 || noDL.ExpiredAtEnd || noDL.ExpiredBeforeEnd {
			t.Fatalf("unexpected no-deadline testwait output: %+v", noDL)
		}
		wantPolls := []int{10_000, 20_000, 40_000, 80_000, 160_000, 320_000, 640_000, 1_000_000, 1_000_000, 1_000_000}
		if !slices.Equal(noDL.PollsNs, wantPolls) {
			t.Fatalf("poll backoff mismatch: got=%v want=%v", noDL.PollsNs, wantPolls)
		}

		var shortDL waitOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "testwait", "1700000000", "5000", "3"), &shortDL); err != nil {
			t.Fatal(err)
		}
		if !shortDL.HasEnd || shortDL.ReserveNs != 500_000_000 || shortDL.LeftNs != 4_500_000_000 || !shortDL.ExpiredAtEnd || shortDL.ExpiredBeforeEnd {
			t.Fatalf("unexpected 5s deadline testwait output: %+v", shortDL)
		}

		// Compare against Go testwait.End / Left using a 20m deadline so min(remaining/10, 1m) is invariant at 60s.
		var longDL waitOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "testwait", "1700000000", "1200000", "2"), &longDL); err != nil {
			t.Fatal(err)
		}
		dl := time.Now().Add(20 * time.Minute)
		ft := fakeWaitT{has: true, deadline: dl}
		goEnd, goOK := testwait.End(ft)
		goLeft := testwait.Left(ft)
		if !goOK || !longDL.HasEnd {
			t.Fatalf("expected deadline on both Go (%v) and Ergo (%v)", goOK, longDL.HasEnd)
		}
		if longDL.ReserveNs != int(time.Minute) || dl.Sub(goEnd) != time.Minute {
			t.Fatalf("reserve mismatch: ergo=%d go=%v", longDL.ReserveNs, dl.Sub(goEnd))
		}
		if goLeft <= 18*time.Minute || longDL.LeftNs != int(19*time.Minute) {
			t.Fatalf("left mismatch: ergo=%d go=%v", longDL.LeftNs, goLeft)
		}
	})

	t.Run("Sizes", func(t *testing.T) {
		type sizePkgOut struct {
			Pkg    string `json:"pkg"`
			Linked bool   `json:"linked"`
		}
		symbols := []string{
			"main.main",
			"time.Time.String",
			"runtime.mallocgc",
			"ergo.local/prototype/runtime/v1.RegisterMachine[go.shape.struct {}]",
			"type:.eq.runtime.g",
			"go:itab.*os.File,io.Writer",
			"__x86_return_thunk",
			"vendor/golang.org/x/net/dns/dnsmessage.(*Builder).Finish",
		}
		for _, sym := range symbols {
			var got sizePkgOut
			if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "sizes-pkg", sym), &got); err != nil {
				t.Fatalf("sizes-pkg %q: %v", sym, err)
			}
			want := sizes.Package(sym)
			if got.Pkg != want {
				t.Fatalf("sizes.Package(%q): ergo=%q go=%q", sym, got.Pkg, want)
			}
		}

		// Real ELF pair: build internal/sizes/testdata/gohello unstripped + stripped cleanly.
		helloDir := filepath.Join(root, "internal", "sizes", "testdata", "gohello")
		defBin := filepath.Join(work, "gohello")
		stripBin := filepath.Join(work, "gohello.stripped")
		for idx, extra := range [][]string{nil, {"-ldflags=-s"}} {
			dst := defBin
			if idx == 1 {
				dst = stripBin
			}
			c := receipt.GoBuildCommand(helloDir, dst, ".", extra...)
			c.Env = append(c.Env, "GOFLAGS=")
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("go build gohello: %v\n%s", err, out)
			}
		}
		goBin, err := sizes.MeasureBinary(defBin, stripBin)
		if err != nil {
			t.Fatalf("sizes.MeasureBinary: %v", err)
		}
		goBin.GoPackages = 42

		type sizeElfOut struct {
			Size        uint64         `json:"size"`
			Stripped    uint64         `json:"stripped"`
			MainSymbols int            `json:"mainSymbols"`
			GoPackages  int            `json:"goPackages"`
			SymbolCount int            `json:"symbolCount"`
			Sections    []nameSizePair `json:"sections"`
			Text        []nameSizePair `json:"text"`
			SymbolsHead []string       `json:"symbolsHead"`
		}
		var ergoBin sizeElfOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "sizes-elf", defBin, stripBin, "42"), &ergoBin); err != nil {
			t.Fatal(err)
		}
		if ergoBin.Size != uint64(goBin.Size) || ergoBin.Stripped != uint64(goBin.Stripped) {
			t.Fatalf("binary sizes mismatch: ergo=(%d,%d) go=(%d,%d)", ergoBin.Size, ergoBin.Stripped, goBin.Size, goBin.Stripped)
		}
		if ergoBin.MainSymbols != goBin.MainSymbols || ergoBin.GoPackages != goBin.GoPackages {
			t.Fatalf("symbol counts mismatch: ergo=(%d,%d) go=(%d,%d)", ergoBin.MainSymbols, ergoBin.GoPackages, goBin.MainSymbols, goBin.GoPackages)
		}
		wantSections := sortedNameSizes(goBin.Sections)
		if !slices.Equal(ergoBin.Sections, wantSections) {
			t.Fatalf("ELF sections mismatch:\nergo=%v\ngo=%v", ergoBin.Sections, wantSections)
		}
		wantText := sortedNameSizes(goBin.Text)
		if !slices.Equal(ergoBin.Text, wantText) {
			t.Fatalf("ELF text package breakdown mismatch:\nergo=%v\ngo=%v", ergoBin.Text, wantText)
		}

		// Real checked-in budgets.json + Go vs Ergo CheckBudget agreement.
		budgetsPath := filepath.Join(root, "internal", "sizes", "budgets.json")
		goBudget, err := sizes.Checked()
		if err != nil {
			t.Fatalf("sizes.Checked: %v", err)
		}
		ctrl := goBin
		receipts := []sizes.Receipt{
			{
				Fixture: "hello",
				Binary: sizes.Binary{
					Size:        ctrl.Size + 999_999_999,
					Stripped:    ctrl.Stripped + 999_999_999,
					Text:        map[string]uint64{"fmt": 4096, "main": 128},
					Sections:    map[string]uint64{".text": 8192},
					GoPackages:  999,
					MainSymbols: 1,
				},
				Control: ctrl,
			},
			{
				Fixture: "unknown-fixture",
				Binary:  ctrl,
				Control: ctrl,
			},
		}
		receiptJSONLines := make([]json.RawMessage, 0, len(receipts))
		for _, r := range receipts {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			receiptJSONLines = append(receiptJSONLines, json.RawMessage(b))
		}
		receiptsPath := filepath.Join(work, "receipts.json")
		rawReceipts, err := json.Marshal(receiptJSONLines)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receiptsPath, rawReceipts, 0o600); err != nil {
			t.Fatal(err)
		}
		type budgetCheckOut struct {
			Reason       string   `json:"reason"`
			FixtureCount int      `json:"fixtureCount"`
			Valid        bool     `json:"valid"`
			LedgerError  string   `json:"ledgerError"`
			Problems     []string `json:"problems"`
		}
		var bOut budgetCheckOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "sizes-budget", budgetsPath, receiptsPath), &bOut); err != nil {
			t.Fatal(err)
		}
		if !bOut.Valid || bOut.LedgerError != "" || bOut.FixtureCount != len(goBudget.Fixtures) {
			t.Fatalf("budget validation mismatch: %+v", bOut)
		}
		goProblems := goBudget.Check(receipts)
		if !slices.Equal(bOut.Problems, goProblems) {
			t.Fatalf("CheckBudget problems mismatch:\nergo=%v\ngo=%v", bOut.Problems, goProblems)
		}
	})

	t.Run("ProfMD", func(t *testing.T) {
		cpuBytes := buildSyntheticCPUProfileGz(t)
		cpuPath := filepath.Join(work, "cpu.pb.gz")
		if err := os.WriteFile(cpuPath, cpuBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		goCPUMd, err := profmd.RenderCPUMarkdown(cpuBytes, profmd.Options{Top: 10, TopStacks: 5})
		if err != nil {
			t.Fatalf("go RenderCPUMarkdown: %v", err)
		}
		ergoCPUMd := string(runTaskProfmdDump(t, ctx, bin, "profmd", "cpu", cpuPath, "10", "5"))
		if ergoCPUMd != goCPUMd {
			t.Fatalf("CPU markdown differs:\n--- ergo ---\n%s\n--- go ---\n%s", ergoCPUMd, goCPUMd)
		}

		// Real live Go runtime heap profile via pprof.WriteHeapProfile.
		retained := make([][]byte, 8)
		for i := range retained {
			buf := make([]byte, 32*1024)
			buf[0] = byte(i + 1)
			retained[i] = buf
		}
		runtime.KeepAlive(retained)
		var heapBuf bytes.Buffer
		if err := pprof.WriteHeapProfile(&heapBuf); err != nil {
			t.Fatalf("WriteHeapProfile: %v", err)
		}
		heapBytes := heapBuf.Bytes()
		heapPath := filepath.Join(work, "heap.pb.gz")
		if err := os.WriteFile(heapPath, heapBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		goHeapMd, err := profmd.RenderHeapMarkdown(heapBytes, profmd.Options{Top: 15, TopStacks: 8})
		if err != nil {
			t.Fatalf("go RenderHeapMarkdown: %v", err)
		}
		ergoHeapMd := string(runTaskProfmdDump(t, ctx, bin, "profmd", "heap", heapPath, "15", "8"))
		if ergoHeapMd != goHeapMd {
			t.Fatalf("Heap markdown differs:\n--- ergo ---\n%s\n--- go ---\n%s", ergoHeapMd, goHeapMd)
		}
	})

	t.Run("TaskAffectedAndGraph", func(t *testing.T) {
		treeRoot := filepath.Join(work, "aff-tree")
		for _, sub := range []string{"src", "dir"} {
			if err := os.MkdirAll(filepath.Join(treeRoot, sub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(treeRoot, "src", "in.txt"), []byte("v1"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(treeRoot, "dir", "entry.txt"), []byte("d1"), 0o644); err != nil {
			t.Fatal(err)
		}

		store := lawcache.Memory(task.StoreIdentity)
		r := &task.Runner{Root: treeRoot, Store: store}
		nB := task.Node{
			Name: "genB", Class: task.Vouched, Ident: "go run ./b",
			Env: func(context.Context) (string, error) { return "E=1\n", nil },
			Inputs: func(context.Context, string) (task.Inputs, error) {
				return task.Inputs{Files: []string{"mid.txt"}, Dirs: []string{"dir"}}, nil
			},
			Run: func(ctx context.Context, root string) ([]byte, error) {
				data, _ := os.ReadFile(filepath.Join(root, "mid.txt"))
				return nil, os.WriteFile(filepath.Join(root, "end.txt"), append(data, 'b'), 0o644)
			},
		}
		nA := task.Node{
			Name: "genA", Class: task.Derived, Ident: "go run ./a",
			Env: func(context.Context) (string, error) { return "E=1\n", nil },
			Inputs: func(context.Context, string) (task.Inputs, error) {
				return task.Inputs{Files: []string{"src/in.txt"}}, nil
			},
			Run: func(ctx context.Context, root string) ([]byte, error) {
				data, _ := os.ReadFile(filepath.Join(root, "src", "in.txt"))
				return nil, os.WriteFile(filepath.Join(root, "mid.txt"), append(data, 'a'), 0o644)
			},
		}
		nodes := []task.Node{nB, nA}
		var outBuf bytes.Buffer
		r.Out = &outBuf
		if _, err := r.Gen(ctx, nodes, task.Mode{}); err != nil {
			t.Fatalf("go task.Gen: %v", err)
		}
		goGraph, err := r.Graph(nodes)
		if err != nil {
			t.Fatalf("go task.Graph: %v", err)
		}
		base := fakeGoBase{
			changed: map[string]bool{"src/in.txt": true},
			listings: map[string]string{
				"dir": task.ListingDigest([]task.Entry{{Name: "entry.txt", Dir: false}}),
			},
		}
		goAffected, err := r.Affected(nodes, base)
		if err != nil {
			t.Fatalf("go task.Affected: %v", err)
		}

		// Feed the exact raw stored records from Go's lawcache into Ergo's task-affected.
		type kvStr struct {
			Key string `json:"key"`
			Val string `json:"val"`
		}
		type taskRecIn struct {
			Name     string  `json:"name"`
			Class    string  `json:"class"`
			Ident    string  `json:"ident"`
			Env      string  `json:"env"`
			Files    []kvStr `json:"files"`
			Dirs     []kvStr `json:"dirs"`
			Outs     []kvStr `json:"outs"`
			EntryKey string  `json:"entryKey"`
			RawText  string  `json:"rawText"`
		}
		type taskAffIn struct {
			Root         string      `json:"root"`
			Records      []taskRecIn `json:"records"`
			Changed      []string    `json:"changed"`
			BaseDirs     []kvStr     `json:"baseDirs"`
			WorkDirs     []kvStr     `json:"workDirs"`
			CurFiles     []kvStr     `json:"curFiles"`
			CurEnv       string      `json:"curEnv"`
			LookupIdx    int         `json:"lookupIdx"`
			DiffA        string      `json:"diffA"`
			DiffB        string      `json:"diffB"`
			SeedStatText string      `json:"seedStatText"`
		}
		recsIn := make([]taskRecIn, len(nodes))
		for i, n := range nodes {
			k := lawcache.Key("task last", treeRoot, n.Name, n.Ident)
			rawRec, ok := store.Get(k)
			if !ok {
				t.Fatalf("missing Go lastKey for %s", n.Name)
			}
			recsIn[i] = taskRecIn{
				Name:    n.Name,
				Class:   string(n.Class),
				Ident:   n.Ident,
				Files:   []kvStr{},
				Dirs:    []kvStr{},
				Outs:    []kvStr{},
				RawText: rawRec,
			}
		}
		workList := task.ListingDigest([]task.Entry{{Name: "entry.txt", Dir: false}})
		affPayload, err := json.Marshal(taskAffIn{
			Root:     treeRoot,
			Records:  recsIn,
			Changed:  []string{"src/in.txt"},
			BaseDirs: []kvStr{{Key: "dir", Val: workList}},
			WorkDirs: []kvStr{{Key: "dir", Val: workList}},
			CurFiles: []kvStr{},
			CurEnv:   "E=1\n",
			LookupIdx: -1,
			DiffA:    "A=1\nB=2\n",
			DiffB:    "A=1\nB=3\n",
		})
		if err != nil {
			t.Fatal(err)
		}
		affInPath := filepath.Join(work, "task-affected.json")
		if err := os.WriteFile(affInPath, affPayload, 0o600); err != nil {
			t.Fatal(err)
		}
		type edgeOut struct {
			From uint32 `json:"from"`
			To   uint32 `json:"to"`
		}
		type affectOut struct {
			Name string `json:"name"`
			Why  string `json:"why"`
		}
		type taskAffOut struct {
			Order        []uint32    `json:"order"`
			Edges        []edgeOut   `json:"edges"`
			Cycles       [][]uint32  `json:"cycles"`
			Affected     []affectOut `json:"affected"`
			FirstDiffOut string      `json:"firstDiffOut"`
		}
		var ergoAff taskAffOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "task-affected", affInPath), &ergoAff); err != nil {
			t.Fatal(err)
		}
		goOrderU32 := make([]uint32, len(goGraph.Order))
		for i, x := range goGraph.Order {
			goOrderU32[i] = uint32(x)
		}
		if !slices.Equal(ergoAff.Order, goOrderU32) {
			t.Fatalf("Graph Order mismatch: ergo=%v go=%v", ergoAff.Order, goOrderU32)
		}
		goEdges := make([]edgeOut, len(goGraph.Edges))
		for i, e := range goGraph.Edges {
			goEdges[i] = edgeOut{From: uint32(e[0]), To: uint32(e[1])}
		}
		if !slices.Equal(ergoAff.Edges, goEdges) {
			t.Fatalf("Graph Edges mismatch: ergo=%v go=%v", ergoAff.Edges, goEdges)
		}
		gotAff := make([]task.Affect, len(ergoAff.Affected))
		for i, a := range ergoAff.Affected {
			gotAff[i] = task.Affect{Name: a.Name, Why: a.Why}
		}
		if !slices.Equal(gotAff, goAffected) {
			t.Fatalf("Affected mismatch:\nergo=%+v\ngo=%+v", gotAff, goAffected)
		}
		if ergoAff.FirstDiffOut != "\"B=2\" -> \"B=3\"" {
			t.Fatalf("FirstDiff mismatch: got=%q", ergoAff.FirstDiffOut)
		}
	})

	t.Run("TaskEnvAndGoNode", func(t *testing.T) {
		type taskEnvIn struct {
			Host     []string `json:"host"`
			Names    []string `json:"names"`
			Extra    []string `json:"extra"`
			Root     string   `json:"root"`
			GoList   string   `json:"goList"`
			GenName  string   `json:"genName"`
			GenCmd   []string `json:"genCmd"`
			GenReads []string `json:"genReads"`
		}
		type taskEnvOut struct {
			Entries  []string `json:"entries"`
			EnvKey   string   `json:"envKey"`
			Environ  []string `json:"environ"`
			Pkg      string   `json:"pkg"`
			IsTest   bool     `json:"isTest"`
			Ident    string   `json:"ident"`
			PkgError string   `json:"pkgError"`
			InFiles  []string `json:"inFiles"`
			InDirs   []string `json:"inDirs"`
		}
		in := taskEnvIn{
			Host:     []string{"PATH=/usr/bin", "HOME=/h", "GOOS=linux", "CGO_ENABLED=1", "SECRET=no"},
			Names:    []string{"MISSING_VAR"},
			Extra:    []string{"GOMAXPROCS=2", "ERGO_BUDGET_GRANT=lease-42"},
			Root:     root,
			GoList:   "P " + filepath.Join(root, "cmd", "ergo") + " main.go\nM " + filepath.Join(root, "go.mod") + "\n",
			GenName:  "linked",
			GenCmd:   []string{"go", "run", "./scripts/linked"},
			GenReads: []string{"compiler"},
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(work, "task-env.json")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		var out taskEnvOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "task-env", p), &out); err != nil {
			t.Fatal(err)
		}
		goNode, err := task.GoNode(task.GoGen{Name: in.GenName, Command: in.GenCmd, Reads: in.GenReads}, task.ChildEnv(out.Entries))
		if err != nil {
			t.Fatalf("task.GoNode: %v", err)
		}
		if out.Ident != goNode.Ident {
			t.Fatalf("GoGen Ident mismatch: ergo=%q go=%q", out.Ident, goNode.Ident)
		}
		goEnvKey := task.ChildEnv(out.Entries).Key()
		if out.EnvKey != goEnvKey {
			t.Fatalf("EnvKey mismatch:\nergo=%q\ngo=%q", out.EnvKey, goEnvKey)
		}
		if !strings.Contains(out.EnvKey, "ERGO_BUDGET_GRANT=(lent grant)\n") || !strings.Contains(out.EnvKey, "\x00unset MISSING_VAR\n") || strings.Contains(out.EnvKey, "SECRET") {
			t.Fatalf("unexpected EnvKey contents: %q", out.EnvKey)
		}
		if out.Pkg != "./scripts/linked" || out.IsTest || out.PkgError != "" {
			t.Fatalf("unexpected GoPackage output: %+v", out)
		}
		if !slices.Contains(out.InFiles, "cmd/ergo/main.go") || !slices.Contains(out.InFiles, "go.mod") {
			t.Fatalf("missing GoList inputs: %v", out.InFiles)
		}
	})

	t.Run("TaskVerdictsAndGen", func(t *testing.T) {
		type kvStr struct {
			Key string `json:"key"`
			Val string `json:"val"`
		}
		type stepIn struct {
			Task        string   `json:"task"`
			Verb        string   `json:"verb"`
			Member      string   `json:"member"`
			Class       string   `json:"class"`
			HasKey      bool     `json:"hasKey"`
			Retry       bool     `json:"retry"`
			After       []uint32 `json:"after"`
			Parts       []kvStr  `json:"parts"`
			PostParts   []kvStr  `json:"postParts"`
			Paths       []string `json:"paths"`
			CustomPaths []string `json:"customPaths"`
			UseCustom   bool     `json:"useCustom"`
			Outcomes    []bool   `json:"outcomes"`
			Logs        []string `json:"logs"`
		}
		type verdictsIn struct {
			Root         string   `json:"root"`
			Fresh        bool     `json:"fresh"`
			Affected     bool     `json:"affected"`
			Changed      []string `json:"changed"`
			RunWrites    []string `json:"runWrites"`
			PriorEntries []kvStr  `json:"priorEntries"`
			Steps        []stepIn `json:"steps"`
		}
		type stepOut struct {
			Task         string `json:"task"`
			Step         string `json:"step"`
			Member       string `json:"member"`
			Verdict      string `json:"verdict"`
			Replay       string `json:"replay"`
			Why          string `json:"why"`
			Class        string `json:"class"`
			Log          string `json:"log"`
			Seed         string `json:"seed"`
			Contradicted bool   `json:"contradicted"`
			BlockedBy    string `json:"blockedBy"`
		}
		type verdictsOut struct {
			Failed bool      `json:"failed"`
			Steps  []stepOut `json:"steps"`
		}

		vIn := verdictsIn{
			Root:         "/sim-verdict-root",
			Changed:      []string{},
			RunWrites:    []string{},
			PriorEntries: []kvStr{},
			Steps: []stepIn{
				{
					Task:        "gate",
					Verb:        "check",
					Member:      "compiler/a",
					Class:       string(task.Derived),
					HasKey:      true,
					After:       []uint32{},
					Parts:       []kvStr{{Key: "src", Val: "h1"}},
					PostParts:   []kvStr{},
					Paths:       []string{"compiler/a/*.ergo"},
					CustomPaths: []string{},
					Outcomes:    []bool{true},
					Logs:        []string{"check ok"},
				},
				{
					Task:        "gate",
					Verb:        "test",
					Member:      "compiler/a",
					Class:       string(task.Derived),
					HasKey:      true,
					Retry:       true,
					After:       []uint32{0},
					Parts:       []kvStr{{Key: "src", Val: "h1"}},
					PostParts:   []kvStr{},
					Paths:       []string{"compiler/a/*.ergo"},
					CustomPaths: []string{},
					Outcomes:    []bool{false, true},
					Logs:        []string{"FAIL seed = 777", "pass on retry"},
				},
				{
					Task:        "gate",
					Verb:        "lint",
					Member:      "compiler/b",
					Class:       string(task.Derived),
					HasKey:      true,
					After:       []uint32{1},
					Parts:       []kvStr{{Key: "src", Val: "h2"}},
					PostParts:   []kvStr{},
					Paths:       []string{"compiler/b"},
					CustomPaths: []string{},
					Outcomes:    []bool{},
					Logs:        []string{},
				},
			},
		}
		vPath := filepath.Join(work, "verdicts.json")
		vRaw, err := json.Marshal(vIn)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vPath, vRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		var ergoV verdictsOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "task-verdicts", vPath), &ergoV); err != nil {
			t.Fatal(err)
		}

		// Run matching plan in Go task.Runner.Verdicts.
		goStore := lawcache.Memory(task.StoreIdentity)
		goRunner := &task.Runner{Root: vIn.Root, Store: goStore}
		callCount := make([]int, len(vIn.Steps))
		goSteps := make([]task.Step, len(vIn.Steps))
		for i, s := range vIn.Steps {
			idx := i
			spec := s
			after := make([]int, len(s.After))
			for j, a := range s.After {
				after[j] = int(a)
			}
			goSteps[i] = task.Step{
				Task:   spec.Task,
				Verb:   spec.Verb,
				Member: spec.Member,
				Class:  task.Class(spec.Class),
				Retry:  spec.Retry,
				After:  after,
				Key: func(context.Context) (task.Key, error) {
					parts := make([]task.Part, len(spec.Parts))
					for pi, p := range spec.Parts {
						parts[pi] = task.Part{Name: p.Key, Digest: p.Val}
					}
					return task.Key{Parts: parts, Paths: spec.Paths}, nil
				},
				Run: func(context.Context) (bool, []byte, error) {
					n := callCount[idx]
					callCount[idx]++
					pass := true
					if n < len(spec.Outcomes) {
						pass = spec.Outcomes[n]
					}
					logStr := ""
					if n < len(spec.Logs) {
						logStr = spec.Logs[n]
					}
					return pass, []byte(logStr), nil
				},
			}
		}
		goRep, err := goRunner.Verdicts(ctx, goSteps, task.VerdictMode{})
		if err != nil {
			t.Fatalf("go Verdicts: %v", err)
		}
		if ergoV.Failed != goRep.Failed(false) || len(ergoV.Steps) != len(goRep.Steps) {
			t.Fatalf("verdicts summary mismatch: ergo=%+v go=%+v", ergoV, goRep)
		}
		for i, gs := range goRep.Steps {
			es := ergoV.Steps[i]
			if es.Task != gs.Task || es.Step != gs.Step || es.Member != gs.Member ||
				es.Verdict != string(gs.Verdict) || es.Replay != gs.Replay ||
				es.Why != gs.Why || es.Seed != gs.Seed ||
				es.Contradicted != gs.Contradicted || es.BlockedBy != gs.BlockedBy {
				t.Fatalf("step %d verdict mismatch:\nergo=%+v\ngo=%+v", i, es, gs)
			}
		}

		// Also run task-gen parity on multi-pass topological discovery ([genB, genA]).
		type genNodeIn struct {
			Name   string `json:"name"`
			Class  string `json:"class"`
			Ident  string `json:"ident"`
			InFile string `json:"inFile"`
			Out    string `json:"out"`
			Drift  bool   `json:"drift"`
		}
		type taskGenIn struct {
			Root  string      `json:"root"`
			Files []kvStr     `json:"files"`
			Nodes []genNodeIn `json:"nodes"`
			Check bool        `json:"check"`
			Fresh bool        `json:"fresh"`
		}
		type taskGenOut struct {
			Order        []string `json:"order"`
			Passes       int      `json:"passes"`
			Failed       bool     `json:"failed"`
			Contradicted []string `json:"contradicted"`
			Stale        []string `json:"stale"`
			Lines        []string `json:"lines"`
			Files        []kvStr  `json:"files"`
		}
		gIn := taskGenIn{
			Root:  "/sim-gen",
			Files: []kvStr{{Key: "src.txt", Val: "v1"}},
			Nodes: []genNodeIn{
				{Name: "genB", Class: string(task.Vouched), Ident: "go run ./b", InFile: "mid.txt", Out: "end.txt"},
				{Name: "genA", Class: string(task.Derived), Ident: "go run ./a", InFile: "src.txt", Out: "mid.txt"},
			},
		}
		gRaw, err := json.Marshal(gIn)
		if err != nil {
			t.Fatal(err)
		}
		gPath := filepath.Join(work, "task-gen.json")
		if err := os.WriteFile(gPath, gRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		var ergoGen taskGenOut
		if err := json.Unmarshal(runTaskProfmdDump(t, ctx, bin, "task-gen", gPath), &ergoGen); err != nil {
			t.Fatal(err)
		}
		if ergoGen.Failed || !slices.Equal(ergoGen.Order, []string{"genA", "genB"}) {
			t.Fatalf("unexpected task-gen output: %+v", ergoGen)
		}
	})
}

type fakeWaitT struct {
	has      bool
	deadline time.Time
}

func (f fakeWaitT) Helper() {}
func (f fakeWaitT) Deadline() (time.Time, bool) {
	return f.deadline, f.has
}

type nameSizePair struct {
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
}

func sortedNameSizes(m map[string]uint64) []nameSizePair {
	keys := slices.Sorted(maps.Keys(m))
	out := make([]nameSizePair, 0, len(keys))
	for _, k := range keys {
		out = append(out, nameSizePair{Name: k, Bytes: m[k]})
	}
	slices.SortFunc(out, func(a, b nameSizePair) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

type fakeGoBase struct {
	changed  map[string]bool
	listings map[string]string
}

func (b fakeGoBase) Changed(path string) bool { return b.changed[path] }
func (b fakeGoBase) Listing(dir string) (string, error) {
	if s, ok := b.listings[dir]; ok {
		return s, nil
	}
	return task.Missing, nil
}

func buildSyntheticCPUProfileGz(t *testing.T) []byte {
	t.Helper()
	var pb []byte
	strIdx := map[string]int64{}
	var stringsTab []string
	addStr := func(s string) int64 {
		if idx, ok := strIdx[s]; ok {
			return idx
		}
		idx := int64(len(stringsTab))
		strIdx[s] = idx
		stringsTab = append(stringsTab, s)
		return idx
	}
	addStr("")
	stType := addStr("cpu")
	stUnit := addStr("nanoseconds")
	fnA := addStr("main.hot")
	fnB := addStr("main.caller")
	fileA := addStr("cmd/ergo/hot.go")
	fileB := addStr("cmd/ergo/caller.go")

	appendVarint := func(dst []byte, v uint64) []byte {
		var tmp [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(tmp[:], v)
		return append(dst, tmp[:n]...)
	}
	appendFieldBytes := func(dst []byte, num int, body []byte) []byte {
		dst = appendVarint(dst, uint64(num<<3|2))
		dst = appendVarint(dst, uint64(len(body)))
		return append(dst, body...)
	}
	appendFieldVarint := func(dst []byte, num int, v uint64) []byte {
		dst = appendVarint(dst, uint64(num<<3))
		return appendVarint(dst, v)
	}

	var st []byte
	st = appendFieldVarint(st, 1, uint64(stType))
	st = appendFieldVarint(st, 2, uint64(stUnit))
	pb = appendFieldBytes(pb, 1, st)

	for _, f := range []struct {
		id, name, file int64
	}{{1, fnA, fileA}, {2, fnB, fileB}} {
		var fm []byte
		fm = appendFieldVarint(fm, 1, uint64(f.id))
		fm = appendFieldVarint(fm, 2, uint64(f.name))
		fm = appendFieldVarint(fm, 4, uint64(f.file))
		pb = appendFieldBytes(pb, 5, fm)
	}
	for _, loc := range []struct {
		id, fnID, line int64
	}{{1, 1, 42}, {2, 2, 17}} {
		var ln, lm []byte
		ln = appendFieldVarint(ln, 1, uint64(loc.fnID))
		ln = appendFieldVarint(ln, 2, uint64(loc.line))
		lm = appendFieldVarint(lm, 1, uint64(loc.id))
		lm = appendFieldBytes(lm, 4, ln)
		pb = appendFieldBytes(pb, 4, lm)
	}
	var smp []byte
	smp = appendFieldVarint(smp, 1, 1)
	smp = appendFieldVarint(smp, 1, 2)
	smp = appendFieldVarint(smp, 2, 1_500_000)
	pb = appendFieldBytes(pb, 2, smp)

	for _, s := range stringsTab {
		pb = appendFieldBytes(pb, 6, []byte(s))
	}
	pb = appendFieldVarint(pb, 10, 1_500_000)
	pb = appendFieldBytes(pb, 11, st)

	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(pb); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return gz.Bytes()
}

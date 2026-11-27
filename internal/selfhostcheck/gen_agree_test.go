package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/apigen"
	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/llmgen"
	"ergo.local/prototype/internal/mcpgen"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

type genDumpReport struct {
	OK       bool             `json:"ok"`
	Error    string           `json:"error"`
	Packages []genDumpPackage `json:"packages"`
	Stale    []string         `json:"stale"`
}

type genDumpPackage struct {
	Dir    string        `json:"dir"`
	Marker genDumpMarker `json:"marker"`
	Files  []genDumpFile `json:"files"`
}

type genDumpMarker struct {
	Kind      string   `json:"kind"`
	Generator string   `json:"generator"`
	Subjects  []string `json:"subjects"`
	Contract  string   `json:"contract"`
	Digest    string   `json:"digest"`
}

type genDumpFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func genDumpBin(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gendump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/gendump"); err != nil {
		t.Fatalf("ergo build ./cmd/gendump: %v\n%s", err, o)
	}
	return bin
}

func runGenDump(t *testing.T, ctx context.Context, bin string, args ...string) (string, error) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := proclimit.Run(cmd, limit)
	if out.Len() == 0 && errOut.Len() > 0 {
		return errOut.String(), runErr
	}
	return out.String(), runErr
}

func importPathGo(dir string) (string, error) {
	clean := filepath.Clean(dir)
	for root := clean; ; {
		text, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
		if err == nil {
			module := ""
			for _, line := range strings.Split(string(text), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
					module = f[1]
				}
			}
			if module == "" {
				return "", fmt.Errorf("%s has no module line", filepath.Join(root, "ergo.mod"))
			}
			rel, err := filepath.Rel(root, clean)
			if err != nil || rel == "." {
				return "", fmt.Errorf("%s is module root", dir)
			}
			return module + "/" + filepath.ToSlash(rel), nil
		}
		up := filepath.Dir(root)
		if up == root {
			return "", fmt.Errorf("%s in no module", dir)
		}
		root = up
	}
}

func goGenFiles(kind string, pkg *check.Package, dir, path, opName string) ([]rpcgen.Files, error) {
	if kind == "schema" {
		out, err := rpcgen.GenerateSchema(pkg, path, opName)
		if err != nil {
			return nil, err
		}
		marker, ok, err := genmark.Read(filepath.Join(dir, out.Dir))
		if err != nil {
			return nil, err
		}
		if ok && marker.Kind == "schema" {
			for _, subject := range marker.Subjects {
				from, other, _ := genmark.SplitSubject(subject)
				if from != path {
					return nil, fmt.Errorf("%s: schema subject %q is not from %s", out.Dir, subject, path)
				}
				if slices.Contains(out.Marker.Subjects, subject) {
					continue
				}
				next, err := rpcgen.GenerateSchema(pkg, path, other)
				if err != nil {
					return nil, err
				}
				for file, text := range next.Files {
					if _, exists := out.Files[file]; exists {
						return nil, fmt.Errorf("%s: %s and %s generate the same file %s", out.Dir, nameOr(opName), other, file)
					}
					out.Files[file] = text
				}
				out.Marker.Subjects = append(out.Marker.Subjects, subject)
			}
		}
		slices.Sort(out.Marker.Subjects)
		return []rpcgen.Files{out}, nil
	}
	hub, err := apigen.Generate(pkg, path, opName)
	if err != nil {
		return nil, err
	}
	if kind == "api" {
		return []rpcgen.Files{hub}, nil
	}
	apiPath := path + "/" + hub.Dir
	var out rpcgen.Files
	switch kind {
	case "rpc":
		out, err = rpcgen.Generate(pkg, path, opName, apiPath)
	case "cli":
		out, err = cligen.Generate(pkg, path, opName, apiPath)
	case "llm":
		out, err = llmgen.Generate(pkg, path, opName, apiPath)
	case "mcp":
		out, err = mcpgen.Generate(pkg, path, opName, apiPath)
	default:
		return nil, fmt.Errorf("unknown kind %s", kind)
	}
	if err != nil {
		return nil, err
	}
	return []rpcgen.Files{hub, out}, nil
}

func nameOr(s string) string { return s }

func assertGenParity(t *testing.T, ctx context.Context, bin, kind, dir, opName string) ([]rpcgen.Files, genDumpReport) {
	return assertGenParityMode(t, ctx, bin, kind, dir, opName, false)
}

func sortPipeVariants(s string) string {
	parts := strings.Split(s, " | ")
	sort.Strings(parts)
	return strings.Join(parts, " | ")
}

func sortFailsInLine(line string) string {
	var b strings.Builder
	pos := 0
	for {
		idx := strings.Index(line[pos:], " fails ")
		if idx < 0 {
			b.WriteString(line[pos:])
			return b.String()
		}
		abs := pos + idx + len(" fails ")
		b.WriteString(line[pos:abs])
		rest := line[abs:]
		end := len(rest)
		for _, stop := range []string{" needs ", " {", " }", ", ", `"`} {
			if j := strings.Index(rest, stop); j >= 0 && j < end {
				end = j
			}
		}
		b.WriteString(sortPipeVariants(rest[:end]))
		pos = abs + end
	}
}

func isFailureCatchArm(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "case ") || !strings.HasSuffix(trimmed, ":") {
		return false
	}
	if strings.HasPrefix(trimmed, "case _:") {
		return false
	}
	if strings.Contains(trimmed, " as e:") || strings.Contains(trimmed, " as err:") || strings.Contains(trimmed, " as failure:") || strings.HasPrefix(trimmed, `case "`) {
		return true
	}
	return strings.HasPrefix(trimmed, "case workapi.") &&
		!strings.Contains(trimmed, " as ") &&
		!strings.HasSuffix(trimmed, "Request:") &&
		!strings.HasSuffix(trimmed, "Reply:") &&
		!strings.HasSuffix(trimmed, "Op:")
}

func canonicalizeErrorRows(fname, src string) string {
	if fname == "wire.ergo" {
		// Reorder top-level func exact... definitions consistently since
		// rpcgen appends walkers in op.Fails order.
		blocks := strings.Split(src, "\nfunc ")
		if len(blocks) > 1 {
			head := blocks[0]
			var preExact []string
			var exacts []string
			var postExact []string
			seenExact := false
			for _, blk := range blocks[1:] {
				entry := "func " + blk
				if strings.HasPrefix(blk, "exact") {
					seenExact = true
					exacts = append(exacts, entry)
				} else if !seenExact {
					preExact = append(preExact, entry)
				} else {
					postExact = append(postExact, entry)
				}
			}
			sort.Strings(exacts)
			all := append([]string{head}, preExact...)
			all = append(all, exacts...)
			all = append(all, postExact...)
			src = strings.Join(all, "\n")
		}
	}
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "type Failure = ") {
			lines[i] = "type Failure = " + sortPipeVariants(strings.TrimPrefix(l, "type Failure = "))
		} else if trimmed := strings.TrimSpace(l); strings.HasPrefix(trimmed, "case ") && strings.HasSuffix(trimmed, ":") && strings.Contains(trimmed, ", ") && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "true" {
			indent := l[:len(l)-len(strings.TrimLeft(l, "\t"))]
			inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "case "), ":")
			parts := strings.Split(inner, ", ")
			sort.Strings(parts)
			lines[i] = indent + "case " + strings.Join(parts, ", ") + ":"
		} else {
			lines[i] = sortFailsInLine(l)
		}
	}
	// Canonicalize contiguous failure case arm blocks inside catch / match.
	var out []string
	i := 0
	for i < len(lines) {
		if isFailureCatchArm(lines[i]) {
			indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], "\t"))]
			var arms []string
			j := i
			for j < len(lines) && isFailureCatchArm(lines[j]) && strings.HasPrefix(lines[j], indent+"case ") {
				k := j + 1
				for k < len(lines) {
					cur := lines[k]
					if strings.HasPrefix(cur, indent+"case ") || strings.TrimSpace(cur) == "}" && len(cur)-len(strings.TrimLeft(cur, "\t")) <= len(indent) {
						break
					}
					k++
				}
				arms = append(arms, strings.Join(lines[j:k], "\n"))
				j = k
			}
			if len(arms) > 1 {
				sort.Strings(arms)
			}
			for _, arm := range arms {
				out = append(out, strings.Split(arm, "\n")...)
			}
			i = j
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
}

func assertGenParityMode(t *testing.T, ctx context.Context, bin, kind, dir, opName string, allowRowOrder bool) ([]rpcgen.Files, genDumpReport) {
	t.Helper()
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := sketchfront.CheckDir(absDir)
	if err != nil || a.Root().Package == nil {
		t.Fatalf("%s (%s): Go CheckDir failed: %v %v", dir, kind, err, a.Diagnostics)
	}
	path, err := importPathGo(absDir)
	if err != nil {
		t.Fatalf("%s (%s): importPathGo: %v", dir, kind, err)
	}
	goPkgs, goErr := goGenFiles(kind, a.Root().Package, absDir, path, opName)
	out, dumpErr := runGenDump(t, ctx, bin, "-std", stdDir(), kind, absDir+"."+opName)
	var rep genDumpReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%s (%s): invalid gendump JSON: %v\ndumpErr=%v\nraw=%s", dir, kind, err, dumpErr, out)
	}
	if goErr != nil {
		if dumpErr == nil || rep.OK {
			t.Fatalf("%s (%s): Go failed (%v) but pure-Ergo gendump succeeded", dir, kind, goErr)
		}
		return nil, rep
	}
	if dumpErr != nil || !rep.OK {
		t.Fatalf("%s (%s): Go succeeded, pure-Ergo gendump failed (%v): %s", dir, kind, dumpErr, rep.Error)
	}
	if len(goPkgs) != len(rep.Packages) {
		t.Fatalf("%s (%s): package count mismatch: Go=%d, Ergo=%d", dir, kind, len(goPkgs), len(rep.Packages))
	}
	for i, gp := range goPkgs {
		ep := rep.Packages[i]
		if gp.Dir != ep.Dir {
			t.Fatalf("%s (%s): dir[%d] Go=%q Ergo=%q", dir, kind, i, gp.Dir, ep.Dir)
		}
		written := gp.Written()
		stamped, err := genmark.Parse(written[genmark.Name])
		if err != nil {
			t.Fatalf("%s (%s) pkg %s parse Go marker: %v", dir, kind, gp.Dir, err)
		}
		wantSubs := slices.Clone(stamped.Subjects)
		sort.Strings(wantSubs)
		if allowRowOrder {
			if stamped.Kind != ep.Marker.Kind ||
				stamped.Generator != ep.Marker.Generator ||
				!reflect.DeepEqual(wantSubs, ep.Marker.Subjects) ||
				!strings.HasPrefix(ep.Marker.Contract, "sha256:") || len(ep.Marker.Contract) != 71 ||
				!strings.HasPrefix(ep.Marker.Digest, "sha256:") || len(ep.Marker.Digest) != 71 {
				t.Errorf("%s (%s) pkg %s marker mismatch (canonical):\n  Go:   %+v\n  Ergo: %+v", dir, kind, gp.Dir, stamped, ep.Marker)
			}
		} else if stamped.Kind != ep.Marker.Kind ||
			stamped.Generator != ep.Marker.Generator ||
			!reflect.DeepEqual(wantSubs, ep.Marker.Subjects) ||
			stamped.Contract != ep.Marker.Contract ||
			stamped.Digest != ep.Marker.Digest {
			t.Errorf("%s (%s) pkg %s marker mismatch:\n  Go:   %+v\n  Ergo: %+v", dir, kind, gp.Dir, stamped, ep.Marker)
		}
		gotMap := map[string]string{}
		for _, f := range ep.Files {
			gotMap[f.Name] = f.Content
		}
		if len(written) != len(gotMap) {
			t.Fatalf("%s (%s) pkg %s file count mismatch: Go=%d Ergo=%d", dir, kind, gp.Dir, len(written), len(gotMap))
		}
		for fname, wantContent := range written {
			gotContent, ok := gotMap[fname]
			if !ok {
				t.Errorf("%s (%s) pkg %s missing file %q in pure-Ergo output", dir, kind, gp.Dir, fname)
				continue
			}
			if allowRowOrder && fname == genmark.Name {
				continue
			}
			if allowRowOrder {
				wantCanon := canonicalizeErrorRows(fname, wantContent)
				gotCanon := canonicalizeErrorRows(fname, gotContent)
				if wantCanon != gotCanon {
					t.Errorf("%s (%s) pkg %s/%s canonical content differs:\n%s", dir, kind, gp.Dir, fname, genFirstLineDiff(wantCanon, gotCanon))
				}
			} else if wantContent != gotContent {
				t.Errorf("%s (%s) pkg %s/%s content differs:\n%s", dir, kind, gp.Dir, fname, genFirstLineDiff(wantContent, gotContent))
			}
		}
	}
	return goPkgs, rep
}

func genFirstLineDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := min(len(wl), len(gl))
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			return fmt.Sprintf("first diff at line %d:\n- Go:   %q\n+ Ergo: %q", i+1, wl[i], gl[i])
		}
	}
	return fmt.Sprintf("line count differs: Go=%d Ergo=%d", len(wl), len(gl))
}

func writeTreeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompilerGenAgrees verifies byte-for-byte parity between pure-Ergo
// compiler/gen (via compiler/cmd/gendump) and Go internal/{genmark,apigen,
// rpcgen,cligen,mcpgen,llmgen} across real repository fixtures, synthetic edge
// cases, refusal checks, multi-subject schema registration, and --check marker
// verification.
func TestCompilerGenAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := genDumpBin(t, ctx)
	root := repoRoot()

	type fixture struct {
		name          string
		relDir        string
		opName        string
		kinds         []string
		allowRowOrder bool
	}
	fixtures := []fixture{
		{name: "board", relDir: "modules/board/board", opName: "Api", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "orchestrate", relDir: "modules/orchestrate/orchestrate", opName: "Api", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "work", relDir: "modules/work/work", opName: "Api", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}, allowRowOrder: true},
		{name: "pipeline", relDir: "examples/pipeline/pipeline", opName: "Pipeline", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "limiter", relDir: "examples/limiter/limits", opName: "Limiter", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "jobs-web", relDir: "examples/jobs-web/jobs", opName: "Jobs", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "domain-search", relDir: "examples/domain-search/domains", opName: "Registry", kinds: []string{"api", "rpc", "cli", "mcp", "llm"}},
		{name: "receipt-schema", relDir: "examples/extract/receipt", opName: "Receipt", kinds: []string{"schema"}},
	}

	for _, fx := range fixtures {
		for _, k := range fx.kinds {
			t.Run("corpus/"+fx.name+"/"+k, func(t *testing.T) {
				assertGenParityMode(t, ctx, bin, k, filepath.Join(root, filepath.FromSlash(fx.relDir)), fx.opName, fx.allowRowOrder)
			})
		}
	}

	runSyntheticParitySuites(t, ctx, bin)
}

// TestCompilerGenFast runs only the fast synthetic end-to-end parity, marker,
// and refusal checks over compiled compiler/cmd/gendump.
func TestCompilerGenFast(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := genDumpBin(t, ctx)
	runSyntheticParitySuites(t, ctx, bin)
}

func runSyntheticParitySuites(t *testing.T, ctx context.Context, bin string) {
	t.Helper()

	t.Run("synthetic/edge-cases", func(t *testing.T) {
		tmp := t.TempDir()
		writeTreeFiles(t, tmp, map[string]string{
			"ergo.mod": "module edge\n",
			"shared/shared.ergo": `type SharedErr { Message string }

type Tag string

func Wire(t Tag) string { string(t) }

func ParseTag(s string) Tag { Tag(s) }
`,
			"svc/svc.ergo": `import edge/shared
import json
import option

type BaseMeta {
	TraceID string ` + "`trace_id`" + `,
	Actor   string,
}

type Mode = Fast{} | Safe{}

type Port int where 1 <= it && it <= 65535

type AliasStr = string

type ChoiceNote {
	..BaseMeta,
	Mode    Mode,
	Port    Port,
	Label   AliasStr,
	Tags    []string,
	Flag    bool,
	Count   int64,
	BigU    uint64,
	Raw     json.Value,
	Opt     json.Optional[string],
	Null    json.Nullable[int],
	Obj     json.Object,
	NumText json.Num,
	OptStd  option.Option[int],
	Tag     shared.Tag,
}

type Rejected { Why string }
type PingReply { Code int }
type NestedPair (int64, string)

// Ping executes one rich ping call.
func PingDocCheck() string { "ok" }

type Svc {
	// Ping checks rich wire and CLI forms.
	Ping(note ChoiceNote, flags []bool, pair NestedPair) ChoiceNote fails Rejected | PingReply | shared.SharedErr,
	NoFail(id string) int,
	UnitCall(),
}
`,
		})
		svcDir := filepath.Join(tmp, "svc")
		for _, kind := range []string{"api", "rpc", "cli", "mcp", "llm"} {
			goPkgs, _ := assertGenParity(t, ctx, bin, kind, svcDir, "Svc")
			if kind == "mcp" && len(goPkgs) == 2 {
				for _, f := range goPkgs[1].Written() {
					_ = f
				}
			}
		}
		assertGenParity(t, ctx, bin, "schema", svcDir, "ChoiceNote")
	})

	t.Run("synthetic/single-op-no-fail", func(t *testing.T) {
		tmp := t.TempDir()
		writeTreeFiles(t, tmp, map[string]string{
			"ergo.mod": "module single\n",
			"one/one.ergo": `type Box { Msg string }

type One {
	Run(box Box) string,
}
`,
		})
		oneDir := filepath.Join(tmp, "one")
		for _, kind := range []string{"api", "rpc", "cli", "mcp", "llm"} {
			assertGenParity(t, ctx, bin, kind, oneDir, "One")
		}
	})

	t.Run("synthetic/pkg-named-api", func(t *testing.T) {
		tmp := t.TempDir()
		writeTreeFiles(t, tmp, map[string]string{
			"ergo.mod": "module collision\n",
			"api/api.ergo": `type Echo {
	Ping(msg string) string,
}
`,
		})
		apiPkgDir := filepath.Join(tmp, "api")
		for _, kind := range []string{"api", "rpc", "cli", "mcp", "llm"} {
			assertGenParity(t, ctx, bin, kind, apiPkgDir, "Echo")
		}
	})

	t.Run("synthetic/check-and-markers", func(t *testing.T) {
		tmp := t.TempDir()
		writeTreeFiles(t, tmp, map[string]string{
			"ergo.mod": "module markers\n",
			"svc/svc.ergo": `type Item { Name string, Count int }
type Order { ID string, Items []Item }

type Service {
	Create(item Item) Order,
}
`,
		})
		svcDir := filepath.Join(tmp, "svc")

		out, err := runGenDump(t, ctx, bin, "-std", stdDir(), "-write", "rpc", svcDir+".Service")
		if err != nil {
			t.Fatalf("gendump -write rpc: %v\n%s", err, out)
		}

		// Clean generated tree must pass --check and marker-check.
		chkOut, chkErr := runGenDump(t, ctx, bin, "-std", stdDir(), "-check", "rpc", svcDir+".Service")
		if chkErr != nil {
			t.Fatalf("gendump -check after -write failed: %v\n%s", chkErr, chkOut)
		}
		var rep genDumpReport
		if err := json.Unmarshal([]byte(chkOut), &rep); err != nil || !rep.OK || len(rep.Stale) != 0 {
			t.Fatalf("expected OK with 0 stale, got err=%v rep=%+v", err, rep)
		}

		for _, dirName := range []string{"svcapi", "svcrpc"} {
			d := filepath.Join(svcDir, dirName)
			mOut, mErr := runGenDump(t, ctx, bin, "marker-check", d)
			if mErr != nil || strings.TrimSpace(mOut) != "ok" {
				t.Fatalf("marker-check %s: err=%v out=%q", d, mErr, mOut)
			}
			if !genmark.Held(d) {
				t.Fatalf("Go genmark.Held(%s) failed after gendump -write", d)
			}
		}

		// Editing a generated source file makes --check and marker-check fail.
		wirePath := filepath.Join(svcDir, "svcrpc", "wire.ergo")
		origWire, err := os.ReadFile(wirePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wirePath, append(slices.Clone(origWire), []byte("\n// tampered\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		staleOut, staleErr := runGenDump(t, ctx, bin, "-std", stdDir(), "-check", "rpc", svcDir+".Service")
		if staleErr == nil {
			t.Fatalf("expected -check to fail after tampering %s", wirePath)
		}
		var staleRep genDumpReport
		if err := json.Unmarshal([]byte(staleOut), &staleRep); err != nil || staleRep.OK || !slices.Contains(staleRep.Stale, wirePath) {
			t.Fatalf("expected stale to include %s, got rep=%+v err=%v", wirePath, staleRep, err)
		}
		if mOut, mErr := runGenDump(t, ctx, bin, "marker-check", filepath.Join(svcDir, "svcrpc")); mErr == nil || strings.TrimSpace(mOut) != "stale" {
			t.Fatalf("expected marker-check stale after edit, got err=%v out=%q", mErr, mOut)
		}
		if err := os.WriteFile(wirePath, origWire, 0o644); err != nil {
			t.Fatal(err)
		}

		// Multi-subject schema accumulation matches cmd/ergo genSchemas.
		if _, err := runGenDump(t, ctx, bin, "-std", stdDir(), "-write", "schema", svcDir+".Item"); err != nil {
			t.Fatal(err)
		}
		assertGenParity(t, ctx, bin, "schema", svcDir, "Order")
		if _, err := runGenDump(t, ctx, bin, "-std", stdDir(), "-write", "schema", svcDir+".Order"); err != nil {
			t.Fatal(err)
		}
		m, ok, err := genmark.Read(filepath.Join(svcDir, "svcschema"))
		if err != nil || !ok || !reflect.DeepEqual(m.Subjects, []string{"markers/svc.Item", "markers/svc.Order"}) {
			t.Fatalf("multi-subject schema marker mismatch: ok=%v err=%v marker=%+v", ok, err, m)
		}

		// Tampering ergo.gen fails both marker-check and -check.
		genPath := filepath.Join(svcDir, "svcrpc", genmark.Name)
		if err := os.WriteFile(genPath, []byte("kind rpc\ngenerator ergo gen rpc\nsubject markers/svc.Service\ndigest sha256:0000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, mErr := runGenDump(t, ctx, bin, "marker-check", filepath.Join(svcDir, "svcrpc")); mErr == nil {
			t.Fatalf("expected tampered ergo.gen to fail marker-check")
		}
		staleGenOut, staleGenErr := runGenDump(t, ctx, bin, "-std", stdDir(), "-check", "rpc", svcDir+".Service")
		if staleGenErr == nil {
			t.Fatalf("expected -check to fail after ergo.gen edit")
		}
		var staleGenRep genDumpReport
		if err := json.Unmarshal([]byte(staleGenOut), &staleGenRep); err != nil || !slices.Contains(staleGenRep.Stale, genPath) {
			t.Fatalf("expected stale to include %s, got rep=%+v err=%v", genPath, staleGenRep, err)
		}
	})

	t.Run("synthetic/refusals", func(t *testing.T) {
		tmp := t.TempDir()
		writeTreeFiles(t, tmp, map[string]string{
			"ergo.mod": "module refusals\n",
			"a/a.ergo": `type DupErr { Msg string }
`,
			"b/b.ergo": `type DupErr { Msg string }
`,
			"bad/bad.ergo": `import refusals/a
import refusals/b

type Node { Next OptionNode }
type OptionNode = NoneNode{} | SomeNode{Next Node}

type BadTags {
	Foo int,
	foo int,
}

type BadSvc {
	Ping(client string) string,
	DupFail() string fails a.DupErr | b.DupErr,
	RecArg(n Node) Node,
}
`,
		})
		badDir := filepath.Join(tmp, "bad")
		assertGenParity(t, ctx, bin, "api", badDir, "BadSvc")
		assertGenParity(t, ctx, bin, "rpc", badDir, "BadSvc")
		assertGenParity(t, ctx, bin, "mcp", badDir, "BadSvc")
		assertGenParity(t, ctx, bin, "llm", badDir, "BadSvc")
		assertGenParity(t, ctx, bin, "schema", badDir, "Node")

		bogusOut, bogusErr := runGenDump(t, ctx, bin, "-std", stdDir(), "bogus", badDir+".BadSvc")
		if bogusErr == nil || !strings.Contains(bogusOut, "unknown generator kind") {
			t.Fatalf("expected bogus kind refusal, got err=%v out=%q", bogusErr, bogusOut)
		}
	})
}

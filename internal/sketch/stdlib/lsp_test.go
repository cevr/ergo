package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdLSPLawsRun runs every law of std lsp at check time in process
// (falling back to native execution with ERGO_FALLBACK=off for runtimeOnly/machine laws)
// and holds every lsp row of runners.txt to a pass.
func TestStdLSPLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("lsp")
	if err != nil {
		t.Fatalf("import lsp: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 200_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate lsp: %v", err)
	}
	results := map[string][]eval.Result{}
	for _, r := range rep.Results {
		name := strings.TrimPrefix(r.Name, "law ")
		if head, _, ok := strings.Cut(name, " for "); ok {
			name = head
		}
		if head, _, ok := strings.Cut(name, " under "); ok {
			name = head
		}
		key := "lsp " + name
		results[key] = append(results[key], r)
	}
	outerT := t
	laws := map[string]lawledger.Law{}
	var ensureNativeRunner func(t *testing.T) (string, string)
	var ergoBin string
	var nativeModDir string
	ensureNativeRunner = func(t *testing.T) (string, string) {
		t.Helper()
		if ergoBin != "" {
			return ergoBin, nativeModDir
		}
		root := moduleRootDir(t)
		tmp := outerT.TempDir()
		ergoBin = filepath.Join(tmp, "bin", "ergo")
		if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
			t.Fatal(err)
		}
		build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
		build.Dir = root
		if out, err := runBound(build); err != nil {
			t.Fatalf("build ergo for native lsp law: %v\n%s", err, out)
		}
		nativeModDir = filepath.Join(tmp, "mod")
		pkgDir := filepath.Join(nativeModDir, "lsp")
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nativeModDir, "ergo.mod"), []byte("module stdlspcheck\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcDir := filepath.Join(root, "internal", "sketch", "std", "lsp")
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".ergo") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcDir, ent.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkgDir, ent.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return ergoBin, nativeModDir
	}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		row := row
		rs := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if len(rs) == 0 {
				bin, modDir := ensureNativeRunner(t)
				cmd := exec.Command(bin, "test", "--native", "--count", "1", "--seed", "1", "--law", row.Law, "./lsp")
				cmd.Dir = modDir
				cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
				out, err := runBound(cmd)
				if err != nil {
					t.Fatalf("native law %s (ERGO_FALLBACK=off): %v\n%s", row.Key(), err, out)
				}
				if !strings.Contains(out, "ok law "+row.Law) {
					t.Fatalf("native law %s did not report ok:\n%s", row.Key(), out)
				}
				return
			}
			passed := 0
			nativeSkipped := 0
			for _, r := range rs {
				switch r.Status {
				case eval.Passed:
					if r.Evidence != "tested" && r.Evidence != "proved" {
						t.Errorf("law %s (%s): passed with evidence=%q", row.Key(), r.Name, r.Evidence)
					} else {
						passed++
					}
				case eval.Skipped, eval.Native:
					nativeSkipped++
				default:
					t.Errorf("law %s (%s): %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Name, r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
				}
			}
			if passed == 0 && nativeSkipped > 0 {
				bin, modDir := ensureNativeRunner(t)
				cmd := exec.Command(bin, "test", "--native", "--count", "1", "--seed", "1", "--law", row.Law, "./lsp")
				cmd.Dir = modDir
				cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
				out, err := runBound(cmd)
				if err != nil {
					t.Fatalf("native law %s (ERGO_FALLBACK=off): %v\n%s", row.Key(), err, out)
				}
				if !strings.Contains(out, "ok law "+row.Law) {
					t.Fatalf("native law %s did not report ok:\n%s", row.Key(), out)
				}
				return
			}
			if passed == 0 {
				t.Errorf("law %s: no run passed (%+v)", row.Key(), rs)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const lspNativeDriverSrc = `import io
import json
import lsp
import option
import strings
import sync
import task

func main() needs io.Console {
	let srv = lsp.Server{
		Info:     lsp.ServerInfo{Name: "std-lsp-driver", Version: "1.0.0"},
		SyncKind: lsp.SyncIncremental,
	}
	let started = sync.NewLatch()
	let release = sync.NewLatch()
	let live = lsp.NewLive(16)
	let sess = lsp.NewSession()
	let handler = func(ctx lsp.RequestContext) json.Value fails lsp.RPCError {
		if ctx.Method == "ergo/hoverText" {
			let txt = match lsp.Text(ctx.Documents, "file:///driver.ergo") {
			case option.Some as s:
				s.Value
			case option.None:
				""
			}
			return json.Str{Value: txt}
		}
		if ctx.Method == "ergo/ping" {
			return json.Str{Value: "pong"}
		}
		if ctx.Method == "ergo/slow" {
			let _ = sync.Open(started)
			sync.Await(release)
			if ctx.IsCancelled() {
				return lsp.RPCError{Code: lsp.CodeRequestCancelled, Message: "cancelled", Data: option.None}
			}
			return json.Str{Value: "done"}
		}
		return lsp.RPCError{Code: lsp.CodeMethodNotFound, Message: "unknown method", Data: option.None}
	}
	let serverTask = task.Go() {
		with context { lsp.Transport: live } {
			lsp.ServeConcurrent(sess, srv, handler) catch {
			case _:
				-1
			}
		}
	}

	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"textDocument/hover\"}") catch {
	case _:
		io.Console.Println("ERR preinit send")
		return
	}
	let preInit = match lsp.NextServer(live) {
	case option.Some as r:
		r.Value
	case option.None:
		io.Console.Println("ERR preinit recv")
		return
	}
	if !strings.Contains(preInit, "-32002") {
		io.Console.Println("ERR expected -32002 got " + preInit)
		return
	}

	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"initialize\",\"params\":{\"capabilities\":{\"general\":{\"positionEncodings\":[\"utf-16\",\"utf-8\"]}}}}") catch {
	case _:
		io.Console.Println("ERR init send")
		return
	}
	let initReply = match lsp.NextServer(live) {
	case option.Some as r:
		r.Value
	case option.None:
		io.Console.Println("ERR init recv")
		return
	}
	if !strings.Contains(initReply, "\"utf-8\"") {
		io.Console.Println("ERR expected utf-8 negotiation got " + initReply)
		return
	}

	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"method\":\"initialized\",\"params\":{}}") catch {
	case _:
		return
	}
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"method\":\"textDocument/didOpen\",\"params\":{\"textDocument\":{\"uri\":\"file:///driver.ergo\",\"languageId\":\"ergo\",\"version\":1,\"text\":\"a😀b\\r\\ncdé\"}}}") catch {
	case _:
		return
	}
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"method\":\"textDocument/didChange\",\"params\":{\"textDocument\":{\"uri\":\"file:///driver.ergo\",\"version\":2},\"contentChanges\":[{\"range\":{\"start\":{\"line\":0,\"character\":5},\"end\":{\"line\":0,\"character\":6}},\"text\":\"!\"}]}}") catch {
	case _:
		return
	}
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"ergo/hoverText\"}") catch {
	case _:
		return
	}
	let hoverReply = match lsp.NextServer(live) {
	case option.Some as r:
		r.Value
	case option.None:
		io.Console.Println("ERR hover recv")
		return
	}
	if !strings.Contains(hoverReply, "a😀!\\r\\ncdé") {
		io.Console.Println("ERR hover text mismatch: " + hoverReply)
		return
	}

	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"ergo/slow\"}") catch {
	case _:
		return
	}
	sync.Await(started)
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"method\":\"$/cancelRequest\",\"params\":{\"id\":4}}") catch {
	case _:
		return
	}
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":98,\"method\":\"ergo/ping\"}") catch {
	case _:
		return
	}
	let pingReply = match lsp.NextServer(live) {
	case option.Some as r:
		r.Value
	case option.None:
		io.Console.Println("ERR ping recv")
		return
	}
	if !strings.Contains(pingReply, "\"pong\"") {
		io.Console.Println("ERR expected ping pong got " + pingReply)
		return
	}
	let _ = sync.Open(release)
	let slowReply = match lsp.NextServer(live) {
	case option.Some as r:
		r.Value
	case option.None:
		io.Console.Println("ERR slow recv")
		return
	}
	if !strings.Contains(slowReply, "-32800") {
		io.Console.Println("ERR expected cancellation -32800 got " + slowReply)
		return
	}

	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"shutdown\"}") catch {
	case _:
		return
	}
	let _ = lsp.NextServer(live)
	lsp.SendClient(live, "{\"jsonrpc\":\"2.0\",\"method\":\"exit\"}") catch {
	case _:
		return
	}
	lsp.CloseClient(live)
	let code = serverTask.Wait()
	if code != 0 {
		io.Console.Println("ERR exit code \(code)")
		return
	}
	io.Console.Println("OK")
}
`

// TestStdLSPNativeEndToEnd builds and runs an end-to-end native Ergo binary
// with ERGO_FALLBACK=off exercising LSP lifecycle, encoding negotiation,
// CRLF/astral overlay edits, concurrent $/cancelRequest, and clean exit.
func TestStdLSPNativeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	modDir := filepath.Join(tmp, "mod")
	srcPath := filepath.Join(modDir, "cmd", "driver", "main.ergo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, []byte(lspNativeDriverSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(tmp, "bin", "driver")
	compile := exec.Command(ergoBin, "build", "./cmd/driver", "-o", exe)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build driver (ERGO_FALLBACK=off): %v\n%s", err, out)
	}
	run := exec.Command(exe)
	out, err := runBound(run)
	if err != nil {
		t.Fatalf("run driver: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("unexpected driver output:\n%s", out)
	}
}

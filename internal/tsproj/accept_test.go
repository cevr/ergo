//go:build linux || darwin

package tsproj

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testwait"
	tstools "ergo.local/prototype/tools/ts"
)

// TestTsProjectionAgrees is the opt-in TS leg. It builds Go because only a
// stock Effect client through a real loader can establish runtime schema and
// HTTP agreement; checking the lowered Go cannot run that peer. One Ergo
// program is built once for the seeded contract; direct, codec and server
// modes share that binary. The CLI is also built once, for the real mapper.
//
// It kills r3-int, r3-case, r3-tag, r3-option, r3-error and r3-loader: the
// old Go-only RPC/codec properties never run the TypeScript projection or
// loaders. The four drift rows guard distinct call-site contract mutations.
// ERGO_TS_SEED replays the generated contract/value family; failure prints
// its Ergo source, both outputs and the replay command.
// E1 also kills e1-optional, e1-nullable and e1-position: no old mapper
// fixture carries std json's absent-key/null forms or Optional as a value.
func TestTsProjectionAgrees(t *testing.T) {
	if os.Getenv("ERGO_TS_TEST") != "1" {
		t.Skip("select the TS leg with ERGO_TS_TEST=1 (requires pinned offline dependencies)")
	}
	if testing.Short() {
		t.Fatal("the TS leg cannot run with -short")
	}
	for _, name := range []string{"bun", "node"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("TS leg requires %s: %v", name, err)
		}
	}
	seed := uint64(1)
	if s := os.Getenv("ERGO_TS_SEED"); s != "" {
		var err error
		seed, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cli := filepath.Join(dir, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(cli), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-p", "2", "-o", cli, "./cmd/ergo")
	cmd.Dir = "../.."
	runCheck(t, cmd)
	for _, entry := range mustReadDir(t, tstools.Sources, ".") {
		b, err := tstools.Sources.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		put(t, dir, entry.Name(), string(b))
	}
	env := append(os.Environ(), "ERGO="+cli, "PATH="+filepath.Dir(cli)+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := func(name string, args ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = dir
		c.Env = env
		return c
	}
	runCheck(t, command("bun", "install", "--frozen-lockfile", "--offline", "--ignore-scripts"))
	if err := os.Symlink(dir, filepath.Join(dir, "node_modules", "ergo-ts")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client.ts", "runtime.mjs", "bundle.mjs"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		put(t, dir, name, string(b))
	}
	contract, provider, main := tsProgram(seed)
	put(t, dir, "ergo.mod", "module bridge\n")
	put(t, dir, "domain/contract.ergo", contract)
	put(t, dir, "shared/types.ergo", "type Label { Text string }\n")
	put(t, dir, "memory/memory.ergo", provider)
	put(t, dir, "cmd/bridge/main.ergo", main)
	put(t, dir, "tsconfig.json", `{"compilerOptions":{"strict":true,"noEmit":true,"target":"es2022","module":"preserve","moduleResolution":"bundler","skipLibCheck":true,"types":[]},"contentMappers":[{"package":"ergo-ts","extensions":[".ergo"]}],"include":["client.ts"]}`)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("seed %d\n--- contract\n%s\n--- provider\n%s\n--- main\n%s\nreplay: ERGO_TS_TEST=1 ERGO_TS_SEED=%d go test -count=1 -timeout 10m -run '^TestTsProjectionAgrees$' ./internal/tsproj", seed, contract, provider, main, seed)
		}
	})
	runCheck(t, command(cli, "gen", "rpc", "domain.API"))
	exe := filepath.Join(dir, "bridge")
	runCheck(t, command(cli, "build", "cmd/bridge", "-o", exe))
	codec := runCheck(t, command(exe, "codec"))
	var values []json.RawMessage
	if err := json.Unmarshal(codec, &values); err != nil {
		t.Fatalf("Go codec output: %v\n%s", err, codec)
	}
	put(t, dir, "values.json", string(codec))
	direct := runCheck(t, command(exe, "direct"))
	var want []json.RawMessage
	want = append(want, values...)
	for _, line := range strings.Split(strings.TrimSpace(string(direct)), "\n") {
		want = append(want, json.RawMessage(line))
	}
	want = append(want, values...)
	t.Run("tsc", func(t *testing.T) {
		runCheck(t, command("bun", "node_modules/typescript/bin/tsc", "--runExternalCode", "--pretty", "false", "-p", "."))
		for _, m := range []struct{ name, from, to, position string }{
			{"field type", "Remaining int", "Remaining string", "client.ts(8,9)"},
			{"failure removed", "fails Limited | Unknown", "fails Unknown", "client.ts(9,66)"},
			{"operation renamed", "Take(key", "Grant(key", "client.ts(7,31)"},
			{"Option dropped", "Peek(key string) Option[Level]", "Peek(key string) Level", "client.ts(11,9)"},
		} {
			t.Run(m.name, func(t *testing.T) {
				put(t, dir, "domain/contract.ergo", strings.Replace(contract, m.from, m.to, 1))
				defer put(t, dir, "domain/contract.ergo", contract)
				out, code := runResult(t, command("bun", "node_modules/typescript/bin/tsc", "--runExternalCode", "--pretty", "false", "-p", "."))
				if code == 0 || !bytes.Contains(out, []byte(m.position)) {
					t.Fatalf("contract mutation did not fail at %s (exit %d):\n%s", m.position, code, out)
				}
			})
		}
		put(t, dir, "domain/contract.ergo", strings.Replace(contract, "Remaining int", "Remaining Missing", 1))
		out, code := runResult(t, command("bun", "node_modules/typescript/bin/tsc", "--runExternalCode", "--pretty", "false", "-p", "."))
		put(t, dir, "domain/contract.ergo", contract)
		if code == 0 || !bytes.Contains(out, []byte("domain/contract.ergo(4,24)")) || !bytes.Contains(out, []byte("ergo1001")) {
			t.Fatalf("Ergo diagnostic not located with source ergo (exit %d):\n%s", code, out)
		}
	})
	// A Vite build must actually load the .ergo module, not externalize it.
	runCheck(t, command("node", "bundle.mjs"))
	for _, loader := range []string{"bun", "node", "vite", "esbuild"} {
		for _, ser := range []string{"json", "ndjson"} {
			t.Run(loader+"/"+ser, func(t *testing.T) {
				srv := command(exe, "serve")
				var stderr bytes.Buffer
				srv.Stderr = &stderr
				out, err := srv.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				startBound(t, srv)
				defer stopOwned(srv)
				line, err := bufio.NewReader(out).ReadString('\n')
				if err != nil {
					t.Fatalf("server readiness: %v %s", err, stderr.String())
				}
				addr, ok := strings.CutPrefix(strings.TrimSpace(line), "listening 127.0.0.1:")
				if !ok {
					t.Fatalf("server is not loopback: %q", line)
				}
				url := "http://127.0.0.1:" + addr + "/rpc"
				var cmd *exec.Cmd
				switch loader {
				case "bun":
					cmd = command("bun", "--preload", "ergo-ts/bun", "runtime.mjs", url, ser, "values.json")
				case "node":
					cmd = command("node", "--import", "ergo-ts/node", "runtime.mjs", url, ser, "values.json")
				case "vite":
					cmd = command("node", "output/client.mjs", url, ser, "values.json")
				case "esbuild":
					cmd = command("node", "output/esbuild.mjs", url, ser, "values.json")
				}
				got := runCheck(t, cmd)
				lines := strings.Split(strings.TrimSpace(string(got)), "\n")
				if len(lines) != len(want) {
					t.Fatalf("different outcome sequence\n--- Go\n%s\n%s\n--- TS\n%s", codec, direct, got)
				}
				for i, line := range lines {
					if canonical(t, []byte(line)) != canonical(t, want[i]) {
						t.Fatalf("outcome %d differs\n--- Go\n%s\n--- TS\n%s\n--- all Go\n%s\n%s\n--- all TS\n%s", i, want[i], line, codec, direct, got)
					}
				}
			})
		}
	}
}

func mustReadDir(t *testing.T, fsys fs.FS, dir string) []fs.DirEntry {
	t.Helper()
	es, err := fs.ReadDir(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}
	return es
}
func put(t *testing.T, dir, name, text string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
func canonical(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatalf("not JSON: %s: %v", b, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// Every external command owns a new group and inherits a kernel memory bound.
// The watchdog only ends hung checks; completion is the process exit event.
func startBound(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return nil
		}
	}
	testwait.WaitDelay(t, cmd)
	if err := proclimit.Start(cmd, proclimit.DefaultLimit); err != nil {
		t.Fatal(err)
	}
}
func stopOwned(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if cmd.ProcessState == nil {
		_ = cmd.Wait()
	}
}
func runResult(t *testing.T, cmd *exec.Cmd) ([]byte, int) {
	t.Helper()
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	startBound(t, cmd)
	err := cmd.Wait()
	// Still own this process group's descendants until this cleanup.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return b.Bytes(), e.ExitCode()
		}
		t.Fatalf("%s: %v\n%s", cmd.Args, err, &b)
	}
	return b.Bytes(), 0
}
func runCheck(t *testing.T, cmd *exec.Cmd) []byte {
	t.Helper()
	b, code := runResult(t, cmd)
	if code != 0 {
		t.Fatalf("%s exit %d\n%s", cmd.Args, code, b)
	}
	return b
}

// tsProgram generates a shape and a sequence of values. Its seed changes the
// number of extra record fields and values, and all branches of the closed
// union and Option occur in every sequence. The direct provider is the oracle.
func tsProgram(seed uint64) (string, string, string) {
	r := rand.New(rand.NewPCG(seed, seed^0x5453))
	var fields, extras strings.Builder
	for i := 0; i < 1+r.IntN(4); i++ {
		fmt.Fprintf(&fields, ", Extra%d string", i)
		fmt.Fprintf(&extras, ", Extra%d: \"seed-%d-%d\"", i, seed, i)
	}
	contract := `// café 🧭: a checked wire contract, not TypeScript source.
import bridge/shared
import json
type Reply { Remaining int }
type Limited { RetryAfterMs int }
type Unknown { Key string }
type Level = Full{} | Partial{Tokens int} | Empty{RetryAfterMs int}
type Bundle { Count int, Name string, Ready bool, Maybe Option[Level], Levels []Level, Labels Map[string, string], Label shared.Label` + fields.String() + ` }
type API {
 Take(key string, cost int) Reply fails Limited | Unknown,
 Peek(key string) Option[Level],
 Reset(),
 Echo(value Bundle) Bundle,
}
type Schema { Value int }
type Draft { Name string, Priority json.Optional[int] }
type View { Ended json.Nullable[string] }
type Box[T] { Value T }
`
	contract = strings.Replace(contract, "Label shared.Label", "Draft Draft, View View, Nested json.Optional[json.Nullable[int]], Raw []json.Optional[int], Box Box[json.Optional[int]], Label shared.Label", 1)
	provider := `import bridge/domain
type Memory {}
func Take(_ Memory, key string, cost int) domain.Reply fails domain.Limited | domain.Unknown {
 if key == "missing" { return domain.Unknown{Key: key} }
 if key == "b" { return domain.Limited{RetryAfterMs: cost * 100} }
 domain.Reply{Remaining: 10 - cost}
}
func Peek(_ Memory, key string) Option[domain.Level] {
 if key == "missing" { return None }
 Some[domain.Level]{Value: domain.Partial{Tokens: 7}}
}
func Reset(_ Memory) {}
func Echo(_ Memory, value domain.Bundle) domain.Bundle { value }
`
	var vals strings.Builder
	for i := 0; i < 6+r.IntN(5); i++ {
		maybe := "None"
		if i%2 == 0 {
			maybe = "Some[domain.Level]{Value: domain.Partial{Tokens: " + fmt.Sprint(r.IntN(100)) + "}}"
		}
		priority, ended, nested := "json.Absent", "json.Null", "json.Absent"
		if i%2 == 0 {
			priority = fmt.Sprintf("json.Present[int]{Value: %d}", r.IntN(100))
			ended = "json.NonNull[string]{Value: \"done\"}"
		}
		if i%3 == 1 {
			nested = "json.Present[json.Nullable[int]]{Value: json.Null}"
		}
		if i%3 == 2 {
			nested = fmt.Sprintf("json.Present[json.Nullable[int]]{Value: json.NonNull[int]{Value: %d}}", r.IntN(100))
		}
		fmt.Fprintf(&vals, "domain.Bundle{Count: %d, Name: \"café 🧭 \\xff\", Ready: %t, Maybe: %s, Levels: []domain.Level{domain.Full, domain.Partial{Tokens: %d}, domain.Empty{RetryAfterMs: 17}}, Labels: labels, Draft: domain.Draft{Name: \"a\", Priority: %s}, View: domain.View{Ended: %s}, Nested: %s, Raw: []json.Optional[int]{json.Absent, json.Present[int]{Value: 7}}, Box: domain.Box[json.Optional[int]]{Value: %s}, Label: shared.Label{Text: \"tag\"}%s},\n", int64(r.Uint64()%100000)-50000, i%2 == 0, maybe, r.IntN(100), priority, ended, nested, priority, extras.String())
	}
	main := `import bridge/domain
import bridge/domain/domainrpc
import bridge/memory
import bridge/shared
import http
import json
import os
import rpc
func enc[T wire](v T) string { json.Encode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, v) }
func values() []domain.Bundle {
 let mut labels = Map[string, string]{}
 labels["x"] = "one"
 labels["café"] = "two"
 []domain.Bundle{
` + vals.String() + ` }
}
func main() fails http.ListenFailed | http.InvalidLimits needs Console | Ambient {
 let host = os.NewHost()
 if os.Args(host)[0] == "serve" {
  let listener = try http.Listen("127.0.0.1:0")
  Console.Println("listening \(listener.Addr())")
  with context { domain.API: memory.Memory } { try rpc.Serve(listener, http.Limits{MaxBodyBytes: 16777216, MaxHeaderBytes: 1048576}, "/rpc", domainrpc.Handle) }
  return
 }
 if os.Args(host)[0] == "codec" { Console.Println(enc(values())); return }
 with context { domain.API: memory.Memory } {
  let reply = domain.API.Take("a", 3) catch { case domain.Limited: domain.Reply{Remaining: -1}; case domain.Unknown: domain.Reply{Remaining: -2} }
  Console.Println(enc(reply))
  domain.API.Take("b", 5) catch { case domain.Limited as e: Console.Println(rpc.Tagged("Limited", enc(e))); case domain.Unknown as e: Console.Println(rpc.Tagged("Unknown", enc(e))); }
  domain.API.Take("missing", 1) catch { case domain.Limited as e: Console.Println(rpc.Tagged("Limited", enc(e))); case domain.Unknown as e: Console.Println(rpc.Tagged("Unknown", enc(e))); }
  match domain.API.Peek("a") { case Some[domain.Level] as s: Console.Println(enc(s.Value)); case None: Console.Println("null") }
  domain.API.Reset()
  Console.Println("null")
 }
}
`
	return contract, provider, main
}

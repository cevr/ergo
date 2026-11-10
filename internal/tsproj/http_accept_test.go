//go:build linux || darwin

package tsproj

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	jobsexample "ergo.local/prototype/examples/jobs"
	"ergo.local/prototype/internal/testwait"
	tstools "ergo.local/prototype/tools/ts"
)

// TestHttpApiClientCallsJobs builds Go because only a stock Effect HTTP client
// can establish that the mapped statuses and codecs agree with the real jobs
// server. The in-process checker cannot run the TypeScript peer. It kills
// e3-table, e3-method, e3-path, e3-payload, e3-status and e3-param: the old RPC
// property has neither an HTTP route table nor status-directed success codecs.
func TestHttpApiClientCallsJobs(t *testing.T) {
	if os.Getenv("ERGO_TS_TEST") != "1" {
		t.Skip("select the TS leg with ERGO_TS_TEST=1")
	}
	dir := t.TempDir()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cli := filepath.Join(dir, "ergo")
	build := exec.CommandContext(ctx, "go", "build", "-p", "2", "-o", cli, "./cmd/ergo")
	build.Dir = "../.."
	runCheck(t, build)
	for _, entry := range mustReadDir(t, tstools.Sources, ".") {
		b, err := tstools.Sources.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		put(t, dir, entry.Name(), string(b))
	}
	err := fs.WalkDir(jobsexample.Sources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".ergo") || path == "ergo.mod") {
			return nil
		}
		b, err := jobsexample.Sources.ReadFile(path)
		if err != nil {
			return err
		}
		put(t, dir, path, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	command := func(name string, args ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "ERGO="+cli, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		return c
	}
	runCheck(t, command("bun", "install", "--frozen-lockfile", "--offline", "--ignore-scripts"))
	if err := os.Symlink(dir, filepath.Join(dir, "node_modules/ergo-ts")); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "tsconfig.json", `{"compilerOptions":{"strict":true,"noEmit":true,"target":"es2022","module":"preserve","moduleResolution":"bundler","skipLibCheck":true,"types":[]},"contentMappers":[{"package":"ergo-ts","extensions":[".ergo"]}],"include":["client.ts"]}`)
	put(t, dir, "client.ts", `import { Effect } from "effect";
import { HttpApiClient } from "effect/http-api";
import { FetchHttpClient } from "effect/http";
import { Routes } from "./api/routes.ergo";
const program = Effect.gen(function*() {
  const client = yield* HttpApiClient.make(Routes, { baseUrl: process.argv[2] });
  const created = yield* client.POST.SubmitJob({ payload: { name: "build" }, responseMode: "decoded-and-response" });
  if (created[1].status !== 201 || created[0].name !== "build" || created[0].priority !== 0 || created[0].ended !== null) throw new Error("submit");
  const job = yield* client.GET.ShowJob({ params: { id: created[0].id } });
  const id: number = job.id;
  const listed = yield* client.GET.ListJobs();
  if (listed.jobs[0].id !== id) throw new Error("list");
  const cancelled = yield* client.POST.CancelJob({ params: { id } });
  if (cancelled.state !== "cancelled" || cancelled.ended === null) throw new Error("cancel");
  const deleted = yield* client.DELETE.DeleteJob({ params: { id }, responseMode: "decoded-and-response" });
  const unit: void = deleted[0];
  if (deleted[1].status !== 204 || unit !== undefined) throw new Error("delete");
  console.log("HTTP client agrees");
});
declare const process: { argv: string[] };
await Effect.runPromise(program.pipe(Effect.provide(FetchHttpClient.layer)));
`)
	tsc := func() *exec.Cmd {
		return command("bun", "node_modules/typescript/bin/tsc", "--runExternalCode", "--pretty", "false", "-p", ".")
	}
	runCheck(t, tsc())
	// A method-only change leaves the operation signature intact. Method
	// groups make even that drift a call-site error, not just a runtime 405.
	routes, err := os.ReadFile(filepath.Join(dir, "api/routes.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(routes), `Value: endpoint.ShowJob, Method: "GET"`, `Value: endpoint.ShowJob, Method: "POST"`, 1)
	if changed == string(routes) {
		t.Fatal("ShowJob route not found")
	}
	put(t, dir, "api/routes.ergo", changed)
	out, code := runResult(t, tsc())
	put(t, dir, "api/routes.ergo", string(routes))
	if code == 0 || !bytes.Contains(out, []byte("client.ts(9,")) {
		t.Fatalf("method drift did not fail at the call site (exit %d):\n%s", code, out)
	}
	// Rename the path parameter and its operation parameter together. The
	// contract remains checked; the stale client must fail at its call site.
	api := filepath.Join(dir, "api")
	old := map[string]string{}
	for _, e := range mustReadDir(t, os.DirFS(api), ".") {
		if !strings.HasSuffix(e.Name(), ".ergo") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(api, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		old[e.Name()] = string(b)
		put(t, api, e.Name(), strings.ReplaceAll(string(b), "id", "key"))
	}
	out, code = runResult(t, tsc())
	for name, text := range old {
		put(t, api, name, text)
	}
	if code == 0 || !bytes.Contains(out, []byte("client.ts(9,")) {
		t.Fatalf("path-parameter drift did not fail at the call site (exit %d):\n%s", code, out)
	}
	exe := filepath.Join(dir, "jobs-server")
	runCheck(t, command(cli, "build", "cmd/jobs", "-o", exe))
	srv := command(exe, "-addr", "127.0.0.1:0")
	var stderr bytes.Buffer
	srv.Stderr = &stderr
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	startBound(t, srv)
	defer stopOwned(srv)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("server readiness: %v %s", err, stderr.String())
	}
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "jobs: listening on 127.0.0.1:")
	if !ok {
		t.Fatalf("not loopback: %q", line)
	}
	out = runCheck(t, command("bun", "--preload", "ergo-ts/bun", "client.ts", "http://127.0.0.1:"+addr))
	if string(out) != "HTTP client agrees\n" {
		t.Fatalf("client: %s", out)
	}
}

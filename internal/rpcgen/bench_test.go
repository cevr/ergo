package rpcgen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// The module of BenchmarkRpcUnary: a limiter contract, a provider whose
// Peek answers a fixed level, and a main that serves it.
var benchModule = map[string]string{
	"ergo.mod": "module bench\n",
	"limits/limits.ergo": `// Package limits is a rate limiter's contract.

type Grant { Remaining int }
type Limited { RetryAfterMs int }
type UnknownBucket { Key string }
type Level = Full{} | Partial{Tokens int} | Empty{RetryAfterMs int}

type Limiter {
	Take(key string, cost int) Grant fails Limited | UnknownBucket,
	Peek(key string) Option[Level],
}
`,
	"memory/memory.ergo": `// Package memory is a fixed limits.Limiter.

import bench/limits

type Fixed {}

func Take(f Fixed, key string, cost int) limits.Grant fails limits.Limited | limits.UnknownBucket {
	if key != "a" {
		return limits.UnknownBucket{Key: key}
	}
	limits.Grant{Remaining: 10 - cost}
}

func Peek(f Fixed, key string) Option[limits.Level] {
	if key != "a" {
		return None
	}
	Some[limits.Level]{Value: limits.Partial{Tokens: 7}}
}
`,
	"cmd/bench/main.ergo": `import http
import rpc
import bench/limits
import bench/limits/limitsrpc
import bench/memory

func serve(l http.Listener) fails http.InvalidLimits needs Scheduler {
	with context { limits.Limiter: memory.Fixed } {
		try rpc.Serve(l, http.Limits{MaxBodyBytes: 16777216, MaxHeaderBytes: 1048576, IdleTimeout: 5s, ReadHeaderTimeout: 10s, ReadBodyTimeout: 30s}, "/rpc", limitsrpc.Handle)
	}
}

func main() fails http.ListenFailed | http.InvalidLimits needs Console | Ambient {
	let l = try http.Listen("127.0.0.1:0")
	Console.Println("listening \(l.Addr())")
	with context { Scheduler: NewLiveScheduler() } { try serve(l) }
}
`,
}

// rpcBenchGo is the Go benchmark file beside the emitted main package:
// one unary call, Peek("a"), posted in process (net.Pipe connections, no
// sockets) to the Ergo server and to a hand Go server that decodes the
// envelope and the payload and encodes the Exit with encoding/json. Both
// answer the same bytes, which the file checks first.
const rpcBenchGo = `package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"

	rt "ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

type pipeListener struct{ conns chan net.Conn; done chan struct{} }

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { select { case <-l.done: default: close(l.done) }; return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func pipeClient(l *pipeListener) *http.Client {
	return &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 4,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, s := net.Pipe()
			l.conns <- s
			return c, nil
		},
	}}
}

const body = ` + "`" + `{"_tag":"Request","id":0,"tag":"Peek","payload":{"key":"a"},"headers":[]}` + "`" + `
const want = ` + "`" + `[{"_tag":"Exit","requestId":0,"exit":{"_tag":"Success","value":{"_tag":"Some","value":{"_tag":"Partial","tokens":7}}}}]` + "`" + `

// The hand server.

type handRequest struct {
	Tag     string          ` + "`json:\"_tag\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	RPC     string          ` + "`json:\"tag\"`" + `
	Payload json.RawMessage ` + "`json:\"payload\"`" + `
}

type handPeek struct {
	Key string ` + "`json:\"key\"`" + `
}

type handPartial struct {
	Tag    string ` + "`json:\"_tag\"`" + `
	Tokens int    ` + "`json:\"tokens\"`" + `
}

type handSome struct {
	Tag   string      ` + "`json:\"_tag\"`" + `
	Value handPartial ` + "`json:\"value\"`" + `
}

type handSuccess struct {
	Tag   string   ` + "`json:\"_tag\"`" + `
	Value handSome ` + "`json:\"value\"`" + `
}

type handExit struct {
	Tag       string          ` + "`json:\"_tag\"`" + `
	RequestID json.RawMessage ` + "`json:\"requestId\"`" + `
	Exit      handSuccess     ` + "`json:\"exit\"`" + `
}

func handPeekOf(key string) handSome {
	return handSome{Tag: "Some", Value: handPartial{Tag: "Partial", Tokens: 7}}
}

func handHandler(w http.ResponseWriter, r *http.Request) {
	var req handRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RPC != "Peek" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var p handPeek
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	out, _ := json.Marshal([]handExit{{Tag: "Exit", RequestID: req.ID, Exit: handSuccess{Tag: "Success", Value: handPeekOf(p.Key)}}})
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func post(b *testing.B, c *http.Client) []byte {
	resp, err := c.Post("http://pipe/rpc/", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		b.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return out
}

func run(b *testing.B, l *pipeListener) {
	c := pipeClient(l)
	if got := post(b, c); string(got) != want {
		b.Fatalf("the answer %s, want %s", got, want)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		post(b, c)
	}
	b.StopTimer()
}

func BenchmarkErgoUnary(b *testing.B) {
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	task := rt.Root("bench")
	ended := make(chan any, 1)
	go func() {
		defer func() { ended <- recover() }()
		` + "SERVE" + `
	}()
	run(b, l)
	task.Interrupt()
	<-ended
}

func BenchmarkGoUnary(b *testing.B) {
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{Handler: http.HandlerFunc(handHandler)}
	go srv.Serve(l)
	run(b, l)
	srv.Close()
}
`

// BenchmarkRpcUnary is R1's speed receipt: one unary call (Peek, an
// Option of a union back) served by the generated Handle on rpc.Serve,
// against a hand Go net/http handler with encoding/json, both in process
// over the same pipe transport (ns/op is per call, client included).
// Interleave the runs (-count, the sub-benchmarks alternating) on a quiet
// host for a ratio; one run on a loaded host is no claim.
//
// It builds Go because the generated server runs only in an emitted,
// linked module: the benchmark file sits beside the emitted main package.
func BenchmarkRpcUnary(b *testing.B) {
	dir := b.TempDir()
	for name, src := range benchModule {
		writeFile(b, filepath.Join(dir, name), src)
	}
	a, err := sketchfront.CheckDir(filepath.Join(dir, "limits"))
	if err != nil || a.Root().Package == nil {
		b.Fatalf("the contract does not check: %v %v", err, a.Diagnostics)
	}
	out, err := Generate(a.Root().Package, "bench/limits", "Limiter")
	if err != nil {
		b.Fatal(err)
	}
	for name, src := range out.Files {
		writeFile(b, filepath.Join(dir, "limits", out.Dir, name), src)
	}
	main := filepath.Join(dir, "cmd", "bench")
	a, err = sketchfront.CheckDir(main)
	if err != nil || len(a.Diagnostics) > 0 {
		b.Fatalf("the program does not check: %v %v", err, a.Diagnostics)
	}
	var stderr bytes.Buffer
	mod, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    main,
		Output:    filepath.Join(dir, "bench.exe"),
		Cache:     build.Cache{Root: filepath.Join(dir, "cache")},
		Toolchain: build.HostToolchain(),
		Stderr:    &stderr,
	})
	if err != nil {
		b.Fatalf("build: %v\n%s", err, stderr.String())
	}
	// The emitted module, copied out of the cache, with the benchmark
	// file beside its main package.
	root := filepath.Join(dir, "benchmod")
	if err := os.CopyFS(root, os.DirFS(mod.Dir)); err != nil {
		b.Fatal(err)
	}
	mainGo, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		b.Fatal(err)
	}
	serve := "serve(&Env{task: task}, v1.ListenerOn(l))"
	if !bytes.Contains(mainGo, []byte("func serve(env *Env, l *ergo.Listener)")) {
		b.Fatalf("the emitted serve is not serve(env, l):\n%s", mainGo)
	}
	writeFile(b, filepath.Join(root, "rpc_bench_test.go"), strings.Replace(rpcBenchGo, "SERVE", serve, 1))
	exe := filepath.Join(dir, "bench.test")
	cmd := exec.Command("go", "test", "-c", "-trimpath", "-o", exe, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("build the benchmark: %v\n%s", err, out)
	}
	line := regexp.MustCompile(`([0-9.]+) ns/op\s+([0-9.]+) B/op\s+([0-9.]+) allocs/op`)
	for _, bench := range []string{"ErgoUnary", "GoUnary"} {
		b.Run(bench, func(b *testing.B) {
			b.ReportAllocs()
			run := exec.Command(exe, "-test.run", "^$", "-test.bench", "^Benchmark"+bench+"$", "-test.benchmem", "-test.benchtime", strconv.Itoa(b.N)+"x", "-test.count", "1")
			var out bytes.Buffer
			run.Stdout, run.Stderr = &out, &out
			if err := proclimit.Run(run, limit(b)); err != nil {
				b.Fatalf("%s: %v\n%s", bench, err, out.String())
			}
			m := line.FindStringSubmatch(out.String())
			if m == nil {
				b.Fatalf("%s: no result line in\n%s", bench, out.String())
			}
			for i, unit := range []string{"ns/op", "B/op", "allocs/op"} {
				v, err := strconv.ParseFloat(m[i+1], 64)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(v, unit)
			}
		})
	}
}

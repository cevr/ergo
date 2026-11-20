package eval_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

// TestNetSendCarriesRawPath is the smallest repro of the evaluator's jobs
// panic (fallout b25/flip-g4, "interface conversion: eval.Value is string,
// not eval.sliceV" at nativesnet.go fieldsOf): HTTP-RAWPATH (0f56de58d)
// gave std http's send extern a rawPath parameter after path, and its
// serve callback one too, and the natives still read the old positions.
// A request whose RawPath encodes its Path ("/a%2Fb" for "/a/b") reaches
// the handler with both, as runtime/v1's HTTPDo and HTTPServe give them
// (Go sends RawPath when it encodes Path). Mutants net-send-rawpath-unread
// and net-serve-rawpath-dropped.
func TestNetSendCarriesRawPath(t *testing.T) {
	const src = `import http
import io
import os
import task

let limits = http.Limits{MaxBodyBytes: 1 << 10, MaxHeaderBytes: 1 << 16, IdleTimeout: 2m, ReadHeaderTimeout: 10s, ReadBodyTimeout: 30s}

func main() fails os.Exit needs Ambient {
	with context { io.Stdout: io.NewHost() } {
		let l = http.Listen("127.0.0.1:0") catch {
		case http.ListenFailed:
			return os.Exit{Code: 1, Message: "no listener"}
		}
		let client = http.NewClient("http://"+l.Addr(), limits) catch {
		case http.InvalidLimits, http.InvalidClient:
			return os.Exit{Code: 1, Message: "no client"}
		}
		with context { Scheduler: NewLiveScheduler() } {
			let server = task.Go() {
				http.Serve(l, limits, func(r http.Request) http.Response needs http.Streaming {
					http.Response{Status: 200, Header: []http.Field{}, Body: r.Path + " " + r.RawPath}
				}) catch {
				case http.InvalidLimits:
				}
			}
			with context { http.Net: client } {
				let resp = http.Do(http.Request{Method: "GET", Path: "/a/b", RawPath: "/a%2Fb", Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}}) catch {
				case http.RequestFailed:
					return os.Exit{Code: 1, Message: "the request failed"}
				}
				io.Stdout.Println(resp.Body)
			}
			server.Stop()
		}
	}
}
`
	a := sketchfront.Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatalf("the program does not check: %v", a.Diagnostics)
	}
	pkgs := a.Checked()
	out, err := eval.RunMain(pkgs[len(pkgs)-1], mainBudget, eval.Process{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 0 || out.Stdout != "/a/b /a%2Fb\n" {
		t.Fatalf("exit %d, stdout %q, want \"/a/b /a%%2Fb\\n\"; stderr %q (%s%s)", out.Status, out.Stdout, out.Stderr, out.Skipped, out.Incomplete)
	}
}

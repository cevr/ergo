package rpcgen

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// heldMain serves a "Hold" call that waits until an "Open" call lets it
// go, and an "Echo" call that answers at once.
const heldMain = `import http
import queue
import rpc

func answer(gate queue.Queue[bool], tag string, payload string) string fails rpc.Failed {
	if tag == "Hold" {
		let _ = gate.Pop() catch {
		case queue.Closed:
			false
		}
		return "null"
	}
	if tag == "Open" {
		gate.Push(true) catch {
		case queue.Closed:
		}
		return "null"
	}
	payload
}

func main() fails http.ListenFailed | http.InvalidLimits needs Console | Ambient {
	let l = try http.Listen("127.0.0.1:0")
	Console.Println("listening \(l.Addr())")
	let gate = queue.New[bool](1)
	try rpc.Serve(l, http.Limits{MaxBodyBytes: 65536, MaxHeaderBytes: 16384}, "/rpc", func(tag string, payload string) string fails rpc.Failed {
		try answer(gate, tag, payload)
	})
}
`

// TestRpcFramedExitFlowsWhileHeld is rpc.md rule 2 through the whole
// server (rpc.Answer, http.Stream, the transport's Exchange): in one
// ndjson batch, the Exit of a call that ends is on the wire while another
// call of the batch is still held. The client reads the Echo's line
// first; only then does it post the Open that lets the Hold end, so a
// server that buffers the batch never answers and the deadline names it
// (rpc-answer-buffers, rpc-lines-at-end). It waits on events, not time.
//
// It builds Go because the rule is about a socket: the flush of each
// line by the served program's transport, which go/types on the lowered
// Go cannot observe.
func TestRpcFramedExitFlowsWhileHeld(t *testing.T) {
	needsBuild(t)
	dir := t.TempDir()
	files := map[string]string{"ergo.mod": "module loop\n", "cmd/loop/main.ergo": heldMain}
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	exe := buildProgram(t, 0, dir, files)
	srv := startServer(t, exe)
	defer srv.stop(t)
	url := "http://" + srv.addr + "/rpc/"
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	batch := `{"_tag":"Request","id":0,"tag":"Hold","payload":{},"headers":[]}` + "\n" +
		`{"_tag":"Request","id":1,"tag":"Echo","payload":{"n":1},"headers":[]}` + "\n"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(batch))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || ct != "application/x-ndjson" {
		t.Fatalf("the batch's answer: %d %q", resp.StatusCode, ct)
	}
	lines := bufio.NewReader(resp.Body)
	first, err := lines.ReadString('\n')
	if want := `{"_tag":"Exit","requestId":1,"exit":{"_tag":"Success","value":{"n":1}}}` + "\n"; err != nil || first != want {
		t.Fatalf("the first line while Hold is held: %q %v, want %q\n%s", first, err, want, srv.stderr.String())
	}
	open, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"_tag":"Request","id":2,"tag":"Open","payload":{},"headers":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	open.Header.Set("Content-Type", "application/json")
	opened, err := http.DefaultClient.Do(open)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, opened.Body)
	opened.Body.Close()
	rest, err := io.ReadAll(lines)
	if want := `{"_tag":"Exit","requestId":0,"exit":{"_tag":"Success","value":null}}` + "\n"; err != nil || string(rest) != want {
		t.Fatalf("the rest after Open: %q %v, want %q", rest, err, want)
	}
}

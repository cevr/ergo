package v1

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// rpcHarness serves RPCServe on a root task with a Go handle, as the
// generated Handle would be, on a loopback port.
type rpcHarness struct {
	url     string
	task    *rt.Task
	done    chan any
	started chan struct{} // a "Wait" call has started
	release chan struct{} // closed when a "Wait" call's release ran
}

func startRPC(t *testing.T) *rpcHarness {
	t.Helper()
	l, err := RPCListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &rpcHarness{url: "http://" + l.Addr() + "/rpc", task: rt.Root("rpc test"), done: make(chan any, 1), started: make(chan struct{}, 1), release: make(chan struct{})}
	handle := func(ct *rt.Task, tag, payload string) (string, error) {
		switch tag {
		case "Echo":
			return payload, nil
		case "Unit":
			return "null", nil
		case "Fail":
			return "", RPCFailed{Text: `{"_tag":"Nope","n":1}`}
		case "Die":
			panic(rpcDefect{Name: "Boom", Message: "it broke"})
		case "Wait":
			// A call that runs until its task is stopped; its release
			// closes h.release, as an Ergo frame's exit action would.
			defer close(h.release)
			h.started <- struct{}{}
			<-ct.Done()
			panic(rt.Interrupted)
		}
		panic(rpcDefect{Name: "RpcError", Message: "no operation " + tag})
	}
	go func() {
		defer func() { h.done <- recover() }()
		RPCServe("rpc test", h.task, l, "/rpc", handle)
	}()
	t.Cleanup(func() {
		h.task.Interrupt()
		select {
		case p := <-h.done:
			if p != rt.Interrupted {
				t.Errorf("RPCServe ended with %v, want the interruption", p)
			}
		case <-testwait.After(t):
			t.Error("RPCServe did not end after its task was stopped")
		}
	})
	return h
}

func (h *rpcHarness) post(t *testing.T, path, contentType, body string) (string, string) {
	t.Helper()
	resp, err := http.Post(strings.TrimSuffix(h.url, "/rpc")+path, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %s %s", path, resp.Status, b)
	}
	return string(b), resp.Header.Get("Content-Type")
}

// TestRPCWireFacts holds the facts of Effect RPC 4.1.2's HTTP protocol
// that a server must keep for the stock RpcClient (effect/src/rpc/
// RpcMessage.ts, RpcSerialization.ts; R1 plan §2.1): the wire shape is
// the spec, so each row states one fact as text. Mutants it kills, which
// the loopback property (internal/rpcgen) cannot see because the Ergo
// client reads its own server leniently: rpc-m1 Success without value,
// rpc-m2 requestId re-typed, rpc-m3 a failure sent as Die, rpc-m4 the
// trailing-slash path refused, rpc-m5 a batch answered by position.
func TestRPCWireFacts(t *testing.T) {
	h := startRPC(t)
	req := func(id, tag, payload string) string {
		return `{"_tag":"Request","id":` + id + `,"tag":"` + tag + `","payload":` + payload + `,"headers":[]}`
	}
	rows := []struct {
		name, path, ct, body string
		want                 []string
	}{
		// RpcClient posts to its URL with a trailing slash
		// (RpcClient.makeProtocolHttp): both paths answer.
		{"trailing slash", "/rpc/", rpcJSON, req("0", "Echo", `{"a":1}`), []string{`[{"_tag":"Exit","requestId":0,"exit":{"_tag":"Success","value":{"a":1}}}]`}},
		{"mount", "/rpc", rpcJSON, req("1", "Echo", `{}`), []string{`"requestId":1,`}},
		// ResponseExitEncoded.requestId is string | number: the token
		// comes back as it was sent.
		{"string id", "/rpc/", rpcJSON, req(`"7"`, "Echo", `{}`), []string{`"requestId":"7"`}},
		// ExitEncoded Success has value, null for none (Schema.Void).
		{"unit value", "/rpc/", rpcJSON, req("2", "Unit", `{}`), []string{`"exit":{"_tag":"Success","value":null}`}},
		// A typed failure is a Fail cause whose error is the tagged object.
		{"fail", "/rpc/", rpcJSON, req("3", "Fail", `{}`), []string{`"exit":{"_tag":"Failure","cause":[{"_tag":"Fail","error":{"_tag":"Nope","n":1}}]}`}},
		// A defect is a Die cause with {name, message} (Schema.Defect).
		{"die", "/rpc/", rpcJSON, req("4", "Die", `{}`), []string{`"cause":[{"_tag":"Die","defect":{"name":"Boom","message":"it broke"}}]`}},
		// json: an array of envelopes is answered by id, whatever the
		// order the calls end in.
		{"batch", "/rpc/", rpcJSON, "[" + req("10", "Echo", `{"x":10}`) + "," + req(`"b"`, "Echo", `{"x":"b"}`) + "]", []string{`"requestId":10,"exit":{"_tag":"Success","value":{"x":10}}`, `"requestId":"b","exit":{"_tag":"Success","value":{"x":"b"}}`}},
		// ndjson: one Exit line per Request line (RpcSerialization ndjson).
		{"ndjson", "/rpc/", rpcNDJSON, req("5", "Echo", `{"n":5}`) + "\n" + req("6", "Unit", `{}`) + "\n", []string{"{\"_tag\":\"Exit\",\"requestId\":5,\"exit\":{\"_tag\":\"Success\",\"value\":{\"n\":5}}}\n", "{\"_tag\":\"Exit\",\"requestId\":6,\"exit\":{\"_tag\":\"Success\",\"value\":null}}\n"}},
	}
	for _, r := range rows {
		got, ct := h.post(t, r.path, r.ct, r.body)
		if ct != r.ct {
			t.Errorf("%s: answered with Content-Type %q, want %q", r.name, ct, r.ct)
		}
		for _, w := range r.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: answer %s\nlacks %s", r.name, got, w)
			}
		}
	}
	// The client side of the same facts: a value, a Fail, a Die.
	task := rt.Root("rpc client")
	if v, err := RPCCall(task, h.url, "Echo", `{"k":"v"}`); err != nil || v != `{"k":"v"}` {
		t.Errorf("Call Echo = %q, %v", v, err)
	}
	if _, err := RPCCall(task, h.url, "Fail", `{}`); err != (RPCFailed{Text: `{"_tag":"Nope","n":1}`}) {
		t.Errorf("Call Fail = %v, want Failed with the tagged error", err)
	}
	func() {
		defer func() {
			if p := recover(); p != (rpcDefect{Name: "Boom", Message: "it broke"}) {
				t.Errorf("Call Die raised %v, want the server's defect", p)
			}
		}()
		_, _ = RPCCall(task, h.url, "Die", `{}`)
	}()
}

// TestRPCAbortStopsTheCall is R1's interruption fact, both ways: a client
// that abandons its request (an aborted fetch) stops the call's task, and
// the call's release runs; an Ergo caller whose task is stopped aborts
// its request, with the same end on the server. It waits on events: the
// call's start and its release (rpc-m6: a dispatch that ignores the
// request's context never releases, and the test's deadline names it).
func TestRPCAbortStopsTheCall(t *testing.T) {
	h := startRPC(t)
	ctx, cancel := context.WithCancel(context.Background())
	body := `{"_tag":"Request","id":0,"tag":"Wait","payload":{},"headers":[]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", rpcNDJSON)
	sent := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		sent <- err
	}()
	select {
	case <-h.started:
	case <-testwait.After(t):
		t.Fatal("the call did not start")
	}
	cancel()
	select {
	case <-h.release:
	case <-testwait.After(t):
		t.Fatal("the aborted request's call was not stopped and released")
	}
	<-sent

	// The Ergo caller's stop.
	h.release = make(chan struct{})
	caller := rt.Root("rpc caller")
	ended := make(chan any, 1)
	go func() {
		defer func() { ended <- recover() }()
		_, _ = RPCCall(caller, h.url, "Wait", `{}`)
	}()
	select {
	case <-h.started:
	case <-testwait.After(t):
		t.Fatal("the second call did not start")
	}
	caller.Interrupt()
	select {
	case p := <-ended:
		if p != rt.Interrupted {
			t.Errorf("the stopped caller's Call ended with %v, want the interruption", p)
		}
	case <-testwait.After(t):
		t.Fatal("the stopped caller's Call did not end")
	}
	select {
	case <-h.release:
	case <-testwait.After(t):
		t.Fatal("the stopped caller's call was not stopped on the server")
	}
}

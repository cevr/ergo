package v1

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// rpcHarness serves the rpc bodies on HTTPServe with a Go handle, as std
// rpc's Answer does with the generated Handle: RPCParse, then RPCExits for
// json or RPCExitLines through the request's Exchange for ndjson. Answer's
// routing (mount, method, refusals) is Ergo's; TestRpcLoopbackAgrees and
// TestRpcFramedExitFlowsWhileHeld (internal/rpcgen) run it.
type rpcHarness struct {
	url     string
	started chan struct{} // a "Wait" call has started
	release chan struct{} // closed when a "Wait" call's release ran
}

func rpcHandle(h *rpcHarness) func(*rt.Task, string, string) (string, error) {
	return func(ct *rt.Task, tag, payload string) (string, error) {
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
}

func startRPC(t *testing.T) *rpcHarness {
	t.Helper()
	h := &rpcHarness{started: make(chan struct{}, 1), release: make(chan struct{})}
	handle := rpcHandle(h)
	l, _ := startHTTPLimits(t, serveLimits(1<<20, 4096), func(ct *rt.Task, x *Exchange, _, _ string, _, header []HTTPField, body string, _ bool, _ string) HTTPResponse {
		framed := false
		for _, f := range header {
			if strings.EqualFold(f.Name, "Content-Type") && strings.HasPrefix(f.Value, rpcNDJSON) {
				framed = true
			}
		}
		envs, err := RPCParse(body, framed)
		if err != nil {
			return HTTPResponse{Status: 400, Body: err.Error()}
		}
		if !framed {
			return HTTPResponse{Status: 200, Header: []HTTPField{{"Content-Type", rpcJSON}}, Body: RPCExits("rpc test", ct, envs, handle)}
		}
		x.Begin(200, []HTTPField{{"Content-Type", rpcNDJSON}})
		_ = RPCExitLines("rpc test", ct, envs, handle, func(t *rt.Task, line string) error { return x.Write(t, line) })
		x.End()
		return HTTPResponse{Status: 200}
	})
	h.url = "http://" + l.Addr() + "/rpc"
	return h
}

func (h *rpcHarness) post(t *testing.T, contentType, body string) (string, string) {
	t.Helper()
	resp, err := http.Post(h.url+"/", contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST: %s %s", resp.Status, b)
	}
	return string(b), resp.Header.Get("Content-Type")
}

// TestRPCWireFacts holds the facts of Effect RPC 4.1.2's HTTP protocol
// that a server must keep for the stock RpcClient (effect/src/rpc/
// RpcMessage.ts, RpcSerialization.ts; R1 plan §2.1): the wire shape is
// the spec, so each row states one fact as text. Mutants it kills, which
// the loopback property (internal/rpcgen) cannot see because the Ergo
// client reads its own server leniently: rpc-m1 Success without value,
// rpc-m2 requestId re-typed, rpc-m3 a failure sent as Die, rpc-m5 a batch
// answered by position. (rpc-m4, the trailing-slash path, is Answer's,
// in Ergo: every Ergo Call posts to it.)
func TestRPCWireFacts(t *testing.T) {
	h := startRPC(t)
	req := func(id, tag, payload string) string {
		return `{"_tag":"Request","id":` + id + `,"tag":"` + tag + `","payload":` + payload + `,"headers":[]}`
	}
	rows := []struct {
		name, ct, body string
		want           []string
	}{
		{"echo", rpcJSON, req("0", "Echo", `{"a":1}`), []string{`[{"_tag":"Exit","requestId":0,"exit":{"_tag":"Success","value":{"a":1}}}]`}},
		// ResponseExitEncoded.requestId is string | number: the token
		// comes back as it was sent.
		{"string id", rpcJSON, req(`"7"`, "Echo", `{}`), []string{`"requestId":"7"`}},
		// ExitEncoded Success has value, null for none (Schema.Void).
		{"unit value", rpcJSON, req("2", "Unit", `{}`), []string{`"exit":{"_tag":"Success","value":null}`}},
		// A typed failure is a Fail cause whose error is the tagged object.
		{"fail", rpcJSON, req("3", "Fail", `{}`), []string{`"exit":{"_tag":"Failure","cause":[{"_tag":"Fail","error":{"_tag":"Nope","n":1}}]}`}},
		// A defect is a Die cause with {name, message} (Schema.Defect).
		{"die", rpcJSON, req("4", "Die", `{}`), []string{`"cause":[{"_tag":"Die","defect":{"name":"Boom","message":"it broke"}}]`}},
		// json: an array of envelopes is answered by id, whatever the
		// order the calls end in.
		{"batch", rpcJSON, "[" + req("10", "Echo", `{"x":10}`) + "," + req(`"b"`, "Echo", `{"x":"b"}`) + "]", []string{`"requestId":10,"exit":{"_tag":"Success","value":{"x":10}}`, `"requestId":"b","exit":{"_tag":"Success","value":{"x":"b"}}`}},
		// ndjson: one Exit line per Request line (RpcSerialization ndjson).
		{"ndjson", rpcNDJSON, req("5", "Echo", `{"n":5}`) + "\n" + req("6", "Unit", `{}`) + "\n", []string{"{\"_tag\":\"Exit\",\"requestId\":5,\"exit\":{\"_tag\":\"Success\",\"value\":{\"n\":5}}}\n", "{\"_tag\":\"Exit\",\"requestId\":6,\"exit\":{\"_tag\":\"Success\",\"value\":null}}\n"}},
	}
	for _, r := range rows {
		got, ct := h.post(t, r.ct, r.body)
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
	// A Die the server's rpc layer raises for a call it cannot read
	// (RpcError) is the wire's failure, Transport, not the server's defect
	// (R-chk mutant rchk-m1: no other test sends a call a server refuses).
	if _, err := RPCCall(task, h.url, "Nope", `{}`); err == nil || err.Error() != (RPCTransport{Reason: "the server refused the call: no operation Nope"}).Error() {
		t.Errorf("Call Nope = %v, want Transport", err)
	}
}

// TestRPCAbortStopsTheCall is R1's interruption fact, both ways: a client
// that abandons its request (an aborted fetch) stops the call's task, and
// the call's release runs; an Ergo caller whose task is stopped aborts
// its request, with the same end on the server. It waits on events: the
// call's start and its release (rpc-m6: a dispatch that ignores the
// request's stop never releases, and the test's deadline names it).
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

// TestRPCCallsStopWithTheCaller is rpc.Loopback's interruption fact: a
// stop of the task that runs the calls (a Loopback caller's, or a served
// request's) stops each call's task, and RPCExits ends with the stop
// after the call's release ran. It catches rpc-calls-stop (a stopped
// caller's Call that ends with a defect read from an Interrupt exit). The
// limiter's laws over a Loopback hold its success and failure paths; no
// law stops a caller. It waits on events.
func TestRPCCallsStopWithTheCaller(t *testing.T) {
	started := make(chan struct{}, 1)
	var released atomic.Bool
	handle := func(ct *rt.Task, tag, payload string) (string, error) {
		defer released.Store(true)
		started <- struct{}{}
		<-ct.Done()
		panic(rt.Interrupted)
	}
	caller := rt.Root("rpc loopback caller")
	ended := make(chan any, 1)
	go func() {
		defer func() { ended <- recover() }()
		envs, _ := RPCParse(RPCRequest("0", "Wait", `{}`), false)
		_ = RPCExits("rpc loopback test", caller, envs, handle)
	}()
	select {
	case <-started:
	case <-testwait.After(t):
		t.Fatal("the loopback call did not start")
	}
	caller.Interrupt()
	select {
	case p := <-ended:
		if p != rt.Interrupted {
			t.Errorf("the stopped caller's RPCExits ended with %v, want the interruption", p)
		}
		if !released.Load() {
			t.Error("RPCExits returned before the call's release ran")
		}
	case <-testwait.After(t):
		t.Fatal("the stopped caller's loopback call did not end")
	}
}

// TestRPCFailedEmitStopsTheCallsLeft is rpc.md rule 1's "no call outlives
// its request" on the path where the request is not stopped: an ndjson
// line that cannot be written (a WriteFailed of the stream) ends
// RPCExitLines with that failure, and the calls of the batch still held
// are stopped and their releases have run before it returns. Only this
// path leaves the request's task running, so only it catches
// rpc-calls-join (no join of the calls left): a held call that nothing
// stops would never release. It waits on events.
func TestRPCFailedEmitStopsTheCallsLeft(t *testing.T) {
	held := make(chan struct{})
	var released atomic.Bool
	handle := func(ct *rt.Task, tag, payload string) (string, error) {
		if tag == "Hold" {
			defer released.Store(true)
			close(held)
			<-ct.Done()
			panic(rt.Interrupted)
		}
		<-held // the Hold is running before the Echo's line is emitted
		return payload, nil
	}
	body := RPCRequest("0", "Hold", `{}`) + "\n" + RPCRequest("1", "Echo", `{}`) + "\n"
	envs, err := RPCParse(body, true)
	if err != nil {
		t.Fatal(err)
	}
	refused := HTTPWriteFailed{Reason: "the client went away"}
	request := rt.Root("rpc request")
	done := make(chan error, 1)
	go func() {
		done <- RPCExitLines("rpc emit test", request, envs, handle, func(*rt.Task, string) error { return refused })
	}()
	select {
	case err := <-done:
		if err != refused {
			t.Errorf("RPCExitLines ended with %v, want the emit's failure", err)
		}
		if !released.Load() {
			t.Error("RPCExitLines returned while a call of its batch was still held")
		}
	case <-testwait.After(t):
		t.Fatal("RPCExitLines did not end after its emit failed")
	}
}

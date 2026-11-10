package v1

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of std rpc (docs/spec/rpc.md; R1): Effect RPC 4.1.2's HTTP
// protocol with its json and ndjson serializations (effect/src/rpc/
// RpcMessage.ts, RpcSerialization.ts), served and called over net/http.
// The Ergo side writes and reads the payloads, values and failures with
// std json's derived codecs; this file moves the envelopes.
//
// Wire facts, each the shape RpcMessage.ts gives (the wire is the spec):
//   - a client posts to its URL with a trailing slash; Serve answers the
//     mount path and the mount path with a slash;
//   - a json body is one envelope or an array of them, and the answer is
//     an array; an ndjson body is one envelope per line, and the answer is
//     one line per Exit, written and flushed as each call ends;
//   - an Exit echoes the Request's id token as it was written (a number
//     stays a number, a string a string);
//   - Success always carries value (null for no value);
//   - a typed failure is a Fail cause whose error is the failure's tagged
//     object, a defect a Die cause with {name, message}, an interruption
//     an Interrupt cause.

// RPCFailed is rpc.Failed: a typed failure as its JSON text, a
// {"_tag": ...} object.
type RPCFailed struct {
	Text string
}

// Error is the failure's text.
func (e RPCFailed) Error() string { return "rpc.Failed{Text:" + e.Text + "}" }

// RPCListenFailed is rpc.ListenFailed: Listen could not bind Addr.
type RPCListenFailed struct {
	Addr   string
	Reason string
}

// Error is the failure's text.
func (e RPCListenFailed) Error() string {
	return "rpc.ListenFailed{Addr:" + e.Addr + " Reason:" + e.Reason + "}"
}

// RPCUnrepresentable is rpc.Unrepresentable: an integer at Path whose
// Value a JavaScript number does not hold exactly.
type RPCUnrepresentable struct {
	Path  string
	Value string
}

// Error is the failure's text.
func (e RPCUnrepresentable) Error() string {
	return "rpc.Unrepresentable{Path:" + e.Path + " Value:" + e.Value + "}"
}

// RPCDefect is the defect of a call that the peer ended with a Die cause,
// or that broke the protocol: Name and Message are the Die's.
type RPCDefect struct {
	Name    string
	Message string
}

// Error is the defect's text, as a JavaScript Error prints.
func (e RPCDefect) Error() string { return e.Name + ": " + e.Message }

// RPCDie raises the defect {name, message}: a server answers it as a Die
// cause with that name and message.
func RPCDie[T any](name string, message string) T { panic(RPCDefect{Name: name, Message: message}) }

// rpcMaxSafe is 2^53-1, JavaScript's Number.MAX_SAFE_INTEGER: the largest
// integer an Effect Schema.Int holds exactly.
const rpcMaxSafe = 1<<53 - 1

// RPCExact fails Unrepresentable when n is outside +-(2^53-1).
func RPCExact(path string, n int64) error {
	if n > rpcMaxSafe || n < -rpcMaxSafe {
		return RPCUnrepresentable{Path: path, Value: strconv.FormatInt(n, 10)}
	}
	return nil
}

// RPCExactUint fails Unrepresentable when n is above 2^53-1.
func RPCExactUint(path string, n uint64) error {
	if n > rpcMaxSafe {
		return RPCUnrepresentable{Path: path, Value: strconv.FormatUint(n, 10)}
	}
	return nil
}

// RPCTagged is object, a JSON object's text, with "_tag": tag as its first
// member: the encoded form of a Schema.TaggedError and of a union variant.
func RPCTagged(tag string, object string) string {
	b := make([]byte, 0, len(object)+len(tag)+12)
	b = append(b, `{"_tag":`...)
	b = JSONAppendString(b, tag)
	rest := strings.TrimSpace(object)
	if len(rest) < 2 || rest[0] != '{' {
		panic(RPCDefect{Name: "RpcError", Message: "rpc.Tagged: not an object: " + object})
	}
	if inner := strings.TrimSpace(rest[1:]); inner != "}" {
		b = append(b, ',')
		b = append(b, inner...)
	} else {
		b = append(b, '}')
	}
	return string(b)
}

// RPCTagOf is the "_tag" member of the object text, "" when it has none.
func RPCTagOf(text string) string {
	var m struct {
		Tag string `json:"_tag"`
	}
	if json.Unmarshal([]byte(text), &m) != nil {
		return ""
	}
	return m.Tag
}

// Listener is rpc.Listener: a bound TCP listener that Serve serves.
type Listener struct {
	ln   net.Listener
	addr string
}

// RPCListenerOn is a Listener over ln, which Go code makes: an in-process
// transport for a benchmark of Serve. Ergo source has only Listen.
func RPCListenerOn(ln net.Listener) *Listener {
	return &Listener{ln: ln, addr: ln.Addr().String()}
}

// Addr is the address the listener is bound to, with its port.
func (l *Listener) Addr() string { return l.addr }

// RPCListen binds addr, host:port, on TCP; port 0 picks a free port.
//
//ergo:host
func RPCListen(addr string) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		reason := err.Error()
		if e, ok := err.(*net.OpError); ok && e.Err != nil {
			reason = e.Err.Error()
		}
		return nil, RPCListenFailed{Addr: addr, Reason: reason}
	}
	return &Listener{ln: ln, addr: ln.Addr().String()}, nil
}

// rpcMaxBody bounds a request body: Effect's ndjson line cap, 16 MiB.
const rpcMaxBody = 16 << 20

// rpcEnvelope is one message from a client. ID keeps the id's JSON token.
type rpcEnvelope struct {
	Tag     string          `json:"_tag"`
	ID      json.RawMessage `json:"id"`
	RPC     string          `json:"tag"`
	Payload json.RawMessage `json:"payload"`
}

// rpcCall is one Request on its way to a task of Serve and back.
type rpcCall struct {
	id, tag, payload string
	// exit is the Exit's "exit" member; done is closed after it is set
	// and after the call's task has ended, its releases included.
	exit string
	done chan struct{}
	// mu guards task and cancelled, so a cancel before the task starts
	// still reaches it.
	mu        sync.Mutex
	task      *rt.Task
	cancelled bool
}

// cancel interrupts the call's task, now or when it starts.
func (c *rpcCall) cancel() {
	c.mu.Lock()
	c.cancelled = true
	t := c.task
	c.mu.Unlock()
	if t != nil {
		t.Interrupt()
	}
}

// started records the call's task, and interrupts it when the call was
// cancelled before.
func (c *rpcCall) started(t *rt.Task) {
	c.mu.Lock()
	c.task = t
	cancelled := c.cancelled
	c.mu.Unlock()
	if cancelled {
		t.Interrupt()
	}
}

// rpcServer is the HTTP side of one Serve: it hands each Request to the
// serving loop and writes the Exits.
type rpcServer struct {
	mount string
	calls chan *rpcCall
	quit  chan struct{} // closed when Serve ends
}

// RPCServe serves Effect RPC over HTTP on l at mount until the calling
// task is stopped: each Request runs handle(tag, payload) on a task of its
// own below t, whose stop follows the HTTP request (a client abort stops
// it, and the response waits until its releases ran). handle's value is
// the Success value's text; its Failed is the Fail error's text; a defect
// is a Die. A stop of t closes the listener, stops every call and waits
// for each before Serve returns.
//
//ergo:host
func RPCServe(span Site, t *rt.Task, l *Listener, mount string, handle func(*rt.Task, string, string) (string, error)) {
	t.Check()
	s := &rpcServer{mount: strings.TrimSuffix(mount, "/"), calls: make(chan *rpcCall), quit: make(chan struct{})}
	srv := &http.Server{Handler: s}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(l.ln) }()
	// ended carries each finished call task's handle back to this
	// goroutine, which owns the handles and consumes each with Wait.
	ended := make(chan *rt.Handle[struct{}], 16)
	live := map[*rt.Handle[struct{}]]bool{}
	stop := t.Done()
	shutdown := func() {
		close(s.quit)
		_ = srv.Close()
		for h := range live {
			h.Task().Interrupt()
		}
		for len(live) > 0 {
			h := <-ended
			delete(live, h)
			_, _ = h.Wait()
		}
	}
	for {
		select {
		case c := <-s.calls:
			holder := rt.NewScope(t)
			var self atomic.Pointer[rt.Handle[struct{}]]
			ready := make(chan struct{})
			h := rt.Go(&holder, string(span), func(ct *rt.Task) (struct{}, error) {
				c.started(ct)
				v, err := guarded(ct, func(ct *rt.Task) (string, error) { return handle(ct, c.tag, c.payload) })
				c.exit = rpcExit(v, err)
				close(c.done)
				<-ready
				ended <- self.Load()
				return struct{}{}, nil
			})
			self.Store(h)
			close(ready)
			live[h] = true
		case h := <-ended:
			delete(live, h)
			_, _ = h.Wait()
		case <-stop:
			shutdown()
			panic(rt.Interrupted)
		case err := <-failed:
			shutdown()
			panic(RPCDefect{Name: "RpcError", Message: "rpc.Serve: " + err.Error()})
		}
	}
}

// rpcExit is the Exit's "exit" member for a call's outcome.
func rpcExit(v string, err error) string {
	if err == nil {
		return `{"_tag":"Success","value":` + v + `}`
	}
	if f, ok := rt.Failure(err); ok {
		if e, ok := f.(RPCFailed); ok {
			return `{"_tag":"Failure","cause":[{"_tag":"Fail","error":` + e.Text + `}]}`
		}
		return rpcDieExit("Error", f.Error())
	}
	if rt.OnlyInterruption(err) {
		return `{"_tag":"Failure","cause":[{"_tag":"Interrupt","fiberId":null}]}`
	}
	name, message := "Error", err.Error()
	if c, ok := err.(*rt.Cause); ok {
		for _, r := range c.Reasons {
			if r.Kind != rt.KindDefect {
				continue
			}
			p := r.Value
			if pv, ok := p.(*Panic); ok {
				p = pv.Value
			}
			if d, ok := p.(RPCDefect); ok {
				name, message = d.Name, d.Message
			} else {
				message = fmt.Sprint(p)
			}
			break
		}
	}
	return rpcDieExit(name, message)
}

func rpcDieExit(name, message string) string {
	b := []byte(`{"_tag":"Failure","cause":[{"_tag":"Die","defect":{"name":`)
	b = JSONAppendString(b, name)
	b = append(b, `,"message":`...)
	b = JSONAppendString(b, message)
	b = append(b, `}}]}`...)
	return string(b)
}

const (
	rpcJSON   = "application/json"
	rpcNDJSON = "application/x-ndjson"
)

// ServeHTTP answers one POST: it reads the body's envelopes, starts a
// call for each Request, and writes each call's Exit.
func (s *rpcServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p := r.URL.Path; p != s.mount && p != s.mount+"/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "rpc: POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rpcMaxBody))
	if err != nil {
		http.Error(w, "rpc: "+err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	framed := strings.HasPrefix(r.Header.Get("Content-Type"), rpcNDJSON)
	envs, err := rpcEnvelopes(body, framed)
	if err != nil {
		http.Error(w, "rpc: "+err.Error(), http.StatusBadRequest)
		return
	}
	if framed {
		w.Header().Set("Content-Type", rpcNDJSON)
	} else {
		w.Header().Set("Content-Type", rpcJSON)
	}
	var calls []*rpcCall
	for _, e := range envs {
		if e.Tag != "Request" {
			continue // Ack, Ping and Eof mean nothing over HTTP
		}
		c := &rpcCall{id: string(e.ID), tag: e.RPC, payload: string(e.Payload), done: make(chan struct{})}
		if len(c.payload) == 0 {
			c.payload = "{}"
		}
		if len(c.id) == 0 {
			c.id = "null"
		}
		select {
		case s.calls <- c:
			calls = append(calls, c)
		case <-s.quit:
			c.exit = `{"_tag":"Failure","cause":[{"_tag":"Interrupt","fiberId":null}]}`
			close(c.done)
			calls = append(calls, c)
		}
	}
	s.answer(w, r.Context(), calls, framed)
}

// answer writes the Exit of each call as it ends; a done request context
// (the client went away) cancels the calls left and waits for them.
func (s *rpcServer) answer(w http.ResponseWriter, ctx context.Context, calls []*rpcCall, framed bool) {
	ends := make(chan *rpcCall, len(calls))
	for _, c := range calls {
		go func() { <-c.done; ends <- c }()
	}
	var out []byte
	if !framed {
		out = append(out, '[')
	}
	flusher, _ := w.(http.Flusher)
	gone := ctx.Done()
	for left := len(calls); left > 0; {
		select {
		case c := <-ends:
			left--
			line := rpcExitLine(c.id, c.exit)
			if framed {
				line = append(line, '\n')
				if gone != nil {
					_, _ = w.Write(line)
					if flusher != nil {
						flusher.Flush()
					}
				}
			} else {
				if len(out) > 1 {
					out = append(out, ',')
				}
				out = append(out, line...)
			}
		case <-gone:
			gone = nil
			for _, c := range calls {
				c.cancel()
			}
		}
	}
	if !framed && gone != nil {
		out = append(out, ']')
		_, _ = w.Write(out)
	}
}

func rpcExitLine(id, exit string) []byte {
	b := make([]byte, 0, len(id)+len(exit)+40)
	b = append(b, `{"_tag":"Exit","requestId":`...)
	b = append(b, id...)
	b = append(b, `,"exit":`...)
	b = append(b, exit...)
	return append(b, '}')
}

// rpcEnvelopes reads a body: one JSON value per line (ndjson), or one
// envelope or an array of them (json).
func rpcEnvelopes(body []byte, framed bool) ([]rpcEnvelope, error) {
	var envs []rpcEnvelope
	if framed {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(nil, rpcMaxBody)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var e rpcEnvelope
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			envs = append(envs, e)
		}
		return envs, sc.Err()
	}
	t := bytes.TrimSpace(body)
	if len(t) > 0 && t[0] == '[' {
		err := json.Unmarshal(t, &envs)
		return envs, err
	}
	var e rpcEnvelope
	if err := json.Unmarshal(t, &e); err != nil {
		return nil, err
	}
	return append(envs, e), nil
}

// rpcIDs numbers the Requests of this process's clients, from 0, as
// Effect's default id generator does.
var rpcIDs atomic.Uint64

// rpcClient is the HTTP client of every Call: no timeout of its own, since
// a stop of the calling task ends a call.
var rpcClient = &http.Client{}

// RPCCall posts one Request for the operation tag with payload, a JSON
// object's text, to url, and returns the Success value's text. A Fail
// cause is Failed with the error's text; a Die cause, a protocol error and
// a transport error are defects. A stop of t aborts the request and ends
// the call with the stop.
//
//ergo:host
func RPCCall(t *rt.Task, url string, tag string, payload string) (string, error) {
	t.Check()
	id := strconv.FormatUint(rpcIDs.Add(1)-1, 10)
	b := make([]byte, 0, len(payload)+len(tag)+64)
	b = append(b, `{"_tag":"Request","id":`...)
	b = append(b, id...)
	b = append(b, `,"tag":`...)
	b = JSONAppendString(b, tag)
	b = append(b, `,"payload":`...)
	b = append(b, payload...)
	b = append(b, `,"headers":[]}`...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(url, "/")+"/", bytes.NewReader(b))
	if err != nil {
		panic(RPCDefect{Name: "RpcClientError", Message: err.Error()})
	}
	req.Header.Set("Content-Type", rpcJSON)
	type result struct {
		body []byte
		ct   string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := rpcClient.Do(req)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		got <- result{body: body, ct: resp.Header.Get("Content-Type"), err: err}
	}()
	var res result
	select {
	case res = <-got:
	case <-t.Done():
		cancel()
		<-got
		panic(rt.Interrupted)
	}
	if res.err != nil {
		panic(RPCDefect{Name: "RpcClientError", Message: res.err.Error()})
	}
	return rpcReadExit(res.body, strings.HasPrefix(res.ct, rpcNDJSON), id)
}

// rpcReply is one message from a server.
type rpcReply struct {
	Tag       string          `json:"_tag"`
	RequestID json.RawMessage `json:"requestId"`
	Exit      *struct {
		Tag   string          `json:"_tag"`
		Value json.RawMessage `json:"value"`
		Cause []struct {
			Tag    string          `json:"_tag"`
			Error  json.RawMessage `json:"error"`
			Defect json.RawMessage `json:"defect"`
		} `json:"cause"`
	} `json:"exit"`
	Defect json.RawMessage `json:"defect"`
}

// rpcReadExit finds the Exit of request id in a response body and returns
// its outcome as RPCCall does.
func rpcReadExit(body []byte, framed bool, id string) (string, error) {
	var replies []rpcReply
	if framed {
		for _, line := range bytes.Split(body, []byte{'\n'}) {
			if line = bytes.TrimSpace(line); len(line) == 0 {
				continue
			}
			var r rpcReply
			if err := json.Unmarshal(line, &r); err != nil {
				panic(RPCDefect{Name: "RpcClientError", Message: "malformed response: " + err.Error()})
			}
			replies = append(replies, r)
		}
	} else {
		t := bytes.TrimSpace(body)
		var err error
		if len(t) > 0 && t[0] == '[' {
			err = json.Unmarshal(t, &replies)
		} else {
			var r rpcReply
			err = json.Unmarshal(t, &r)
			replies = append(replies, r)
		}
		if err != nil {
			panic(RPCDefect{Name: "RpcClientError", Message: "malformed response: " + err.Error()})
		}
	}
	for _, r := range replies {
		switch r.Tag {
		case "Defect":
			panic(rpcDefectOf(r.Defect))
		case "ClientProtocolError":
			panic(RPCDefect{Name: "RpcClientError", Message: "the server refused the request"})
		case "Exit":
		default:
			continue
		}
		if rid := strings.Trim(string(r.RequestID), `"`); rid != id || r.Exit == nil {
			continue
		}
		switch r.Exit.Tag {
		case "Success":
			if len(r.Exit.Value) == 0 {
				return "null", nil
			}
			return string(r.Exit.Value), nil
		case "Failure":
			for _, c := range r.Exit.Cause {
				if c.Tag == "Fail" {
					return "", RPCFailed{Text: string(c.Error)}
				}
			}
			for _, c := range r.Exit.Cause {
				if c.Tag == "Die" {
					panic(rpcDefectOf(c.Defect))
				}
			}
			panic(RPCDefect{Name: "RpcInterrupted", Message: "the server interrupted the call"})
		}
	}
	panic(RPCDefect{Name: "RpcClientError", Message: "no Exit for request " + id})
}

// rpcDefectOf reads a Die's defect: {name, message}, or any other value
// as its text.
func rpcDefectOf(raw json.RawMessage) RPCDefect {
	var d struct {
		Name    *string `json:"name"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(raw, &d) == nil && d.Message != nil {
		name := "Error"
		if d.Name != nil {
			name = *d.Name
		}
		return RPCDefect{Name: name, Message: *d.Message}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return RPCDefect{Name: "Error", Message: s}
	}
	return RPCDefect{Name: "Error", Message: string(raw)}
}

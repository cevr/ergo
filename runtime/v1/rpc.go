package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// The bodies of std rpc (docs/spec/rpc.md; R1): Effect RPC 4.1.2's HTTP
// protocol with its json and ndjson serializations (effect/src/rpc/
// RpcMessage.ts, RpcSerialization.ts). Std rpc's Answer serves it as an
// ordinary std http handler (Rm): the Ergo side routes the request and
// streams an ndjson answer through http.Stream, and writes and reads the
// payloads, values and failures with std json's derived codecs; this file
// parses the envelopes, runs the calls and writes the Exits. Call posts
// over net/http.
//
// Wire facts, each the shape RpcMessage.ts gives (the wire is the spec):
//   - a client posts to its URL with a trailing slash; Answer answers the
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

// rpcDefect is the defect of a call that the peer ended with a Die cause,
// or that broke the protocol: Name and Message are the Die's.
type rpcDefect struct {
	Name    string
	Message string
}

// Error is the defect's text, as a JavaScript Error prints.
func (e rpcDefect) Error() string { return e.Name + ": " + e.Message }

// RPCDie raises the defect {name, message}: a server answers it as a Die
// cause with that name and message.
func RPCDie[T any](name string, message string) T { panic(rpcDefect{Name: name, Message: message}) }

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
		panic(rpcDefect{Name: "RpcError", Message: "rpc.Tagged: not an object: " + object})
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

// RPCMalformed is rpc.Malformed: a request body that is not Effect RPC's.
type RPCMalformed struct {
	Reason string
}

// Error is the failure's text.
func (e RPCMalformed) Error() string { return "rpc.Malformed{Reason:" + e.Reason + "}" }

// RPCEnvelope is rpc.Envelope: one message of a request body. ID keeps
// the id's JSON token as sent.
type RPCEnvelope struct {
	Tag     string
	ID      string
	RPC     string
	Payload string
}

// rpcCall is one Request on a task of its own and its Exit.
type rpcCall struct {
	id, tag, payload string
	// exit is the Exit's "exit" member, set before the call's task sends
	// the call on ended.
	exit string
	h    *rt.Handle[struct{}]
}

// rpcRun is the calls of one request body below the request's task t.
// Only the goroutine that runs t calls its methods: it owns the handles
// and consumes each with Wait; ended carries each finished call back.
type rpcRun struct {
	span   Site
	t      *rt.Task
	handle func(*rt.Task, string, string) (string, error)
	ended  chan *rpcCall
	live   map[*rpcCall]bool
}

// start runs the call c on a task of its own below t.
func (s *rpcRun) start(c *rpcCall) {
	holder := rt.NewScope(s.t)
	ready := make(chan struct{})
	c.h = rt.Go(&holder, string(s.span), func(ct *rt.Task) (struct{}, error) {
		v, err := guarded(ct, func(ct *rt.Task) (string, error) { return s.handle(ct, c.tag, c.payload) })
		c.exit = rpcExit(v, err)
		<-ready // c.h is set
		s.ended <- c
		return struct{}{}, nil
	})
	close(ready)
	s.live[c] = true
}

// end consumes the finished call c.
func (s *rpcRun) end(c *rpcCall) {
	delete(s.live, c)
	_, _ = c.h.Wait()
}

// stopAll stops every call left and consumes each, its releases run.
func (s *rpcRun) stopAll() {
	for c := range s.live {
		c.h.Task().Interrupt()
	}
	for len(s.live) > 0 {
		s.end(<-s.ended)
	}
}

// rpcCalls runs each Request of envs on a task of its own below t and
// gives each Exit line to each as its call ends, on t's goroutine. A stop
// of t, a defect or a stop raised by each, and an error of each stop the
// calls left and wait for them before rpcCalls ends: no call outlives it.
func rpcCalls(span Site, t *rt.Task, envs []RPCEnvelope, handle func(*rt.Task, string, string) (string, error), each func(line []byte) error) error {
	t.Check()
	s := &rpcRun{span: span, t: t, handle: handle, ended: make(chan *rpcCall, len(envs)), live: map[*rpcCall]bool{}}
	// A stop or a defect unwinds through here: the calls left stop and are
	// consumed while the panic goes on, so no recover takes it from its
	// owner. A failed each returns with calls left, which stop the same way;
	// on a whole run live is already empty.
	defer s.stopAll()
	for _, e := range envs {
		if e.Tag != "Request" {
			continue // Ack, Ping and Eof mean nothing over HTTP
		}
		c := &rpcCall{id: e.ID, tag: e.RPC, payload: e.Payload}
		if c.payload == "" {
			c.payload = "{}"
		}
		if c.id == "" {
			c.id = "null"
		}
		s.start(c)
	}
	stop := t.Done()
	for len(s.live) > 0 {
		select {
		case c := <-s.ended:
			s.end(c)
			if err := each(rpcExitLine(c.id, c.exit)); err != nil {
				return err // the deferred stopAll stops the calls left
			}
		case <-stop:
			panic(rt.Interrupted)
		}
	}
	return nil
}

// RPCExits runs each Request of envs on a task of its own below t and is
// the json answer: the array of their Exits, in the order the calls end,
// each with its request's id. A stop of t stops the calls, waits for
// their releases and ends RPCExits with the stop.
func RPCExits(span Site, t *rt.Task, envs []RPCEnvelope, handle func(*rt.Task, string, string) (string, error)) string {
	out := []byte{'['}
	_ = rpcCalls(span, t, envs, handle, func(line []byte) error {
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, line...)
		return nil
	})
	return string(append(out, ']'))
}

// RPCExitLines runs each Request of envs as RPCExits does and emits each
// Exit as an ndjson line as soon as its call ends: emit's failure stops
// the calls left and is RPCExitLines' failure.
func RPCExitLines(span Site, t *rt.Task, envs []RPCEnvelope, handle func(*rt.Task, string, string) (string, error), emit func(*rt.Task, string) error) error {
	return rpcCalls(span, t, envs, handle, func(line []byte) error {
		return emit(t, string(append(line, '\n')))
	})
}

// RPCNextID is the id of this process's next Request, from 0, as
// Effect's default id generator counts.
func RPCNextID() string { return strconv.FormatUint(rpcIDs.Add(1)-1, 10) }

// RPCRequest is the body of one Request id for tag with payload.
func RPCRequest(id, tag, payload string) string {
	b := make([]byte, 0, len(payload)+len(tag)+64)
	b = append(b, `{"_tag":"Request","id":`...)
	b = append(b, id...)
	b = append(b, `,"tag":`...)
	b = JSONAppendString(b, tag)
	b = append(b, `,"payload":`...)
	b = append(b, payload...)
	b = append(b, `,"headers":[]}`...)
	return string(b)
}

// RPCReadExit finds the Exit of request id in an answer of status and
// content type and returns its outcome as RPCCall does; a status other
// than 200 is the defect RpcClientError.
func RPCReadExit(id string, status int, contentType string, body string) (string, error) {
	if status != http.StatusOK {
		panic(rpcDefect{Name: "RpcClientError", Message: fmt.Sprintf("status %d: %s", status, strings.TrimSpace(body))})
	}
	return rpcReadExit([]byte(body), strings.HasPrefix(contentType, rpcNDJSON), id)
}

// rpcResult is a response's body and content type, or why there is none.
type rpcResult struct {
	body []byte
	ct   string
	err  error
}

// rpcPost sends req with hc and reads the whole response.
func rpcPost(hc *http.Client, req *http.Request) rpcResult {
	resp, err := hc.Do(req)
	if err != nil {
		return rpcResult{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return rpcResult{body: body, ct: resp.Header.Get("Content-Type"), err: err}
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
			if d, ok := p.(rpcDefect); ok {
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

func rpcExitLine(id, exit string) []byte {
	b := make([]byte, 0, len(id)+len(exit)+40)
	b = append(b, `{"_tag":"Exit","requestId":`...)
	b = append(b, id...)
	b = append(b, `,"exit":`...)
	b = append(b, exit...)
	return append(b, '}')
}

// rpcEnvelope is one message of a body as JSON writes it. ID keeps the
// id's JSON token.
type rpcEnvelope struct {
	Tag     string          `json:"_tag"`
	ID      json.RawMessage `json:"id"`
	RPC     string          `json:"tag"`
	Payload json.RawMessage `json:"payload"`
}

// RPCParse reads a body's envelopes: one JSON value per line (framed,
// ndjson), or one envelope or an array of them (json). A body that is
// not one fails Malformed, before any call starts.
func RPCParse(text string, framed bool) ([]RPCEnvelope, error) {
	var raw []rpcEnvelope
	var err error
	if framed {
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			var e rpcEnvelope
			if err = json.Unmarshal([]byte(line), &e); err != nil {
				break
			}
			raw = append(raw, e)
		}
	} else if t := strings.TrimSpace(text); len(t) > 0 && t[0] == '[' {
		err = json.Unmarshal([]byte(t), &raw)
	} else {
		var e rpcEnvelope
		err = json.Unmarshal([]byte(t), &e)
		raw = append(raw, e)
	}
	if err != nil {
		return nil, RPCMalformed{Reason: err.Error()}
	}
	envs := make([]RPCEnvelope, len(raw))
	for i, e := range raw {
		envs[i] = RPCEnvelope{Tag: e.Tag, ID: string(e.ID), RPC: e.RPC, Payload: string(e.Payload)}
	}
	return envs, nil
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
	id := RPCNextID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(url, "/")+"/", strings.NewReader(RPCRequest(id, tag, payload)))
	if err != nil {
		panic(rpcDefect{Name: "RpcClientError", Message: err.Error()})
	}
	req.Header.Set("Content-Type", rpcJSON)
	got := make(chan rpcResult, 1)
	go func() { got <- rpcPost(rpcClient, req) }()
	var res rpcResult
	select {
	case res = <-got:
	case <-t.Done():
		cancel()
		<-got
		panic(rt.Interrupted)
	}
	if res.err != nil {
		panic(rpcDefect{Name: "RpcClientError", Message: res.err.Error()})
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
				panic(rpcDefect{Name: "RpcClientError", Message: "malformed response: " + err.Error()})
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
			panic(rpcDefect{Name: "RpcClientError", Message: "malformed response: " + err.Error()})
		}
	}
	for _, r := range replies {
		switch r.Tag {
		case "Defect":
			panic(rpcDefectOf(r.Defect))
		case "ClientProtocolError":
			panic(rpcDefect{Name: "RpcClientError", Message: "the server refused the request"})
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
			panic(rpcDefect{Name: "RpcInterrupted", Message: "the server interrupted the call"})
		}
	}
	panic(rpcDefect{Name: "RpcClientError", Message: "no Exit for request " + id})
}

// rpcDefectOf reads a Die's defect: {name, message}, or any other value
// as its text.
func rpcDefectOf(raw json.RawMessage) rpcDefect {
	var d struct {
		Name    *string `json:"name"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(raw, &d) == nil && d.Message != nil {
		name := "Error"
		if d.Name != nil {
			name = *d.Name
		}
		return rpcDefect{Name: name, Message: *d.Message}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return rpcDefect{Name: "Error", Message: s}
	}
	return rpcDefect{Name: "Error", Message: string(raw)}
}

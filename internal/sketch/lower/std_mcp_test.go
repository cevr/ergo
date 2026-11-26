package lower

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdMCPLawsProbe restates the laws of std mcp and mcp/memory (wf10 MCP0,
// moved from modules/mcp) as Ergo tests over their exported API: the
// server laws over generated sets of tools, Transport.StopsWhenAsked over
// memory.Transport, and memory's CallAnswersOnce, with the packages'
// helpers restated. MemorySession is a client's whole session, the one
// place a tool that ran and failed is answered (mutant
// ap2-tool-error-as-rpc-error). mcp/stdio's Transport keeps
// StopsWhenAsked under its own named context, overText (stdio's
// Transport over io.NewInput and io.NewRecorder), which TestStdMCPLawsRun
// runs with ergo test (stdioStopsWhenAsked).
const stdMCPLawsProbe = `import http
import io
import json
import mcp
import mcp/httpmcp
import mcp/memory
import mcp/stdio
import rand
import strings
import task

// echo is a tool named name that gives its name back.
func echo(name string) mcp.Tool {
	mcp.Tool{Name: name, Description: "gives \(name)", Input: json.Object{Members: []json.Member{}}, Call: func(arguments json.Value) json.Value fails mcp.ToolError { json.Str{Value: name} }}
}

// serverOf is a server whose tools are named names, in order.
func serverOf(names []string) mcp.Server {
	let mut tools = []mcp.Tool{}
	for n in names {
		tools = tools.Append(echo(n))
	}
	mcp.Server{Name: "laws", Version: "1", Tools: tools}
}

// quoted is s as a JSON string.
func quoted(s string) string { json.Encode(json.Codec[string]{MaxBytes: 0, MaxDepth: 0}, s) }

// parsed is the JSON value text spells, or null.
func parsed(text string) json.Value {
	json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case _:
		json.Null
	}
}

func lawListNamesEveryTool(names []string) bool {
	match mcp.Handle(serverOf(names), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}") {
	case mcp.Answered as a:
		let listing = json.Decode(json.Codec[mcp.Listing]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch {
		case _:
			return false
		}
		let mut got = []string{}
		for t in listing.Tools {
			got = got.Append(t.Name)
		}
		got == names
	case mcp.Refused, mcp.Silent:
		false
	}
}

test McpListNamesEveryTool(names []string) { assert lawListNamesEveryTool(names) }

func lawUnknownToolIsATypedError(names []string, asked string) bool {
	for n in names {
		if n == asked {
			return true
		}
	}
	let s = serverOf(names)
	let mut failed = false
	let _ = mcp.Call(s, asked, json.Null) catch {
	case mcp.UnknownTool as u:
		failed = u.Name == asked
		json.Null
	case mcp.ToolError:
		json.Null
	}
	let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":" + quoted(asked) + "}}"
	failed && mcp.Handle(s, message) == mcp.Refused{ID: json.IntNumber(7), Code: mcp.InvalidParams, Message: "unknown tool: \(asked)"}
}

test McpUnknownToolIsATypedError(names []string, asked string) { assert lawUnknownToolIsATypedError(names, asked) }

func lawArgumentsOutsideTheSchemaAreRefused(key string, keys []string) bool {
	let schema = parsed("{\"type\":\"object\",\"required\":[" + quoted(key) + "]}")
	let s = mcp.Server{Name: "laws", Version: "1", Tools: []mcp.Tool{mcp.Tool{Name: "t", Description: "t", Input: schema, Call: func(arguments json.Value) json.Value fails mcp.ToolError { json.Str{Value: "ran"} }}}}
	let mut members = []string{}
	let mut has = false
	for k in keys {
		members = members.Append(quoted(k) + ":null")
		has = has || k == key
	}
	let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":\"t\",\"arguments\":{" + strings.Join(members, ",") + "}}}"
	match mcp.Handle(s, message) {
	case mcp.Answered:
		has
	case mcp.Refused as r:
		!has && r.Code == mcp.InvalidParams
	case mcp.Silent:
		false
	}
}

test McpArgumentsOutsideTheSchemaAreRefused(key string, keys []string) { assert lawArgumentsOutsideTheSchemaAreRefused(key, keys) }

// stopsWhenAsked is Transport.StopsWhenAsked on t.
func stopsWhenAsked(t memory.Transport) bool {
	let mut n = 0
	for _ in t.Messages() {
		n = n + 1
		break
	}
	n <= 1
}

test McpTransportStopsWhenAsked(input []string) { assert stopsWhenAsked(memory.New(input)) }

// add is a tool that adds the integers a and b of its arguments, and
// fails ToolError when one is missing.
func add() mcp.Tool {
	let schema = parsed("{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"integer\"},\"b\":{\"type\":\"integer\"}},\"required\":[\"a\",\"b\"]}")
	mcp.Tool{Name: "add", Description: "adds a and b", Input: schema, Call: func(arguments json.Value) json.Value fails mcp.ToolError {
		let a = try integer(arguments, "a")
		let b = try integer(arguments, "b")
		json.IntNumber(a + b)
	}}
}

// integer is the integer key of arguments.
func integer(arguments json.Value, key string) int64 fails mcp.ToolError {
	match mcp.Field(arguments, key) {
	case Some[json.Value] as v:
		let n = json.Decode(json.Codec[int64]{MaxBytes: 0, MaxDepth: 0}, json.Encode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, v.Value)) catch {
		case _:
			return mcp.ToolError{Message: "\(key) is not an integer"}
		}
		n
	case None:
		mcp.ToolError{Message: "\(key) is missing"}
	}
}

// calc is the server of the session.
func calc() mcp.Server { mcp.Server{Name: "calc", Version: "0.1.0", Tools: []mcp.Tool{add()}} }

// lenient is calc whose add requires only a, so a call without b runs
// the tool and fails.
func lenient() mcp.Server {
	let strict = add()
	let schema = parsed("{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"integer\"},\"b\":{\"type\":\"integer\"}},\"required\":[\"a\"]}")
	mcp.Server{Name: "calc", Version: "0.1.0", Tools: []mcp.Tool{mcp.Tool{Name: strict.Name, Description: strict.Description, Input: schema, Call: strict.Call}}}
}

func lawCallAnswersOnce(kinds []int) bool {
	let mut input = []string{}
	let mut ids = []string{}
	for i, k in kinds {
		let method = match ((k % 3) + 3) % 3 {
		case 0:
			"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":1}}"
		case 1:
			"tools/list\""
		case _:
			"ping\""
		}
		if k%2 == 0 {
			input = input.Append("{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"" + method + "}")
			ids = ids.Append("r\(i)")
		} else {
			input = input.Append("{\"jsonrpc\":\"2.0\",\"method\":\"" + method + "}")
		}
	}
	let t = memory.New(input)
	if mcp.Serve(calc(), t) != len(kinds) {
		return false
	}
	let sent = t.Sent()
	if len(sent) != len(ids) {
		return false
	}
	for i, line in sent {
		if !strings.HasPrefix(line, "{\"jsonrpc\":\"2.0\",\"id\":\"\(ids[i])\",\"result\":") {
			return false
		}
	}
	true
}

test MemoryCallAnswersOnce(kinds []int) { assert lawCallAnswersOnce(kinds) }

func httpLawServer(tag string) mcp.Server {
	let schema = parsed("{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"integer\"},\"b\":{\"type\":\"integer\"}},\"required\":[\"a\"]}")
	let addTool = mcp.Tool{
		Name:        "add",
		Description: "adds a and b",
		Input:       schema,
		Call: func(arguments json.Value) json.Value fails mcp.ToolError {
			let a = try integer(arguments, "a")
			let b = try integer(arguments, "b")
			json.IntNumber(a + b)
		},
	}
	let whoTool = mcp.Tool{
		Name:        "who",
		Description: "identifies caller",
		Input:       json.Object{Members: []json.Member{}},
		Call: func(_ json.Value) json.Value fails mcp.ToolError {
			json.Str{Value: tag}
		},
	}
	mcp.Server{Name: "calc-" + tag, Version: "0.1.0", Tools: []mcp.Tool{addTool, whoTool}}
}

func httpInMemoryHandler(e httpmcp.Endpoint) http.Handler {
	http.InMemory(func(r http.Request) http.Response needs http.Streaming { httpmcp.Handle(e, r) })
}

func lawHttpMatchesMemory(kinds []int) bool {
	let s = httpLawServer("m")
	let mut input = []string{}
	for i, k in kinds {
		let mode = ((k % 7) + 7) % 7
		let msg = match mode {
		case 0:
			"{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}"
		case 1:
			"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}"
		case 2:
			"{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/list\"}"
		case 3:
			"{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":2}}}"
		case 4:
			"{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i)}}}"
		case 5:
			"{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"missing\",\"arguments\":{}}}"
		case _:
			"{\"jsonrpc\":\"2.0\",\"id\":\(i),"
		}
		input = input.Append(msg)
	}
	let mem = memory.New(input)
	if mcp.Serve(s, mem) != len(input) {
		return false
	}
	let ep = httpmcp.New(s, httpmcp.DefaultOptions())
	let jsonClient = httpmcp.NewClient(httpInMemoryHandler(ep), "/mcp")
	let sseClient = httpmcp.NewSSEClient(httpInMemoryHandler(ep), "/mcp")
	let mut gotJSON = []string{}
	let mut gotSSE = []string{}
	for msg in input {
		let a = jsonClient.Call(msg) catch {
		case _:
			return false
		}
		match a {
		case Some[string] as line:
			gotJSON = gotJSON.Append(line.Value)
		case None:
		}
		let b = sseClient.Call(msg) catch {
		case _:
			return false
		}
		match b {
		case Some[string] as line:
			gotSSE = gotSSE.Append(line.Value)
		case None:
		}
	}
	let want = mem.Sent()
	gotJSON == want && gotSSE == want
}

test HttpmcpHttpMatchesMemory(kinds []int) { assert lawHttpMatchesMemory(kinds) }

func lawProtocolHeadersAndSecurity(seed int) bool {
	let s = httpLawServer("sec")
	let opts = httpmcp.Options{
		AllowedOrigins: []string{"http://localhost:3000"},
		MaxBodyBytes:   96,
		Sessions:       httpmcp.Stateless,
	}
	let ep = httpmcp.New(s, opts)
	let ping = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}"
	let notif = "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}"
	let okReq = http.Request{
		Method:  "POST",
		Path:    "/mcp",
		RawPath: "/mcp",
		Query:   []http.Field{},
		Header: []http.Field{
			http.Field{Name: "Origin", Value: "HTTP://LOCALHOST:3000/"},
			http.Field{Name: "Content-Type", Value: "application/json; charset=utf-8"},
			http.Field{Name: "Accept", Value: "application/json"},
		},
		Body: http.Buffered{Text: ping},
	}
	with context { http.Streaming: http.NewRecorder() } {
		let okResp = httpmcp.Handle(ep, okReq)
		if okResp.Status != 200 || http.Header(okResp.Header, httpmcp.ProtocolVersionHeader) != (Some[string]{Value: mcp.Version}) {
			return false
		}
		let notifResp = httpmcp.Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: notif}})
		if notifResp.Status != 202 || notifResp.Body != "" || http.Header(notifResp.Header, httpmcp.ProtocolVersionHeader) != (Some[string]{Value: mcp.Version}) {
			return false
		}
		let evilOrigin = httpmcp.Handle(ep, http.Request{..okReq, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://evil-\(seed).test"},
			http.Field{Name: "Content-Type", Value: "application/json"},
			http.Field{Name: "Accept", Value: "application/json"},
		}})
		if evilOrigin.Status != 403 {
			return false
		}
		let badAccept = httpmcp.Handle(ep, http.Request{..okReq, Header: []http.Field{
			http.Field{Name: "Content-Type", Value: "application/json"},
			http.Field{Name: "Accept", Value: "text/plain"},
		}})
		if badAccept.Status != 406 {
			return false
		}
		let badCT = httpmcp.Handle(ep, http.Request{..okReq, Header: []http.Field{
			http.Field{Name: "Content-Type", Value: "text/plain"},
			http.Field{Name: "Accept", Value: "application/json"},
		}})
		if badCT.Status != 415 {
			return false
		}
		let tooBig = httpmcp.Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: strings.Repeat("a", 128)}})
		if tooBig.Status != 413 {
			return false
		}
		let getResp = httpmcp.Handle(ep, http.Request{..okReq, Method: "GET"})
		let delResp = httpmcp.Handle(ep, http.Request{..okReq, Method: "DELETE"})
		getResp.Status == 405 && delResp.Status == 405
	}
}

test HttpmcpProtocolHeadersAndSecurity(seed int) { assert lawProtocolHeadersAndSecurity(seed) }

func lawSessionLifecycleAndIsolation(seed int) bool {
	let store = httpmcp.NewSessions(rand.NewSeededSecure(uint64(seed) + 1))
	let opts = httpmcp.Options{
		AllowedOrigins: []string{},
		MaxBodyBytes:   1048576,
		Sessions:       httpmcp.Stateful{Store: store},
	}
	let ep = httpmcp.NewDynamic(opts, func(req http.Request) httpmcp.Resolved {
		match http.Header(req.Header, "Authorization") {
		case Some[string] as auth:
			if auth.Value == "Bearer alice" {
				return httpmcp.Use{Server: httpLawServer("alice")}
			}
			if auth.Value == "Bearer bob" {
				return httpmcp.Use{Server: httpLawServer("bob")}
			}
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}}
		case None:
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}}
		}
	})
	let net = httpInMemoryHandler(ep)
	let initMsg = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"c\",\"version\":\"1\"}}}"
	let whoMsg = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"who\",\"arguments\":{}}}"
	let anon = httpmcp.NewClient(net, "/mcp")
	let mut unauthOK = false
	let _ = anon.Call(initMsg) catch {
	case httpmcp.UnexpectedStatus as u:
		unauthOK = u.Status == 401
		None
	case _:
		return false
	}
	if !unauthOK {
		return false
	}
	let alice = httpmcp.NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer alice"}})
	let bob = httpmcp.NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer bob"}})
	let mut missingSessionOK = false
	let _ = alice.Call(whoMsg) catch {
	case httpmcp.UnexpectedStatus as u:
		missingSessionOK = u.Status == 400
		None
	case _:
		return false
	}
	if !missingSessionOK {
		return false
	}
	let _ = alice.Call(initMsg) catch {
	case _:
		return false
	}
	let _ = bob.Call(initMsg) catch {
	case _:
		return false
	}
	let aliceID = alice.SessionID()
	let bobID = bob.SessionID()
	if aliceID == "" || bobID == "" || aliceID == bobID || httpmcp.Count(store) != 2 {
		return false
	}
	let aliceWho = alice.Call(whoMsg) catch {
	case _:
		return false
	}
	let bobWho = bob.Call(whoMsg) catch {
	case _:
		return false
	}
	if aliceWho != (Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"alice\\\"\"}],\"isError\":false}}"}) {
		return false
	}
	if bobWho != (Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"bob\\\"\"}],\"isError\":false}}"}) {
		return false
	}
	alice.Terminate() catch {
	case _:
		return false
	}
	if httpmcp.Has(store, aliceID) || !httpmcp.Has(store, bobID) {
		return false
	}
	let postDelete = with context { http.Streaming: http.NewRecorder() } {
		httpmcp.Handle(ep, http.Request{
			Method:  "POST",
			Path:    "/mcp",
			RawPath: "/mcp",
			Query:   []http.Field{},
			Header: []http.Field{
				http.Field{Name: "Authorization", Value: "Bearer alice"},
				http.Field{Name: "Content-Type", Value: "application/json"},
				http.Field{Name: "Accept", Value: "application/json"},
				http.Field{Name: httpmcp.SessionHeader, Value: aliceID},
			},
			Body: http.Buffered{Text: whoMsg},
		})
	}
	postDelete.Status == 404
}

test HttpmcpSessionLifecycleAndIsolation(seed int) { assert lawSessionLifecycleAndIsolation(seed) }

func lawBatchMatchesMemory(kinds []int) bool {
	if len(kinds) == 0 {
		return true
	}
	let s = httpLawServer("batch")
	let mut items = []string{}
	for i, k in kinds {
		if k%2 == 0 {
			items = items.Append("{\"jsonrpc\":\"2.0\",\"id\":\"b\(i)\",\"method\":\"ping\"}")
		} else {
			items = items.Append("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}")
		}
	}
	let mem = memory.New(items)
	let _ = mcp.Serve(s, mem)
	let want = mem.Sent()
	let ep = httpmcp.New(s, httpmcp.DefaultOptions())
	let client = httpmcp.NewClient(httpInMemoryHandler(ep), "/mcp")
	let batch = "[" + strings.Join(items, ",") + "]"
	let got = client.Call(batch) catch {
	case _:
		return false
	}
	if len(want) == 0 {
		return got == None
	}
	got == Some[string]{Value: "[" + strings.Join(want, ",") + "]"}
}

test HttpmcpBatchMatchesMemory(kinds []int) { assert lawBatchMatchesMemory(kinds) }

func lawMcpResourcesCapabilityMatchesServer(hasResources bool, subscribe bool, listChanged bool) bool {
	let s = mcp.Server{Name: "res", Version: "1", Tools: []mcp.Tool{}}
	let f = if hasResources {
		mcp.WithResources(s, mcp.ResourceSet{
			List:        func() []mcp.Resource { []mcp.Resource{} },
			Read:        func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError { mcp.ResourceNotFound{URI: uri} },
			Subscribe:   subscribe,
			ListChanged: listChanged,
		})
	} else {
		mcp.Full(s)
	}
	match mcp.HandleFull(f, mcp.NewSubscriptions(), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}") {
	case mcp.Answered as a:
		let decoded = json.Decode(json.Codec[mcp.Initialized]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch {
		case _:
			return false
		}
		if !hasResources {
			decoded.Capabilities.Resources == json.Absent && !strings.Contains(a.Result, "\"resources\"")
		} else {
			decoded.Capabilities.Resources == json.Present{Value: mcp.ResourcesCapability{Subscribe: subscribe, ListChanged: listChanged}}
		}
	case mcp.Refused, mcp.Silent:
		false
	}
}

test McpResourcesCapabilityMatchesServer(hasResources bool, subscribe bool, listChanged bool) {
	assert lawMcpResourcesCapabilityMatchesServer(hasResources, subscribe, listChanged)
}

func lawMcpResourceListAndReadRoundTrip(names []string, body string, binary bool) bool {
	let mut items = []mcp.Resource{}
	let mut seen = Map[string, bool]{}
	for n in names {
		let uri = "mem://item/" + n
		match seen.Get(uri) {
		case Some[bool]:
		case None:
			seen = seen.Put(uri, true)
			items = items.Append(mcp.Resource{
				URI:         uri,
				Name:        n,
				Description: if n == "" { "" } else { "desc-" + n },
				MIMEType:    if binary { "application/octet-stream" } else { "text/plain" },
			})
		}
	}
	let rs = mcp.StaticResources(items, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		match seen.Get(uri) {
		case Some[bool]:
			if binary {
				[]mcp.ResourceContents{mcp.BlobContents(uri, "application/octet-stream", strings.Bytes(body))}
			} else {
				[]mcp.ResourceContents{mcp.TextContents(uri, "text/plain", body)}
			}
		case None:
			mcp.ResourceNotFound{URI: uri}
		}
	})
	let f = mcp.WithResources(mcp.Server{Name: "res", Version: "1", Tools: []mcp.Tool{}}, rs)
	let subs = mcp.NewSubscriptions()
	let listedOK = match mcp.HandleFull(f, subs, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"resources/list\"}") {
	case mcp.Answered as a:
		let got = mcp.DecodeResourceListing(a.Result) catch {
		case mcp.ResourceError:
			return false
		}
		got == items
	case mcp.Refused, mcp.Silent:
		false
	}
	let mut readOK = true
	for it in items {
		let req = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(it.URI) + "}}"
		match mcp.HandleFull(f, subs, req) {
		case mcp.Answered as a:
			let got = mcp.DecodeResourceContents(a.Result) catch {
			case mcp.ResourceError:
				readOK = false
				[]mcp.ResourceContents{}
			}
			let want = if binary {
				[]mcp.ResourceContents{mcp.BlobContents(it.URI, "application/octet-stream", strings.Bytes(body))}
			} else {
				[]mcp.ResourceContents{mcp.TextContents(it.URI, "text/plain", body)}
			}
			readOK = readOK && got == want
		case mcp.Refused, mcp.Silent:
			readOK = false
		}
	}
	let missingReq = "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"resources/read\",\"params\":{\"uri\":\"mem://missing/never\"}}"
	let missingOK = match mcp.HandleFull(f, subs, missingReq) {
	case mcp.Refused as r:
		r.Code == mcp.ResourceNotFoundCode
	case mcp.Answered, mcp.Silent:
		false
	}
	listedOK && readOK && missingOK
}

test McpResourceListAndReadRoundTrip(names []string, body string, binary bool) {
	assert lawMcpResourceListAndReadRoundTrip(names, body, binary)
}

func lawMcpSubscriptionFiltersNotifications(subURI string, otherURI string, msg string) bool {
	let target = "mem://sub/" + subURI
	let other = "mem://other/" + otherURI
	let rs = mcp.StaticResources([]mcp.Resource{}, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		mcp.ResourceNotFound{URI: uri}
	})
	let f = mcp.WithResources(mcp.Server{Name: "sub", Version: "1", Tools: []mcp.Tool{}}, rs)
	let subs = mcp.NewSubscriptions()
	let n = mcp.NewNotifier(16)
	let sub = mcp.SubscribeWith(n, subs)
	let subReq = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(target) + "}}"
	let subOK = match mcp.HandleFull(f, subs, subReq) {
	case mcp.Answered as a:
		a.Result == "{}"
	case mcp.Refused, mcp.Silent:
		false
	}
	let s1 = mcp.NotifyResourceUpdated(n, target)
	let _ = mcp.NotifyResourceUpdated(n, other)
	let s2 = mcp.Notify(n, mcp.LogMessage("info", "mcp", json.Str{Value: msg}))
	let unsubReq = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(target) + "}}"
	let unsubOK = match mcp.HandleFull(f, subs, unsubReq) {
	case mcp.Answered as a:
		a.Result == "{}"
	case mcp.Refused, mcp.Silent:
		false
	}
	let _ = mcp.NotifyResourceUpdated(n, target)
	let s3 = mcp.Notify(n, mcp.ToolsListChanged())
	mcp.CloseSubscription(sub)
	let mut got = []mcp.SequencedNotification{}
	for {
		match mcp.Next(sub) {
		case Some[mcp.SequencedNotification] as ev:
			got = got.Append(ev.Value)
		case None:
			break
		}
	}
	let want = []mcp.SequencedNotification{
		mcp.SequencedNotification{Seq: s1, Notification: mcp.ResourceUpdated(target)},
		mcp.SequencedNotification{Seq: s2, Notification: mcp.LogMessage("info", "mcp", json.Str{Value: msg})},
		mcp.SequencedNotification{Seq: s3, Notification: mcp.ToolsListChanged()},
	}
	subOK && unsubOK && got == want
}

test McpSubscriptionFiltersNotifications(subURI string, otherURI string, msg string) {
	assert lawMcpSubscriptionFiltersNotifications(subURI, otherURI, msg)
}

func probeExtractID(line string) Option[string] {
	let v = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, line) catch {
	case _:
		return None
	}
	match mcp.Field(v, "id") {
	case Some[json.Value] as iv:
		match iv.Value {
		case json.Str as s:
			Some[string]{Value: s.Value}
		case json.Null, json.Boolean, json.Num, json.Array, json.Object:
			None
		}
	case None:
		None
	}
}

func probeWaitAnswerID(t memory.LiveTransport, wantID string) Option[[]mcp.Notification] {
	let mut out = []mcp.Notification{}
	for {
		match memory.NextServer(t) {
		case None:
			return None
		case Some[string] as msg:
			match mcp.ParseNotification(msg.Value) {
			case Some[mcp.Notification] as n:
				out = out.Append(n.Value)
			case None:
				if probeExtractID(msg.Value) == (Some[string]{Value: wantID}) {
					return Some[[]mcp.Notification]{Value: out}
				}
				return None
			}
		}
	}
	None
}

func lawMemoryLiveSessionOrdersAndFilters(kinds []int, subKey string, otherKey string) bool {
	let target = "mem://live/" + subKey
	let other = "mem://other/" + otherKey
	let rs = mcp.StaticResources([]mcp.Resource{
		mcp.Resource{URI: target, Name: "target", Description: "", MIMEType: "text/plain"},
	}, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		if uri == target {
			return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "live")}
		}
		mcp.ResourceNotFound{URI: uri}
	})
	let full = mcp.WithResources(calc(), rs)
	let n = mcp.NewNotifier(256)
	let t = memory.NewLive(256)
	let srv = task.Go() { memory.ServeLive(full, n, t) }

	let mut notifs = []mcp.Notification{}
	let mut wantNotifs = []mcp.Notification{}
	let mut ok = true

	if !memory.SendClient(t, "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(target) + "}}") {
		ok = false
	}
	if ok {
		match probeWaitAnswerID(t, "sub") {
		case Some[[]mcp.Notification] as beforeSub:
			notifs = notifs.Concat(beforeSub.Value)
		case None:
			ok = false
		}
	}

	let mut reqIDs = []string{}
	for i, k in kinds {
		let mode = ((k % 4) + 4) % 4
		let id = "k\(i)"
		if mode == 0 {
			let _ = mcp.NotifyResourceUpdated(n, target)
			wantNotifs = wantNotifs.Append(mcp.ResourceUpdated(target))
		} else if mode == 1 {
			let _ = mcp.NotifyResourceUpdated(n, other)
		} else if mode == 2 {
			let msg = mcp.LogMessage("info", "mem", json.IntNumber(int64(i)))
			let _ = mcp.Notify(n, msg)
			wantNotifs = wantNotifs.Append(msg)
		} else {
			reqIDs = reqIDs.Append(id)
			let req = "{\"jsonrpc\":\"2.0\",\"id\":" + quoted(id) + ",\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(target) + "}}"
			if !memory.SendClient(t, req) {
				ok = false
			}
		}
	}
	let done = "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(target) + "}}"
	if !memory.SendClient(t, done) {
		ok = false
	}

	let mut gotReqIDs = []string{}
	for {
		match memory.NextServer(t) {
		case None:
			ok = false
			break
		case Some[string] as msg:
			match mcp.ParseNotification(msg.Value) {
			case Some[mcp.Notification] as notif:
				notifs = notifs.Append(notif.Value)
			case None:
				match probeExtractID(msg.Value) {
				case Some[string] as id:
					if id.Value == "unsub" {
						break
					}
					gotReqIDs = gotReqIDs.Append(id.Value)
				case None:
					ok = false
				}
			}
		}
	}

	let _ = mcp.NotifyResourceUpdated(n, target)
	let after = mcp.ToolsListChanged()
	let _ = mcp.Notify(n, after)
	wantNotifs = wantNotifs.Append(after)

	if !memory.SendClient(t, "{\"jsonrpc\":\"2.0\",\"id\":\"tail\",\"method\":\"ping\"}") {
		ok = false
	}
	memory.CloseClient(t)
	let mut sawTail = false
	for msg in memory.ServerMessages(t) {
		match mcp.ParseNotification(msg) {
		case Some[mcp.Notification] as notif:
			notifs = notifs.Append(notif.Value)
		case None:
			if probeExtractID(msg) == (Some[string]{Value: "tail"}) {
				sawTail = true
			} else {
				ok = false
			}
		}
	}
	let _ = srv.Wait()
	ok && sawTail && gotReqIDs == reqIDs && notifs == wantNotifs
}

test MemoryLiveSessionOrdersAndFilters(kinds []int, subKey string, otherKey string) {
	assert lawMemoryLiveSessionOrdersAndFilters(kinds, subKey, otherKey)
}

func lawStdioDeliversConcurrentNotifications(kinds []int, key string) bool {
	let target = "stdio://res/" + key
	let other = "stdio://other/" + key
	let notifier = mcp.NewNotifier(128)
	let emitTool = mcp.Tool{
		Name:        "emit",
		Description: "emits resource and log notifications",
		Input:       json.Object{Members: []json.Member{}},
		Call: func(arguments json.Value) json.Value fails mcp.ToolError {
			let _ = mcp.NotifyResourceUpdated(notifier, target)
			let _ = mcp.NotifyResourceUpdated(notifier, other)
			let _ = mcp.Notify(notifier, mcp.LogMessage("info", "stdio", json.Str{Value: key}))
			json.Str{Value: "ok"}
		},
	}
	let rs = mcp.StaticResources([]mcp.Resource{
		mcp.Resource{URI: target, Name: "item", Description: "", MIMEType: "text/plain"},
	}, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		if uri == target {
			return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "stdio-body")}
		}
		mcp.ResourceNotFound{URI: uri}
	})
	let full = mcp.WithResources(mcp.Server{Name: "stdio-law", Version: "1", Tools: []mcp.Tool{emitTool}}, rs)

	let mut lines = []string{
		"{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(target) + "}}",
	}
	let mut wantIDs = []string{"sub"}
	let mut emitCount = 0
	for i, k in kinds {
		let mode = ((k % 3) + 3) % 3
		let id = "r\(i)"
		wantIDs = wantIDs.Append(id)
		if mode == 0 {
			emitCount = emitCount + 1
			lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + quoted(id) + ",\"method\":\"tools/call\",\"params\":{\"name\":\"emit\",\"arguments\":{}}}")
		} else if mode == 1 {
			lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + quoted(id) + ",\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(target) + "}}")
		} else {
			lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + quoted(id) + ",\"method\":\"ping\"}")
		}
	}
	lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(target) + "}}")
	lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":\"after\",\"method\":\"tools/call\",\"params\":{\"name\":\"emit\",\"arguments\":{}}}")
	wantIDs = wantIDs.Append("unsub")
	wantIDs = wantIDs.Append("after")

	let rec = io.NewRecorder()
	let tr = stdio.NewFrom(io.NewInput(strings.Join(lines, "\n") + "\n"), rec)
	let readCount = mcp.ServeFull(full, notifier, tr)

	let raw = strings.TrimSuffix(rec.Text(), "\n")
	let outLines = if raw == "" { []string{} } else { strings.Split(raw, "\n") }
	let mut gotIDs = []string{}
	let mut updatedTarget = 0
	let mut updatedOther = 0
	let mut logCount = 0
	let mut valid = readCount == len(lines)
	for ln in outLines {
		match mcp.ParseNotification(ln) {
		case Some[mcp.Notification] as n:
			if n.Value.Method == mcp.MethodResourcesUpdated {
				match mcp.NotificationURI(n.Value) {
				case Some[string] as u:
					if u.Value == target {
						updatedTarget = updatedTarget + 1
					} else if u.Value == other {
						updatedOther = updatedOther + 1
					} else {
						valid = false
					}
				case None:
					valid = false
				}
			} else if n.Value.Method == mcp.MethodMessage {
				logCount = logCount + 1
			} else {
				valid = false
			}
		case None:
			let v = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, ln) catch {
			case _:
				valid = false
				json.Null
			}
			match mcp.Field(v, "id") {
			case Some[json.Value] as iv:
				match iv.Value {
				case json.Str as s:
					gotIDs = gotIDs.Append(s.Value)
				case _:
					valid = false
				}
			case None:
				valid = false
			}
		}
	}
	valid && gotIDs == wantIDs && updatedTarget == emitCount && updatedOther == 0 && logCount == emitCount+1
}

test StdioDeliversConcurrentNotifications(kinds []int, key string) {
	assert lawStdioDeliversConcurrentNotifications(kinds, key)
}

func lawHttpmcpGetStreamSecurityAndLifecycle(seed int) bool {
	let store = httpmcp.NewSessions(rand.NewSeededSecure(uint64(seed) + 1))
	let opts = httpmcp.Options{
		AllowedOrigins: []string{"http://localhost:3000"},
		MaxBodyBytes:   1048576,
		Sessions:       httpmcp.Stateful{Store: store},
	}
	let srv = mcp.Server{Name: "get-sec", Version: "1", Tools: []mcp.Tool{}}
	let ep = httpmcp.NewDynamic(opts, func(req http.Request) httpmcp.Resolved {
		match http.Header(req.Header, "Authorization") {
		case Some[string] as auth:
			if auth.Value == "Bearer ok" {
				return httpmcp.Use{Server: srv}
			}
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{}, Body: "unauthorized"}}
		case None:
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{}, Body: "unauthorized"}}
		}
	})
	let net = httpInMemoryHandler(ep)
	let c = httpmcp.NewClientWithHeaders(net, "/mcp", []http.Field{
		http.Field{Name: "Origin", Value: "http://localhost:3000"},
		http.Field{Name: "Authorization", Value: "Bearer ok"},
	})
	let _ = c.Initialize("c", "1") catch {
	case _:
		return false
	}
	let sid = c.SessionID()
	let baseGet = http.Request{
		Method:  "GET",
		Path:    "/mcp",
		RawPath: "/mcp",
		Query:   []http.Field{},
		Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.SessionHeader, Value: sid},
		},
		Body: http.Buffered{Text: ""},
	}
	let (badOrigin, unauth, badAccept, badVer, missingSid, unknownSid, badMethod) = with context { http.Streaming: http.NewRecorder() } {
		let rOrigin = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://evil-\(seed).test"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.SessionHeader, Value: "does-not-exist"},
		}})
		let rUnauth = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.SessionHeader, Value: "does-not-exist"},
		}})
		let rAccept = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "application/json"},
			http.Field{Name: httpmcp.SessionHeader, Value: "does-not-exist"},
		}})
		let rVer = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.ProtocolVersionHeader, Value: "1999-01-01"},
			http.Field{Name: httpmcp.SessionHeader, Value: "does-not-exist"},
		}})
		let rNoSid = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
		}})
		let rBadSid = httpmcp.Handle(ep, http.Request{..baseGet, Header: []http.Field{
			http.Field{Name: "Origin", Value: "http://localhost:3000"},
			http.Field{Name: "Authorization", Value: "Bearer ok"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.SessionHeader, Value: "does-not-exist"},
		}})
		let rPut = httpmcp.Handle(ep, http.Request{..baseGet, Method: "PUT"})
		(rOrigin.Status == 403, rUnauth.Status == 401, rAccept.Status == 406, rVer.Status == 400, rNoSid.Status == 400, rBadSid.Status == 404, rPut.Status == 405)
	}
	let streamTask = task.Go() {
		let mut got = []mcp.Notification{}
		let mut streamReadOK = true
		c.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev.Notification)
		}) catch {
		case _:
			streamReadOK = false
		}
		streamReadOK && len(got) == 1
	}
	if !httpmcp.WaitStream(store, sid) {
		httpmcp.CloseStreams(store, sid)
		return false
	}
	let _ = httpmcp.Notify(store, mcp.ToolsListChanged())
	httpmcp.CloseStreams(store, sid)
	let streamOK = streamTask.Wait()
	c.Terminate() catch {
	case _:
		return false
	}
	let afterDel = with context { http.Streaming: http.NewRecorder() } { httpmcp.Handle(ep, baseGet) }
	badOrigin && unauth && badAccept && badVer && missingSid && unknownSid && badMethod && streamOK && afterDel.Status == 404
}

test HttpmcpGetStreamSecurityAndLifecycle(seed int) {
	assert lawHttpmcpGetStreamSecurityAndLifecycle(seed)
}

func lawHttpmcpTransportsParityForResourcesAndEvents(kinds []int, uriKey string, textBody string) bool {
	let uriA = "res://a/" + uriKey
	let uriB = "res://b/" + uriKey
	let items = []mcp.Resource{
		mcp.Resource{URI: uriA, Name: "doc-a", Description: "first", MIMEType: "text/plain"},
		mcp.Resource{URI: uriB, Name: "doc-b", Description: "", MIMEType: "application/octet-stream"},
	}
	let bin = strings.Bytes("bin-" + textBody)
	let rs = mcp.StaticResources(items, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		if uri == uriA {
			return []mcp.ResourceContents{mcp.TextContents(uriA, "text/plain", textBody)}
		}
		if uri == uriB {
			return []mcp.ResourceContents{mcp.BlobContents(uriB, "application/octet-stream", bin)}
		}
		mcp.ResourceNotFound{URI: uri}
	})
	let srv = mcp.Server{Name: "parity", Version: "1", Tools: []mcp.Tool{}}
	let full = mcp.WithResources(srv, rs)

	let reqMsgs = []string{
		"{\"jsonrpc\":\"2.0\",\"id\":\"l\",\"method\":\"resources/list\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":\"ra\",\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(uriA) + "}}",
		"{\"jsonrpc\":\"2.0\",\"id\":\"rb\",\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(uriB) + "}}",
		"{\"jsonrpc\":\"2.0\",\"id\":\"rm\",\"method\":\"resources/read\",\"params\":{\"uri\":\"res://missing/x\"}}",
	}
	let memTr = memory.New(reqMsgs)
	let _ = mcp.ServeFull(full, mcp.NewNotifier(8), memTr)
	let memLines = memTr.Sent()

	let stdioRec = io.NewRecorder()
	let stdioTr = stdio.NewFrom(io.NewInput(strings.Join(reqMsgs, "\n") + "\n"), stdioRec)
	let _ = mcp.ServeFull(full, mcp.NewNotifier(8), stdioTr)
	let stdioLines = strings.Split(strings.TrimSuffix(stdioRec.Text(), "\n"), "\n")

	let store = httpmcp.NewSessions(rand.NewSeededSecure(9))
	let opts = httpmcp.Options{AllowedOrigins: []string{}, MaxBodyBytes: 1048576, Sessions: httpmcp.Stateful{Store: store}}
	let ep = httpmcp.NewFull(full, opts)
	let net = httpInMemoryHandler(ep)
	let client = httpmcp.NewClient(net, "/mcp")
	let init = client.Initialize("parity", "1") catch {
	case _:
		return false
	}
	let listedHTTP = client.ListResources() catch {
	case _:
		return false
	}
	let readAHTTP = client.ReadResource(uriA) catch {
	case _:
		return false
	}
	let readBHTTP = client.ReadResource(uriB) catch {
	case _:
		return false
	}
	let mut missingCodeHTTP = 0
	let _ = client.ReadResource("res://missing/x") catch {
	case httpmcp.RPCError as re:
		missingCodeHTTP = re.Code
		[]mcp.ResourceContents{}
	case _:
		return false
	}
	let mut httpDirectLines = []string{}
	for m in reqMsgs {
		let ans = client.Call(m) catch {
		case _:
			return false
		}
		match ans {
		case Some[string] as s:
			httpDirectLines = httpDirectLines.Append(s.Value)
		case None:
			return false
		}
	}

	client.Subscribe(uriA) catch {
	case _:
		return false
	}
	let memNotif = mcp.NewNotifier(128)
	let memLive = memory.NewLive(128)
	let memTask = task.Go() { memory.ServeLive(full, memNotif, memLive) }
	if !memory.SendClient(memLive, "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(uriA) + "}}") {
		return false
	}
	let _ = memory.NextServer(memLive)

	let stdioNotif = mcp.NewNotifier(128)
	let stdioSubs = mcp.NewSubscriptions()
	let stdioSub = mcp.SubscribeWith(stdioNotif, stdioSubs)
	let _ = mcp.HandleFull(full, stdioSubs, "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(uriA) + "}}")

	let mut wantNotifs = []mcp.Notification{}
	for i, k in kinds {
		let mode = ((k % 3) + 3) % 3
		let ev = if mode == 0 {
			mcp.ResourceUpdated(uriA)
		} else if mode == 1 {
			mcp.ResourceUpdated(uriB)
		} else {
			mcp.LogMessage("info", "parity", json.IntNumber(int64(i)))
		}
		let _ = httpmcp.Notify(store, ev)
		let _ = mcp.Notify(memNotif, ev)
		let _ = mcp.Notify(stdioNotif, ev)
		if mode != 1 {
			wantNotifs = wantNotifs.Append(ev)
		}
	}

	let sid = client.SessionID()
	let reader1 = task.Go() {
		let mut got = []mcp.Notification{}
		client.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev.Notification)
		}) catch {
		case _:
			got = []mcp.Notification{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let httpStream1 = reader1.Wait()
	let replayCursor = client.LastEventID()

	let readerReplay = task.Go() {
		let mut got = []mcp.Notification{}
		client.ReadEventsSince("0", func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev.Notification)
		}) catch {
		case _:
			got = []mcp.Notification{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let httpReplay1 = readerReplay.Wait()

	client.Unsubscribe(uriA) catch {
	case _:
		return false
	}
	let _ = memory.SendClient(memLive, "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(uriA) + "}}")
	let mut memNotifs = []mcp.Notification{}
	for {
		match memory.NextServer(memLive) {
		case None:
			return false
		case Some[string] as ln:
			match mcp.ParseNotification(ln.Value) {
			case Some[mcp.Notification] as notif:
				memNotifs = memNotifs.Append(notif.Value)
			case None:
				break
			}
		}
	}
	let _ = mcp.HandleFull(full, stdioSubs, "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(uriA) + "}}")

	let blockedAfter = mcp.ResourceUpdated(uriA)
	let afterBroadcast = mcp.ToolsListChanged()
	let _ = httpmcp.Notify(store, blockedAfter)
	let _ = httpmcp.Notify(store, afterBroadcast)
	let _ = mcp.Notify(memNotif, blockedAfter)
	let _ = mcp.Notify(memNotif, afterBroadcast)
	let _ = mcp.Notify(stdioNotif, blockedAfter)
	let _ = mcp.Notify(stdioNotif, afterBroadcast)
	wantNotifs = wantNotifs.Append(afterBroadcast)

	let reader2 = task.Go() {
		let mut got = []mcp.Notification{}
		client.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev.Notification)
		}) catch {
		case _:
			got = []mcp.Notification{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let httpStream2 = reader2.Wait()

	let readerCursor = task.Go() {
		let mut got = []mcp.Notification{}
		client.ReadEventsSince(replayCursor, func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev.Notification)
		}) catch {
		case _:
			got = []mcp.Notification{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let httpCursorTail = readerCursor.Wait()

	memory.CloseClient(memLive)
	for ln in memory.ServerMessages(memLive) {
		match mcp.ParseNotification(ln) {
		case Some[mcp.Notification] as n:
			memNotifs = memNotifs.Append(n.Value)
		case None:
		}
	}
	let _ = memTask.Wait()

	mcp.CloseSubscription(stdioSub)
	let mut stdioNotifs = []mcp.Notification{}
	for {
		match mcp.Next(stdioSub) {
		case Some[mcp.SequencedNotification] as ev:
			stdioNotifs = stdioNotifs.Append(ev.Value.Notification)
		case None:
			break
		}
	}

	let capOK = init.Capabilities.Resources == (json.Present{Value: mcp.ResourcesCapability{Subscribe: true, ListChanged: false}})
	let reqParity = memLines == stdioLines && memLines == httpDirectLines
	let resOK = listedHTTP == items && readAHTTP == []mcp.ResourceContents{mcp.TextContents(uriA, "text/plain", textBody)} && readBHTTP == []mcp.ResourceContents{mcp.BlobContents(uriB, "application/octet-stream", bin)} && missingCodeHTTP == mcp.ResourceNotFoundCode
	let httpAll = httpStream1.Concat(httpStream2)
	let cursorOK = if replayCursor == "" { len(httpCursorTail) == 0 } else { httpCursorTail == httpStream2 }
	let eventParity = memNotifs == wantNotifs && stdioNotifs == wantNotifs && httpAll == wantNotifs && httpReplay1 == httpStream1 && cursorOK
	capOK && reqParity && resOK && eventParity
}

test HttpmcpTransportsParityForResourcesAndEvents(kinds []int, uriKey string, textBody string) {
	assert lawHttpmcpTransportsParityForResourcesAndEvents(kinds, uriKey, textBody)
}

func lawHttpmcpSubscriptionAndLastEventIDReplay(subKey string, otherKey string, count int) bool {
	let subURI = "http://res/" + subKey
	let otherURI = "http://other/" + otherKey
	let n = 1 + ((count % 5) + 5) % 5
	let store = httpmcp.NewSessions(rand.NewSeededSecure(17))
	let rs = mcp.StaticResources([]mcp.Resource{
		mcp.Resource{URI: subURI, Name: "sub", Description: "", MIMEType: "text/plain"},
	}, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		if uri == subURI {
			return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "ok")}
		}
		mcp.ResourceNotFound{URI: uri}
	})
	let ep = httpmcp.NewWithResources(mcp.Server{Name: "sse", Version: "1", Tools: []mcp.Tool{}}, rs, httpmcp.Options{
		AllowedOrigins: []string{},
		MaxBodyBytes:   1048576,
		Sessions:       httpmcp.Stateful{Store: store},
	})
	let net = httpInMemoryHandler(ep)
	let c = httpmcp.NewClient(net, "/mcp")
	let _ = c.Initialize("c", "1") catch {
	case _:
		return false
	}
	c.Subscribe(subURI) catch {
	case _:
		return false
	}
	let sid = c.SessionID()
	let mut firstExpected = []mcp.Notification{}
	for i in 0..n {
		let ev = mcp.ResourceUpdated(subURI)
		let _ = httpmcp.Notify(store, ev)
		let _ = httpmcp.NotifyResourceUpdated(store, otherURI)
		firstExpected = firstExpected.Append(ev)
		let _ = i
	}
	let t1 = task.Go() {
		let mut got = []httpmcp.NotificationEvent{}
		c.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev)
		}) catch {
		case _:
			got = []httpmcp.NotificationEvent{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let batch1 = t1.Wait()
	let cursor1 = c.LastEventID()

	let missed = mcp.LogMessage("notice", "replay", json.Str{Value: subKey})
	let _ = httpmcp.Notify(store, missed)
	c.Unsubscribe(subURI) catch {
	case _:
		return false
	}
	let _ = httpmcp.NotifyResourceUpdated(store, subURI)
	let afterUnsub = mcp.ResourcesListChanged()
	let _ = httpmcp.Notify(store, afterUnsub)

	let t2 = task.Go() {
		let mut got = []httpmcp.NotificationEvent{}
		c.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev)
		}) catch {
		case _:
			got = []httpmcp.NotificationEvent{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let batch2 = t2.Wait()

	let mut batch1Notifs = []mcp.Notification{}
	for e in batch1 {
		batch1Notifs = batch1Notifs.Append(e.Notification)
	}
	let mut batch2Notifs = []mcp.Notification{}
	for e in batch2 {
		batch2Notifs = batch2Notifs.Append(e.Notification)
	}
	let tReplay = task.Go() {
		let mut got = []httpmcp.NotificationEvent{}
		c.ReadEventsSince(cursor1, func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			got = got.Append(ev)
		}) catch {
		case _:
			got = []httpmcp.NotificationEvent{}
		}
		got
	}
	if !httpmcp.WaitStream(store, sid) {
		return false
	}
	httpmcp.CloseStreams(store, sid)
	let replayBatch = tReplay.Wait()
	let mut replayNotifs = []mcp.Notification{}
	for e in replayBatch {
		replayNotifs = replayNotifs.Append(e.Notification)
	}

	let wantBatch2 = []mcp.Notification{missed, afterUnsub}
	batch1Notifs == firstExpected && cursor1 != "" && batch2Notifs == wantBatch2 && replayNotifs == wantBatch2 && c.LastEventID() != cursor1
}

test HttpmcpSubscriptionAndLastEventIDReplay(subKey string, otherKey string, count int) {
	assert lawHttpmcpSubscriptionAndLastEventIDReplay(subKey, otherKey, count)
}

` + stdMCPSession

// stdMCPSession is a client's session: initialize, the initialized
// notification, the list, a call, a call whose tool fails, an unknown
// tool, an unknown method, a message that is not JSON, a ping, and a call
// outside the tool's schema. Each request gets one answer, the
// notification none. It was module mcp's memory test Session; its add
// requires only a (lenient), so the call without b runs the tool and
// fails, and the one without a is refused by the schema.
const stdMCPSession = `test MemorySession {
	let t = memory.New([]string{
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}",
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":\"c3\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":2,\"b\":40}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":2}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tools/call\",\"params\":{\"name\":\"sub\",\"arguments\":{}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"resources/list\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":7,",
		"{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"ping\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"b\":2}}}",
	})
	assert mcp.Serve(lenient(), t) == 10
	assert strings.Join(t.Sent(), "\n") == strings.Join([]string{
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{\"tools\":{}},\"serverInfo\":{\"name\":\"calc\",\"version\":\"0.1.0\"}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"add\",\"description\":\"adds a and b\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"a\":{\"type\":\"integer\"},\"b\":{\"type\":\"integer\"}},\"required\":[\"a\"]}}]}}",
		"{\"jsonrpc\":\"2.0\",\"id\":\"c3\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"42\"}],\"isError\":false}}",
		"{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"b is missing\"}],\"isError\":true}}",
		"{\"jsonrpc\":\"2.0\",\"id\":5,\"error\":{\"code\":-32602,\"message\":\"unknown tool: sub\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":6,\"error\":{\"code\":-32601,\"message\":\"method not found: resources/list\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"parse error: the message is not JSON\"}}",
		"{\"jsonrpc\":\"2.0\",\"id\":8,\"result\":{}}",
		"{\"jsonrpc\":\"2.0\",\"id\":9,\"error\":{\"code\":-32602,\"message\":\"invalid params: the arguments do not fit the input schema of add\"}}",
	}, "\n")
}
`

// stdMCPLawStatements is the statement of each mcp law, by ledger key;
// lawledger.Run holds it equal to the ledger.
var stdMCPLawStatements = map[string]string{
	"mcp ListNamesEveryTool":                            `ListNamesEveryTool(names []string) { match Handle(serverOf(names), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}") { case Answered as a: let listing = json.Decode(json.Codec[Listing]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch { case _: return false } let mut got = []string{} for t in listing.Tools { got = got.Append(t.Name) } got == names case Refused, Silent: false } }`,
	"mcp UnknownToolIsATypedError":                      `UnknownToolIsATypedError(names []string, asked string) { for n in names { if n == asked { return true } } let s = serverOf(names) let mut failed = false let _ = Call(s, asked, json.Null) catch { case UnknownTool as u: failed = u.Name == asked json.Null case ToolError: json.Null } let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":" + quoted(asked) + "}}" failed && Handle(s, message) == Refused{ID: json.IntNumber(7), Code: InvalidParams, Message: "unknown tool: \(asked)"} }`,
	"mcp ArgumentsOutsideTheSchemaAreRefused":           `ArgumentsOutsideTheSchemaAreRefused(key string, keys []string) { let schema = parsed("{\"type\":\"object\",\"required\":[" + quoted(key) + "]}") let s = Server{Name: "laws", Version: "1", Tools: []Tool{Tool{Name: "t", Description: "t", Input: schema, Call: func(arguments json.Value) json.Value fails ToolError { json.Str{Value: "ran"} }}}} let mut members = []string{} let mut has = false for k in keys { members = members.Append(quoted(k) + ":null") has = has || k == key } let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":\"t\",\"arguments\":{" + strings.Join(members, ",") + "}}}" match Handle(s, message) { case Answered: has case Refused as r: !has && r.Code == InvalidParams case Silent: false } }`,
	"mcp Transport.StopsWhenAsked":                      `StopsWhenAsked() { let mut n = 0 for _ in Messages() { n = n + 1 break } n <= 1 }`,
	"mcp ResourcesCapabilityMatchesServer":              `ResourcesCapabilityMatchesServer(hasResources bool, subscribe bool, listChanged bool) { let s = Server{Name: "res", Version: "1", Tools: []Tool{}} let f = if hasResources { WithResources(s, ResourceSet{ List: func() []Resource { []Resource{} }, Read: func(uri string) []ResourceContents fails ResourceNotFound | ResourceError { ResourceNotFound{URI: uri} }, Subscribe: subscribe, ListChanged: listChanged, }) } else { Full(s) } let ok = match HandleFull(f, NewSubscriptions(), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}") { case Answered as a: let decoded = json.Decode(json.Codec[Initialized]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch { case _: return false } if !hasResources { decoded.Capabilities.Resources == json.Absent && !strings.Contains(a.Result, "\"resources\"") } else { decoded.Capabilities.Resources == json.Present{Value: ResourcesCapability{Subscribe: subscribe, ListChanged: listChanged}} } case Refused, Silent: false } always ok sometimes hasResources && ok sometimes !hasResources && ok ok }`,
	"mcp ResourceListAndReadRoundTrip":                  `ResourceListAndReadRoundTrip(names []string, body string, binary bool) { let mut items = []Resource{} let mut seen = Map[string, bool]{} for n in names { let uri = "mem://item/" + n match seen.Get(uri) { case option.Some[bool]: case option.None: seen = seen.Put(uri, true) items = items.Append(Resource{ URI: uri, Name: n, Description: if n == "" { "" } else { "desc-" + n }, MIMEType: if binary { "application/octet-stream" } else { "text/plain" }, }) } } let rs = StaticResources(items, func(uri string) []ResourceContents fails ResourceNotFound | ResourceError { match seen.Get(uri) { case option.Some[bool]: if binary { []ResourceContents{BlobContents(uri, "application/octet-stream", strings.Bytes(body))} } else { []ResourceContents{TextContents(uri, "text/plain", body)} } case option.None: ResourceNotFound{URI: uri} } }) let f = WithResources(Server{Name: "res", Version: "1", Tools: []Tool{}}, rs) let subs = NewSubscriptions() let listedOK = match HandleFull(f, subs, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"resources/list\"}") { case Answered as a: let got = DecodeResourceListing(a.Result) catch { case ResourceError: return false } got == items case Refused, Silent: false } let mut readOK = true for it in items { let req = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"resources/read\",\"params\":{\"uri\":" + quoted(it.URI) + "}}" match HandleFull(f, subs, req) { case Answered as a: let got = DecodeResourceContents(a.Result) catch { case ResourceError: readOK = false []ResourceContents{} } let want = if binary { []ResourceContents{BlobContents(it.URI, "application/octet-stream", strings.Bytes(body))} } else { []ResourceContents{TextContents(it.URI, "text/plain", body)} } readOK = readOK && got == want case Refused, Silent: readOK = false } } let missingReq = "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"resources/read\",\"params\":{\"uri\":\"mem://missing/never\"}}" let missingOK = match HandleFull(f, subs, missingReq) { case Refused as r: r.Code == ResourceNotFoundCode case Answered, Silent: false } let ok = listedOK && readOK && missingOK always ok sometimes binary && items.Len() > 0 && ok sometimes !binary && items.Len() > 0 && ok ok }`,
	"mcp SubscriptionFiltersNotifications":              `SubscriptionFiltersNotifications(subURI string, otherURI string, msg string) { let target = "mem://sub/" + subURI let other = "mem://other/" + otherURI let rs = StaticResources([]Resource{}, func(uri string) []ResourceContents fails ResourceNotFound | ResourceError { ResourceNotFound{URI: uri} }) let f = WithResources(Server{Name: "sub", Version: "1", Tools: []Tool{}}, rs) let subs = NewSubscriptions() let n = NewNotifier(16) let sub = SubscribeWith(n, subs) let subReq = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + quoted(target) + "}}" let subOK = match HandleFull(f, subs, subReq) { case Answered as a: a.Result == "{}" case Refused, Silent: false } let s1 = NotifyResourceUpdated(n, target) let _ = NotifyResourceUpdated(n, other) let s2 = Notify(n, LogMessage("info", "mcp", json.Str{Value: msg})) let unsubReq = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + quoted(target) + "}}" let unsubOK = match HandleFull(f, subs, unsubReq) { case Answered as a: a.Result == "{}" case Refused, Silent: false } let _ = NotifyResourceUpdated(n, target) let s3 = Notify(n, ToolsListChanged()) CloseSubscription(sub) let mut got = []SequencedNotification{} for { match Next(sub) { case option.Some[SequencedNotification] as ev: got = got.Append(ev.Value) case option.None: break } } let want = []SequencedNotification{ SequencedNotification{Seq: s1, Notification: ResourceUpdated(target)}, SequencedNotification{Seq: s2, Notification: LogMessage("info", "mcp", json.Str{Value: msg})}, SequencedNotification{Seq: s3, Notification: ToolsListChanged()}, } let ok = subOK && unsubOK && got == want always ok sometimes target != other && ok ok }`,
	"mcp/memory CallAnswersOnce":                        `CallAnswersOnce(kinds []int) { let mut input = []string{} let mut ids = []string{} for i, k in kinds { let method = match ((k % 3) + 3) % 3 { case 0: "tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":1}}" case 1: "tools/list\"" case _: "ping\"" } if k%2 == 0 { input = input.Append("{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"" + method + "}") ids = ids.Append("r\(i)") } else { input = input.Append("{\"jsonrpc\":\"2.0\",\"method\":\"" + method + "}") } } let t = New(input) if mcp.Serve(calc, t) != kinds.Len() { return false } let sent = t.Sent() if sent.Len() != ids.Len() { return false } for i, line in sent { if !strings.HasPrefix(line, "{\"jsonrpc\":\"2.0\",\"id\":\"\(ids[i])\",\"result\":") { return false } } true }`,
	"mcp/memory LiveSessionOrdersAndFilters":            `LiveSessionOrdersAndFilters(kinds []int, subKey string, otherKey string) { let target = "mem://live/" + subKey let other = "mem://other/" + otherKey let rs = mcp.StaticResources([]mcp.Resource{ mcp.Resource{URI: target, Name: "target", Description: "", MIMEType: "text/plain"}, }, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError { if uri == target { return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "live")} } mcp.ResourceNotFound{URI: uri} }) let full = mcp.WithResources(calc, rs) let n = mcp.NewNotifier(256) let t = NewLive(256) let srv = task.Go() { ServeLive(full, n, t) } let mut notifs = []mcp.Notification{} let mut wantNotifs = []mcp.Notification{} let mut ok = true if !SendClient(t, "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + jsonText(target) + "}}") { ok = false } if ok { match waitAnswerID(t, "sub") { case option.Some[[]mcp.Notification] as beforeSub: notifs = notifs.Concat(beforeSub.Value) case option.None: ok = false } } let mut reqIDs = []string{} for i, k in kinds { let mode = ((k % 4) + 4) % 4 let id = "k\(i)" if mode == 0 { let _ = mcp.NotifyResourceUpdated(n, target) wantNotifs = wantNotifs.Append(mcp.ResourceUpdated(target)) } else if mode == 1 { let _ = mcp.NotifyResourceUpdated(n, other) } else if mode == 2 { let msg = mcp.LogMessage("info", "mem", json.IntNumber(int64(i))) let _ = mcp.Notify(n, msg) wantNotifs = wantNotifs.Append(msg) } else { reqIDs = reqIDs.Append(id) let req = "{\"jsonrpc\":\"2.0\",\"id\":" + jsonText(id) + ",\"method\":\"resources/read\",\"params\":{\"uri\":" + jsonText(target) + "}}" if !SendClient(t, req) { ok = false } } } let done = "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + jsonText(target) + "}}" if !SendClient(t, done) { ok = false } let mut gotReqIDs = []string{} for { match NextServer(t) { case option.None: ok = false break case option.Some[string] as msg: match mcp.ParseNotification(msg.Value) { case option.Some[mcp.Notification] as notif: notifs = notifs.Append(notif.Value) case option.None: match extractID(msg.Value) { case option.Some[string] as id: if id.Value == "unsub" { break } gotReqIDs = gotReqIDs.Append(id.Value) case option.None: ok = false } } } } let _ = mcp.NotifyResourceUpdated(n, target) let after = mcp.ToolsListChanged() let _ = mcp.Notify(n, after) wantNotifs = wantNotifs.Append(after) if !SendClient(t, "{\"jsonrpc\":\"2.0\",\"id\":\"tail\",\"method\":\"ping\"}") { ok = false } CloseClient(t) let mut sawTail = false for msg in ServerMessages(t) { match mcp.ParseNotification(msg) { case option.Some[mcp.Notification] as notif: notifs = notifs.Append(notif.Value) case option.None: if extractID(msg) == (option.Some[string]{Value: "tail"}) { sawTail = true } else { ok = false } } } let _ = srv.Wait() let finalOK = ok && sawTail && gotReqIDs == reqIDs && notifs == wantNotifs always finalOK sometimes reqIDs.Len() > 0 && wantNotifs.Len() > 1 && finalOK sometimes target != other && finalOK finalOK }`,
	"mcp/stdio DeliversConcurrentNotifications":         `DeliversConcurrentNotifications(kinds []int, key string) { let target = "stdio://res/" + key let other = "stdio://other/" + key let notifier = mcp.NewNotifier(128) let emitTool = mcp.Tool{ Name: "emit", Description: "emits resource and log notifications", Input: json.Object{Members: []json.Member{}}, Call: func(arguments json.Value) json.Value fails mcp.ToolError { let _ = mcp.NotifyResourceUpdated(notifier, target) let _ = mcp.NotifyResourceUpdated(notifier, other) let _ = mcp.Notify(notifier, mcp.LogMessage("info", "stdio", json.Str{Value: key})) json.Str{Value: "ok"} }, } let rs = mcp.StaticResources([]mcp.Resource{ mcp.Resource{URI: target, Name: "item", Description: "", MIMEType: "text/plain"}, }, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError { if uri == target { return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "stdio-body")} } mcp.ResourceNotFound{URI: uri} }) let full = mcp.WithResources(mcp.Server{Name: "stdio-law", Version: "1", Tools: []mcp.Tool{emitTool}}, rs) let mut lines = []string{ "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + stdioQuoted(target) + "}}", } let mut wantIDs = []string{"sub"} let mut emitCount = 0 for i, k in kinds { let mode = ((k % 3) + 3) % 3 let id = "r\(i)" wantIDs = wantIDs.Append(id) if mode == 0 { emitCount = emitCount + 1 lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + stdioQuoted(id) + ",\"method\":\"tools/call\",\"params\":{\"name\":\"emit\",\"arguments\":{}}}") } else if mode == 1 { lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + stdioQuoted(id) + ",\"method\":\"resources/read\",\"params\":{\"uri\":" + stdioQuoted(target) + "}}") } else { lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":" + stdioQuoted(id) + ",\"method\":\"ping\"}") } } lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + stdioQuoted(target) + "}}") lines = lines.Append("{\"jsonrpc\":\"2.0\",\"id\":\"after\",\"method\":\"tools/call\",\"params\":{\"name\":\"emit\",\"arguments\":{}}}") wantIDs = wantIDs.Append("unsub") wantIDs = wantIDs.Append("after") let rec = io.NewRecorder() let tr = NewFrom(io.NewInput(strings.Join(lines, "\n") + "\n"), rec) let readCount = mcp.ServeFull(full, notifier, tr) let raw = strings.TrimSuffix(rec.Text(), "\n") let outLines = if raw == "" { []string{} } else { strings.Split(raw, "\n") } let mut gotIDs = []string{} let mut updatedTarget = 0 let mut updatedOther = 0 let mut logCount = 0 let mut valid = readCount == lines.Len() for ln in outLines { match mcp.ParseNotification(ln) { case option.Some[mcp.Notification] as n: if n.Value.Method == mcp.MethodResourcesUpdated { match mcp.NotificationURI(n.Value) { case option.Some[string] as u: if u.Value == target { updatedTarget = updatedTarget + 1 } else if u.Value == other { updatedOther = updatedOther + 1 } else { valid = false } case option.None: valid = false } } else if n.Value.Method == mcp.MethodMessage { logCount = logCount + 1 } else { valid = false } case option.None: let v = json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, ln) catch { case _: valid = false json.Null } match mcp.Field(v, "id") { case option.Some[json.Value] as iv: match iv.Value { case json.Str as s: gotIDs = gotIDs.Append(s.Value) case _: valid = false } case option.None: valid = false } } } let ok = valid && gotIDs == wantIDs && updatedTarget == emitCount && updatedOther == 0 && logCount == emitCount+1 always ok sometimes emitCount > 0 && ok sometimes emitCount == 0 && ok ok }`,
	"mcp/httpmcp HttpMatchesMemory":                     `HttpMatchesMemory(kinds []int) { let s = lawServer("m") let mut input = []string{} for i, k in kinds { let mode = ((k % 7) + 7) % 7 let msg = match mode { case 0: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}" case 1: "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" case 2: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/list\"}" case 3: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":2}}}" case 4: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i)}}}" case 5: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"missing\",\"arguments\":{}}}" case _: "{\"jsonrpc\":\"2.0\",\"id\":\(i)," } input = input.Append(msg) } let mem = memory.New(input) if mcp.Serve(s, mem) != input.Len() { return false } let ep = New(s, DefaultOptions()) let jsonClient = NewClient(lawInMemoryHandler(ep), "/mcp") let sseClient = NewSSEClient(lawInMemoryHandler(ep), "/mcp") let mut gotJSON = []string{} let mut gotSSE = []string{} for msg in input { let a = jsonClient.Call(msg) catch { case _: return false } match a { case option.Some[string] as line: gotJSON = gotJSON.Append(line.Value) case option.None: } let b = sseClient.Call(msg) catch { case _: return false } match b { case option.Some[string] as line: gotSSE = gotSSE.Append(line.Value) case option.None: } } let want = mem.Sent() gotJSON == want && gotSSE == want }`,
	"mcp/httpmcp ProtocolHeadersAndSecurity":            `ProtocolHeadersAndSecurity(seed int) { let s = lawServer("sec") let opts = Options{ AllowedOrigins: []string{"http://localhost:3000"}, MaxBodyBytes: 96, Sessions: Stateless, } let ep = New(s, opts) let ping = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}" let notif = "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" let okReq = http.Request{ Method: "POST", Path: "/mcp", RawPath: "/mcp", Query: []http.Field{}, Header: []http.Field{ http.Field{Name: "Origin", Value: "HTTP://LOCALHOST:3000/"}, http.Field{Name: "Content-Type", Value: "application/json; charset=utf-8"}, http.Field{Name: "Accept", Value: "application/json"}, }, Body: http.Buffered{Text: ping}, } with context { http.Streaming: http.NewRecorder() } { let okResp = Handle(ep, okReq) if okResp.Status != 200 || http.Header(okResp.Header, ProtocolVersionHeader) != (option.Some[string]{Value: mcp.Version}) { return false } let notifResp = Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: notif}}) if notifResp.Status != 202 || notifResp.Body != "" || http.Header(notifResp.Header, ProtocolVersionHeader) != (option.Some[string]{Value: mcp.Version}) { return false } let evilOrigin = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://evil-\(seed).test"}, http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "application/json"}, }}) if evilOrigin.Status != 403 { return false } let badAccept = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "text/plain"}, }}) if badAccept.Status != 406 { return false } let badCT = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Content-Type", Value: "text/plain"}, http.Field{Name: "Accept", Value: "application/json"}, }}) if badCT.Status != 415 { return false } let tooBig = Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: strings.Repeat("a", 128)}}) if tooBig.Status != 413 { return false } let getResp = Handle(ep, http.Request{..okReq, Method: "GET"}) let delResp = Handle(ep, http.Request{..okReq, Method: "DELETE"}) getResp.Status == 405 && delResp.Status == 405 } }`,
	"mcp/httpmcp SessionLifecycleAndIsolation":          `SessionLifecycleAndIsolation(seed int) { let store = NewSessions(rand.NewSeededSecure(uint64(seed) + 1)) let opts = Options{ AllowedOrigins: []string{}, MaxBodyBytes: 1048576, Sessions: Stateful{Store: store}, } let ep = NewDynamic(opts, func(req http.Request) Resolved { match http.Header(req.Header, "Authorization") { case option.Some[string] as auth: if auth.Value == "Bearer alice" { return Use{Server: lawServer("alice")} } if auth.Value == "Bearer bob" { return Use{Server: lawServer("bob")} } Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}} case option.None: Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}} } }) let net = lawInMemoryHandler(ep) let initMsg = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"c\",\"version\":\"1\"}}}" let whoMsg = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"who\",\"arguments\":{}}}" let anon = NewClient(net, "/mcp") let mut unauthOK = false let _ = anon.Call(initMsg) catch { case UnexpectedStatus as u: unauthOK = u.Status == 401 option.None case _: return false } if !unauthOK { return false } let alice = NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer alice"}}) let bob = NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer bob"}}) let mut missingSessionOK = false let _ = alice.Call(whoMsg) catch { case UnexpectedStatus as u: missingSessionOK = u.Status == 400 option.None case _: return false } if !missingSessionOK { return false } let _ = alice.Call(initMsg) catch { case _: return false } let _ = bob.Call(initMsg) catch { case _: return false } let aliceID = alice.SessionID() let bobID = bob.SessionID() if aliceID == "" || bobID == "" || aliceID == bobID || Count(store) != 2 { return false } let aliceWho = alice.Call(whoMsg) catch { case _: return false } let bobWho = bob.Call(whoMsg) catch { case _: return false } if aliceWho != (option.Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"alice\\\"\"}],\"isError\":false}}"}) { return false } if bobWho != (option.Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"bob\\\"\"}],\"isError\":false}}"}) { return false } alice.Terminate() catch { case _: return false } if Has(store, aliceID) || !Has(store, bobID) { return false } let postDelete = with context { http.Streaming: http.NewRecorder() } { Handle(ep, http.Request{ Method: "POST", Path: "/mcp", RawPath: "/mcp", Query: []http.Field{}, Header: []http.Field{ http.Field{Name: "Authorization", Value: "Bearer alice"}, http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "application/json"}, http.Field{Name: SessionHeader, Value: aliceID}, }, Body: http.Buffered{Text: whoMsg}, }) } postDelete.Status == 404 }`,
	"mcp/httpmcp BatchMatchesMemory":                    `BatchMatchesMemory(kinds []int) { if kinds.Len() == 0 { return true } let s = lawServer("batch") let mut items = []string{} for i, k in kinds { if k%2 == 0 { items = items.Append("{\"jsonrpc\":\"2.0\",\"id\":\"b\(i)\",\"method\":\"ping\"}") } else { items = items.Append("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}") } } let mem = memory.New(items) let _ = mcp.Serve(s, mem) let want = mem.Sent() let ep = New(s, DefaultOptions()) let client = NewClient(lawInMemoryHandler(ep), "/mcp") let batch = "[" + strings.Join(items, ",") + "]" let got = client.Call(batch) catch { case _: return false } if want.Len() == 0 { return got == option.None } got == option.Some[string]{Value: "[" + strings.Join(want, ",") + "]"} }`,
	"mcp/httpmcp GetStreamSecurityAndLifecycle":         `GetStreamSecurityAndLifecycle(seed int) { let store = NewSessions(rand.NewSeededSecure(uint64(seed) + 1)) let opts = Options{ AllowedOrigins: []string{"http://localhost:3000"}, MaxBodyBytes: 1048576, Sessions: Stateful{Store: store}, } let srv = mcp.Server{Name: "get-sec", Version: "1", Tools: []mcp.Tool{}} let ep = NewDynamic(opts, func(req http.Request) Resolved { match http.Header(req.Header, "Authorization") { case option.Some[string] as auth: if auth.Value == "Bearer ok" { return Use{Server: srv} } Reject{Response: http.Response{Status: 401, Header: []http.Field{}, Body: "unauthorized"}} case option.None: Reject{Response: http.Response{Status: 401, Header: []http.Field{}, Body: "unauthorized"}} } }) let net = http.InMemory(func(r http.Request) http.Response needs http.Streaming { Handle(ep, r) }) let c = NewClientWithHeaders(net, "/mcp", []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, }) let _ = c.Initialize("c", "1") catch { case _: return false } let sid = c.SessionID() let baseGet = http.Request{ Method: "GET", Path: "/mcp", RawPath: "/mcp", Query: []http.Field{}, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "text/event-stream"}, http.Field{Name: SessionHeader, Value: sid}, }, Body: http.Buffered{Text: ""}, } let (badOrigin, unauth, badAccept, badVer, missingSid, unknownSid, badMethod) = with context { http.Streaming: http.NewRecorder() } { let rOrigin = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://evil-\(seed).test"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "text/event-stream"}, http.Field{Name: SessionHeader, Value: "does-not-exist"}, }}) let rUnauth = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Accept", Value: "text/event-stream"}, http.Field{Name: SessionHeader, Value: "does-not-exist"}, }}) let rAccept = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "application/json"}, http.Field{Name: SessionHeader, Value: "does-not-exist"}, }}) let rVer = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "text/event-stream"}, http.Field{Name: ProtocolVersionHeader, Value: "1999-01-01"}, http.Field{Name: SessionHeader, Value: "does-not-exist"}, }}) let rNoSid = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "text/event-stream"}, }}) let rBadSid = Handle(ep, http.Request{..baseGet, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://localhost:3000"}, http.Field{Name: "Authorization", Value: "Bearer ok"}, http.Field{Name: "Accept", Value: "text/event-stream"}, http.Field{Name: SessionHeader, Value: "does-not-exist"}, }}) let rPut = Handle(ep, http.Request{..baseGet, Method: "PUT"}) (rOrigin.Status == 403, rUnauth.Status == 401, rAccept.Status == 406, rVer.Status == 400, rNoSid.Status == 400, rBadSid.Status == 404, rPut.Status == 405) } let streamTask = task.Go() { let mut got = []mcp.Notification{} let mut streamReadOK = true c.ReadEvents(func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev.Notification) }) catch { case _: streamReadOK = false } streamReadOK && got.Len() == 1 } if !WaitStream(store, sid) { CloseStreams(store, sid) return false } let _ = Notify(store, mcp.ToolsListChanged()) CloseStreams(store, sid) let streamOK = streamTask.Wait() c.Terminate() catch { case _: return false } let afterDel = with context { http.Streaming: http.NewRecorder() } { Handle(ep, baseGet) } let ok = badOrigin && unauth && badAccept && badVer && missingSid && unknownSid && badMethod && streamOK && afterDel.Status == 404 always ok sometimes streamOK && afterDel.Status == 404 ok }`,
	"mcp/httpmcp SubscriptionAndLastEventIDReplay":      `SubscriptionAndLastEventIDReplay(subKey string, otherKey string, count int) { let subURI = "http://res/" + subKey let otherURI = "http://other/" + otherKey let n = 1 + ((count % 5) + 5) % 5 let store = NewSessions(rand.NewSeededSecure(17)) let rs = mcp.StaticResources([]mcp.Resource{ mcp.Resource{URI: subURI, Name: "sub", Description: "", MIMEType: "text/plain"}, }, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError { if uri == subURI { return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "ok")} } mcp.ResourceNotFound{URI: uri} }) let ep = NewWithResources(mcp.Server{Name: "sse", Version: "1", Tools: []mcp.Tool{}}, rs, Options{ AllowedOrigins: []string{}, MaxBodyBytes: 1048576, Sessions: Stateful{Store: store}, }) let net = http.InMemory(func(r http.Request) http.Response needs http.Streaming { Handle(ep, r) }) let c = NewClient(net, "/mcp") let _ = c.Initialize("c", "1") catch { case _: return false } c.Subscribe(subURI) catch { case _: return false } let sid = c.SessionID() let mut firstExpected = []mcp.Notification{} for i in 0..n { let ev = mcp.ResourceUpdated(subURI) let _ = Notify(store, ev) let _ = NotifyResourceUpdated(store, otherURI) firstExpected = firstExpected.Append(ev) let _ = i } let t1 = task.Go() { let mut got = []NotificationEvent{} c.ReadEvents(func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev) }) catch { case _: got = []NotificationEvent{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let batch1 = t1.Wait() let cursor1 = c.LastEventID() let missed = mcp.LogMessage("notice", "replay", json.Str{Value: subKey}) let _ = Notify(store, missed) c.Unsubscribe(subURI) catch { case _: return false } let _ = NotifyResourceUpdated(store, subURI) let afterUnsub = mcp.ResourcesListChanged() let _ = Notify(store, afterUnsub) let t2 = task.Go() { let mut got = []NotificationEvent{} c.ReadEvents(func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev) }) catch { case _: got = []NotificationEvent{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let batch2 = t2.Wait() let mut batch1Notifs = []mcp.Notification{} for e in batch1 { batch1Notifs = batch1Notifs.Append(e.Notification) } let mut batch2Notifs = []mcp.Notification{} for e in batch2 { batch2Notifs = batch2Notifs.Append(e.Notification) } let tReplay = task.Go() { let mut got = []NotificationEvent{} c.ReadEventsSince(cursor1, func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev) }) catch { case _: got = []NotificationEvent{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let replayBatch = tReplay.Wait() let mut replayNotifs = []mcp.Notification{} for e in replayBatch { replayNotifs = replayNotifs.Append(e.Notification) } let wantBatch2 = []mcp.Notification{missed, afterUnsub} let ok = batch1Notifs == firstExpected && cursor1 != "" && batch2Notifs == wantBatch2 && replayNotifs == wantBatch2 && c.LastEventID() != cursor1 always ok sometimes n > 1 && ok sometimes n == 1 && ok ok }`,
	"mcp/httpmcp TransportsParityForResourcesAndEvents": `TransportsParityForResourcesAndEvents(kinds []int, uriKey string, textBody string) { let uriA = "res://a/" + uriKey let uriB = "res://b/" + uriKey let items = []mcp.Resource{ mcp.Resource{URI: uriA, Name: "doc-a", Description: "first", MIMEType: "text/plain"}, mcp.Resource{URI: uriB, Name: "doc-b", Description: "", MIMEType: "application/octet-stream"}, } let bin = strings.Bytes("bin-" + textBody) let rs = mcp.StaticResources(items, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError { if uri == uriA { return []mcp.ResourceContents{mcp.TextContents(uriA, "text/plain", textBody)} } if uri == uriB { return []mcp.ResourceContents{mcp.BlobContents(uriB, "application/octet-stream", bin)} } mcp.ResourceNotFound{URI: uri} }) let srv = mcp.Server{Name: "parity", Version: "1", Tools: []mcp.Tool{}} let full = mcp.WithResources(srv, rs) let reqMsgs = []string{ "{\"jsonrpc\":\"2.0\",\"id\":\"l\",\"method\":\"resources/list\"}", "{\"jsonrpc\":\"2.0\",\"id\":\"ra\",\"method\":\"resources/read\",\"params\":{\"uri\":" + parityQuoted(uriA) + "}}", "{\"jsonrpc\":\"2.0\",\"id\":\"rb\",\"method\":\"resources/read\",\"params\":{\"uri\":" + parityQuoted(uriB) + "}}", "{\"jsonrpc\":\"2.0\",\"id\":\"rm\",\"method\":\"resources/read\",\"params\":{\"uri\":\"res://missing/x\"}}", } let memTr = memory.New(reqMsgs) let _ = mcp.ServeFull(full, mcp.NewNotifier(8), memTr) let memLines = memTr.Sent() let stdioRec = io.NewRecorder() let stdioTr = stdio.NewFrom(io.NewInput(strings.Join(reqMsgs, "\n") + "\n"), stdioRec) let _ = mcp.ServeFull(full, mcp.NewNotifier(8), stdioTr) let stdioLines = strings.Split(strings.TrimSuffix(stdioRec.Text(), "\n"), "\n") let store = NewSessions(rand.NewSeededSecure(9)) let opts = Options{AllowedOrigins: []string{}, MaxBodyBytes: 1048576, Sessions: Stateful{Store: store}} let ep = NewFull(full, opts) let net = http.InMemory(func(r http.Request) http.Response needs http.Streaming { Handle(ep, r) }) let client = NewClient(net, "/mcp") let init = client.Initialize("parity", "1") catch { case _: return false } let listedHTTP = client.ListResources() catch { case _: return false } let readAHTTP = client.ReadResource(uriA) catch { case _: return false } let readBHTTP = client.ReadResource(uriB) catch { case _: return false } let mut missingCodeHTTP = 0 let _ = client.ReadResource("res://missing/x") catch { case RPCError as re: missingCodeHTTP = re.Code []mcp.ResourceContents{} case _: return false } let mut httpDirectLines = []string{} for m in reqMsgs { let ans = client.Call(m) catch { case _: return false } match ans { case option.Some[string] as s: httpDirectLines = httpDirectLines.Append(s.Value) case option.None: return false } } client.Subscribe(uriA) catch { case _: return false } let memNotif = mcp.NewNotifier(128) let memLive = memory.NewLive(128) let memTask = task.Go() { memory.ServeLive(full, memNotif, memLive) } if !memory.SendClient(memLive, "{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + parityQuoted(uriA) + "}}") { return false } let _ = memory.NextServer(memLive) let stdioScript = []string{"{\"jsonrpc\":\"2.0\",\"id\":\"sub\",\"method\":\"resources/subscribe\",\"params\":{\"uri\":" + parityQuoted(uriA) + "}}"} let stdioNotif = mcp.NewNotifier(128) let stdioSubs = mcp.NewSubscriptions() let stdioSub = mcp.SubscribeWith(stdioNotif, stdioSubs) let _ = mcp.HandleFull(full, stdioSubs, stdioScript[0]) let mut wantNotifs = []mcp.Notification{} let mut phase1Count = 0 for i, k in kinds { let mode = ((k % 3) + 3) % 3 let ev = if mode == 0 { mcp.ResourceUpdated(uriA) } else if mode == 1 { mcp.ResourceUpdated(uriB) } else { mcp.LogMessage("info", "parity", json.IntNumber(int64(i))) } let _ = Notify(store, ev) let _ = mcp.Notify(memNotif, ev) let _ = mcp.Notify(stdioNotif, ev) if mode != 1 { wantNotifs = wantNotifs.Append(ev) if i == kinds.Len()/2 { phase1Count = wantNotifs.Len() } } } let sid = client.SessionID() let reader1 = task.Go() { let mut got = []mcp.Notification{} client.ReadEvents(func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev.Notification) }) catch { case _: got = []mcp.Notification{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let httpStream1 = reader1.Wait() let replayCursor = client.LastEventID() let readerReplay = task.Go() { let mut got = []mcp.Notification{} client.ReadEventsSince("0", func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev.Notification) }) catch { case _: got = []mcp.Notification{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let httpReplay1 = readerReplay.Wait() client.Unsubscribe(uriA) catch { case _: return false } let _ = memory.SendClient(memLive, "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + parityQuoted(uriA) + "}}") let mut memNotifs = []mcp.Notification{} for { match memory.NextServer(memLive) { case option.None: return false case option.Some[string] as ln: match mcp.ParseNotification(ln.Value) { case option.Some[mcp.Notification] as notif: memNotifs = memNotifs.Append(notif.Value) case option.None: break } } } let _ = mcp.HandleFull(full, stdioSubs, "{\"jsonrpc\":\"2.0\",\"id\":\"unsub\",\"method\":\"resources/unsubscribe\",\"params\":{\"uri\":" + parityQuoted(uriA) + "}}") let blockedAfter = mcp.ResourceUpdated(uriA) let afterBroadcast = mcp.ToolsListChanged() let _ = Notify(store, blockedAfter) let _ = Notify(store, afterBroadcast) let _ = mcp.Notify(memNotif, blockedAfter) let _ = mcp.Notify(memNotif, afterBroadcast) let _ = mcp.Notify(stdioNotif, blockedAfter) let _ = mcp.Notify(stdioNotif, afterBroadcast) wantNotifs = wantNotifs.Append(afterBroadcast) let reader2 = task.Go() { let mut got = []mcp.Notification{} client.ReadEvents(func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev.Notification) }) catch { case _: got = []mcp.Notification{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let httpStream2 = reader2.Wait() let readerCursor = task.Go() { let mut got = []mcp.Notification{} client.ReadEventsSince(replayCursor, func(ev NotificationEvent) fails http.SinkFailed { got = got.Append(ev.Notification) }) catch { case _: got = []mcp.Notification{} } got } if !WaitStream(store, sid) { return false } CloseStreams(store, sid) let httpCursorTail = readerCursor.Wait() memory.CloseClient(memLive) for ln in memory.ServerMessages(memLive) { match mcp.ParseNotification(ln) { case option.Some[mcp.Notification] as n: memNotifs = memNotifs.Append(n.Value) case option.None: } } let _ = memTask.Wait() mcp.CloseSubscription(stdioSub) let mut stdioNotifs = []mcp.Notification{} for { match mcp.Next(stdioSub) { case option.Some[mcp.SequencedNotification] as ev: stdioNotifs = stdioNotifs.Append(ev.Value.Notification) case option.None: break } } let _ = phase1Count let capOK = init.Capabilities.Resources == (json.Present{Value: mcp.ResourcesCapability{Subscribe: true, ListChanged: false}}) let reqParity = memLines == stdioLines && memLines == httpDirectLines let resOK = listedHTTP == items && readAHTTP == []mcp.ResourceContents{mcp.TextContents(uriA, "text/plain", textBody)} && readBHTTP == []mcp.ResourceContents{mcp.BlobContents(uriB, "application/octet-stream", bin)} && missingCodeHTTP == mcp.ResourceNotFoundCode let httpAll = httpStream1.Concat(httpStream2) let cursorOK = if replayCursor == "" { httpCursorTail.Len() == 0 } else { httpCursorTail == httpStream2 } let eventParity = memNotifs == wantNotifs && stdioNotifs == wantNotifs && httpAll == wantNotifs && httpReplay1 == httpStream1 && cursorOK let ok = capOK && reqParity && resOK && eventParity always ok sometimes kinds.Len() > 0 && ok sometimes kinds.Len() == 0 && ok ok }`,
}

// TestStdMCPLawsRun runs std mcp's laws as the probe's tests with --seed
// 1, and the memory session; lawledger.Run holds that the probe restates
// exactly the ledger's mcp laws, each with the row's statement, and that
// each passed. The ap2-* and af2-* mutants of the std mutant ledger name
// it. It builds Go because each law is an Ergo test the built probe runs,
// as TestStdJSONSchemaLawsRun's are.
func TestStdMCPLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdmcp"), "--seed", "1")
	if code != 0 {
		t.Errorf("the mcp law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	if !strings.Contains(stdout, "ok MemorySession\n") {
		t.Errorf("no ok line for MemorySession:\n%s", stdout)
	}
	lawledger.Run(t, lawsPassed(t, stdMCPLawStatements, stdLLMLawTestName, stdMCPLawsProbe, stdout, "MemorySession"))
	stdioStopsWhenAsked(t)
}

// stdioStopsWhenAsked runs `ergo test` on std mcp/stdio, where the named
// context overText feeds mcp.Transport.StopsWhenAsked with stdio's
// Transport over io.NewInput and io.NewRecorder: the law that stdio's
// Transport keeps, run under std's own context as a module's laws run,
// not restated by the probe (LAW-UNFED: a fed law stays fed). Mutant
// mcp-stdio-messages-ignore-stop.
//
// It builds Go because the law runs in the test build of std mcp/stdio
// (ergo test), the only place a law under a package's named context runs;
// TestStdMCPLawsRun's probe already needs a built test binary too.
func stdioStopsWhenAsked(t *testing.T) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(moduleRoot, "internal", "sketch", "std", "mcp", "stdio"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ergoCLI(t), "test", dir)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok law mcp.Transport.StopsWhenAsked under overText") {
		t.Errorf("ergo test std mcp/stdio: %v; want the law mcp.Transport.StopsWhenAsked to pass under overText:\n%s", err, out)
	}
}

const stdMCPHttpLoopbackProgram = `import http
import json
import mcp
import mcp/httpmcp
import net
import queue
import rand
import task
import time

func loopbackServer(user string) mcp.Server {
	let echoTool = mcp.Tool{
		Name:        "whoami",
		Description: "returns caller identity",
		Input:       json.Object{Members: []json.Member{}},
		Call: func(_ json.Value) json.Value fails mcp.ToolError {
			json.Str{Value: user}
		},
	}
	mcp.Server{Name: "loopback-" + user, Version: "1.0.0", Tools: []mcp.Tool{echoTool}}
}

func loopbackFullServer(user string) mcp.FullServer {
	let rs = mcp.StaticResources([]mcp.Resource{
		mcp.Resource{URI: "loopback://item/a", Name: "alpha", Description: "first", MIMEType: "text/plain"},
	}, func(uri string) []mcp.ResourceContents fails mcp.ResourceNotFound | mcp.ResourceError {
		if uri == "loopback://item/a" {
			return []mcp.ResourceContents{mcp.TextContents(uri, "text/plain", "alpha-" + user)}
		}
		mcp.ResourceNotFound{URI: uri}
	})
	mcp.WithResources(loopbackServer(user), rs)
}

func main() fails http.InvalidClient | http.InvalidLimits | net.ListenFailed | net.Denied needs Ambient | Console {
	let limits = http.Limits{MaxBodyBytes: 65536, MaxHeaderBytes: 8192, IdleTimeout: 5s, ReadHeaderTimeout: 5s, ReadBodyTimeout: 5s}
	let store = httpmcp.NewSessions(rand.NewSecureHost())
	let opts = httpmcp.Options{
		AllowedOrigins: []string{"http://localhost:3000"},
		MaxBodyBytes:   65536,
		Sessions:       httpmcp.Stateful{Store: store},
	}
	let ep = httpmcp.NewDynamic(opts, func(req http.Request) httpmcp.Resolved {
		match http.Header(req.Header, "Authorization") {
		case Some[string] as auth:
			if auth.Value == "Bearer secret-token" {
				return httpmcp.UseFull{Full: loopbackFullServer("alice")}
			}
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}}
		case None:
			httpmcp.Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}}
		}
	})
	let listener = try net.NewHost().Listen("127.0.0.1:0")
	let origin = "http://" + listener.Addr()
	let server = task.Go() {
		with context { Scheduler: time.NewManual(0) } {
			http.Serve(listener, limits, func(r http.Request) http.Response needs http.Streaming {
				httpmcp.Handle(ep, r)
			}) catch {
			case http.InvalidLimits:
			}
		}
	}
	let httpClient = try http.NewClient(origin, limits, []http.Credential{})
	let initMsg = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"loop\",\"version\":\"1\"}}}"
	let notifMsg = "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}"
	let callMsg = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"whoami\",\"arguments\":{}}}"

	let anon = httpmcp.NewClient(httpClient, "/mcp")
	let _ = anon.Call(initMsg) catch {
	case httpmcp.UnexpectedStatus as u:
		Console.Println("unauth:" + if u.Status == 401 { "401" } else { "wrong" })
		None
	case _:
		Console.Println("unauth:err")
		None
	}

	let evil = httpmcp.NewClientWithHeaders(httpClient, "/mcp", []http.Field{
		http.Field{Name: "Authorization", Value: "Bearer secret-token"},
		http.Field{Name: "Origin", Value: "http://evil.example"},
	})
	let _ = evil.Call(initMsg) catch {
	case httpmcp.UnexpectedStatus as u:
		Console.Println("origin:" + if u.Status == 403 { "403" } else { "wrong" })
		None
	case _:
		Console.Println("origin:err")
		None
	}

	let authed = httpmcp.NewClientWithHeaders(httpClient, "/mcp", []http.Field{
		http.Field{Name: "Authorization", Value: "Bearer secret-token"},
		http.Field{Name: "Origin", Value: "http://localhost:3000"},
	})
	let initResp = authed.Call(initMsg) catch {
	case _:
		Console.Println("init:err")
		server.Stop()
		return
	}
	let sid = authed.SessionID()
	Console.Println("init:" + if initResp != None && sid != "" { "ok" } else { "bad" })

	let notifResp = authed.Call(notifMsg) catch {
	case _:
		Console.Println("notif:err")
		server.Stop()
		return
	}
	Console.Println("notif:" + if notifResp == None { "202" } else { "bad" })

	let callResp = authed.Call(callMsg) catch {
	case _:
		Console.Println("call:err")
		server.Stop()
		return
	}
	let wantCall = Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"alice\\\"\"}],\"isError\":false}}"}
	Console.Println("call:" + if callResp == wantCall { "ok" } else { "bad" })

	let sseResp = httpClient.Send(http.Request{
		Method:  "POST",
		Path:    "/mcp",
		RawPath: "/mcp",
		Query:   []http.Field{},
		Header: []http.Field{
			http.Field{Name: "Authorization", Value: "Bearer secret-token"},
			http.Field{Name: "Content-Type", Value: "application/json"},
			http.Field{Name: "Accept", Value: "text/event-stream"},
			http.Field{Name: httpmcp.ProtocolVersionHeader, Value: mcp.Version},
			http.Field{Name: httpmcp.SessionHeader, Value: sid},
		},
		Body: http.Buffered{Text: callMsg},
	}) catch {
	case _:
		Console.Println("sse:err")
		server.Stop()
		return
	}
	let sseEvents = http.ReadEvents(sseResp.Body) catch {
	case http.Truncated:
		[]http.ServerEvent{}
	}
	Console.Println("sse:" + if sseResp.Status == 200 && len(sseEvents) == 1 && (Some[string]{Value: sseEvents[0].Data}) == wantCall { "ok" } else { "bad" })

	let items = authed.ListResources() catch {
	case _:
		Console.Println("res:err")
		server.Stop()
		return
	}
	let contents = authed.ReadResource("loopback://item/a") catch {
	case _:
		Console.Println("res:err")
		server.Stop()
		return
	}
	let resOK = len(items) == 1 && items[0].URI == "loopback://item/a" && contents == []mcp.ResourceContents{mcp.TextContents("loopback://item/a", "text/plain", "alpha-alice")}
	Console.Println("res:" + if resOK { "ok" } else { "bad" })

	authed.Subscribe("loopback://item/a") catch {
	case _:
		Console.Println("sub:err")
		server.Stop()
		return
	}
	let sseQ = queue.New[httpmcp.NotificationEvent](16)
	let sseTask = task.Go() {
		let mut ok = true
		authed.ReadEvents(func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			sseQ.Push(ev) catch {
			case queue.Closed:
			}
		}) catch {
		case _:
			ok = false
		}
		sseQ.End()
		ok
	}
	if !httpmcp.WaitStream(store, sid) {
		Console.Println("push:nostream")
		server.Stop()
		return
	}
	let _ = httpmcp.NotifyResourceUpdated(store, "loopback://item/a")
	let _ = httpmcp.NotifyResourceUpdated(store, "loopback://item/other")
	let logEv = mcp.LogMessage("info", "loop", json.Str{Value: "live"})
	let _ = httpmcp.Notify(store, logEv)
	let mut pushed = []mcp.Notification{}
	for _ in 0..2 {
		let ev = sseQ.Pop() catch {
		case queue.Closed:
			break
		}
		pushed = pushed.Append(ev.Notification)
	}
	httpmcp.CloseStreams(store, sid)
	let streamClosedClean = sseTask.Wait()
	let cursor1 = authed.LastEventID()
	let pushOK = streamClosedClean && cursor1 != "" && pushed == []mcp.Notification{mcp.ResourceUpdated("loopback://item/a"), logEv}
	Console.Println("push:" + if pushOK { "ok" } else { "bad" })

	let missed = mcp.LogMessage("notice", "loop", json.Str{Value: "missed"})
	let _ = httpmcp.Notify(store, missed)
	authed.Unsubscribe("loopback://item/a") catch {
	case _:
		Console.Println("replay:unsuberr")
		server.Stop()
		return
	}
	let _ = httpmcp.NotifyResourceUpdated(store, "loopback://item/a")
	let afterBroadcast = mcp.ResourcesListChanged()
	let _ = httpmcp.Notify(store, afterBroadcast)

	let replayQ = queue.New[httpmcp.NotificationEvent](16)
	let replayTask = task.Go() {
		let mut ok = true
		authed.ReadEventsSince(cursor1, func(ev httpmcp.NotificationEvent) fails http.SinkFailed {
			replayQ.Push(ev) catch {
			case queue.Closed:
			}
		}) catch {
		case _:
			ok = false
		}
		replayQ.End()
		ok
	}
	if !httpmcp.WaitStream(store, sid) {
		Console.Println("replay:nostream")
		server.Stop()
		return
	}
	let mut replayed = []mcp.Notification{}
	for _ in 0..2 {
		let ev = replayQ.Pop() catch {
		case queue.Closed:
			break
		}
		replayed = replayed.Append(ev.Notification)
	}
	httpmcp.CloseStreams(store, sid)
	let replayClosedClean = replayTask.Wait()
	let replayOK = replayClosedClean && replayed == []mcp.Notification{missed, afterBroadcast} && authed.LastEventID() != cursor1
	Console.Println("replay:" + if replayOK { "ok" } else { "bad" })

	let delStatus = with context { Scheduler: NewLiveScheduler() } {
		let delStream = task.Go() {
			let mut count = 0
			let mut ok = true
			authed.ReadEvents(func(_ httpmcp.NotificationEvent) fails http.SinkFailed {
				count = count + 1
			}) catch {
			case _:
				ok = false
			}
			ok && count == 0
		}
		if !httpmcp.WaitStream(store, sid) {
			delStream.Stop()
			"nostream"
		} else {
			let mut termOK = true
			authed.Terminate() catch {
			case _:
				termOK = false
			}
			if !termOK {
				delStream.Stop()
				"err"
			} else {
				let alarm = task.StopAfter(delStream, 1500ms)
				let res = match task.ExitOf(delStream) {
				case task.Done[bool] as ok:
					if ok.Value { "204" } else { "keepstream" }
				case _:
					"keepstream"
				}
				let _ = alarm.Cancel()
				res
			}
		}
	}
	Console.Println("del:" + delStatus)
	if delStatus != "204" {
		server.Stop()
		return
	}

	let postDel = httpClient.Send(http.Request{
		Method:  "POST",
		Path:    "/mcp",
		RawPath: "/mcp",
		Query:   []http.Field{},
		Header: []http.Field{
			http.Field{Name: "Authorization", Value: "Bearer secret-token"},
			http.Field{Name: "Content-Type", Value: "application/json"},
			http.Field{Name: "Accept", Value: "application/json"},
			http.Field{Name: httpmcp.ProtocolVersionHeader, Value: mcp.Version},
			http.Field{Name: httpmcp.SessionHeader, Value: sid},
		},
		Body: http.Buffered{Text: callMsg},
	}) catch {
	case _:
		Console.Println("postdel:err")
		server.Stop()
		return
	}
	Console.Println("postdel:" + if postDel.Status == 404 { "404" } else { "bad" })
	server.Stop()
}
`

// TestStdMCPHttpLoopback verifies std mcp/httpmcp end-to-end over a live
// loopback TCP http.Serve listener and http.NewClient: Bearer rejection (401),
// Origin rejection (403), stateful initialize + Mcp-Session-Id minting,
// notification 202 Accepted, tools/call over JSON and SSE, resources/list and
// resources/read, live GET /mcp SSE push with subscription filtering,
// Last-Event-ID replay, DELETE termination (204), and post-DELETE rejection (404).
func TestStdMCPHttpLoopback(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdmcphttp"))
	want := "unauth:401\norigin:403\ninit:ok\nnotif:202\ncall:ok\nsse:ok\nres:ok\npush:ok\nreplay:ok\ndel:204\npostdel:404\n"
	if code != 0 || stdout != want {
		t.Fatalf("stdmcphttp: exit=%d\ngot:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, want, firstLines(stderr, 20))
	}
}

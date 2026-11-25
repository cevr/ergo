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
import json
import mcp
import mcp/httpmcp
import mcp/memory
import rand
import strings

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
	let postDelete = httpmcp.Handle(ep, http.Request{
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
	"mcp ListNamesEveryTool":                  `ListNamesEveryTool(names []string) { match Handle(serverOf(names), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}") { case Answered as a: let listing = json.Decode(json.Codec[Listing]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch { case _: return false } let mut got = []string{} for t in listing.Tools { got = got.Append(t.Name) } got == names case Refused, Silent: false } }`,
	"mcp UnknownToolIsATypedError":            `UnknownToolIsATypedError(names []string, asked string) { for n in names { if n == asked { return true } } let s = serverOf(names) let mut failed = false let _ = Call(s, asked, json.Null) catch { case UnknownTool as u: failed = u.Name == asked json.Null case ToolError: json.Null } let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":" + quoted(asked) + "}}" failed && Handle(s, message) == Refused{ID: json.IntNumber(7), Code: InvalidParams, Message: "unknown tool: \(asked)"} }`,
	"mcp ArgumentsOutsideTheSchemaAreRefused": `ArgumentsOutsideTheSchemaAreRefused(key string, keys []string) { let schema = parsed("{\"type\":\"object\",\"required\":[" + quoted(key) + "]}") let s = Server{Name: "laws", Version: "1", Tools: []Tool{Tool{Name: "t", Description: "t", Input: schema, Call: func(arguments json.Value) json.Value fails ToolError { json.Str{Value: "ran"} }}}} let mut members = []string{} let mut has = false for k in keys { members = members.Append(quoted(k) + ":null") has = has || k == key } let message = "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":\"t\",\"arguments\":{" + strings.Join(members, ",") + "}}}" match Handle(s, message) { case Answered: has case Refused as r: !has && r.Code == InvalidParams case Silent: false } }`,
	"mcp Transport.StopsWhenAsked":            `StopsWhenAsked() { let mut n = 0 for _ in Messages() { n = n + 1 break } n <= 1 }`,
	"mcp/memory CallAnswersOnce":                 `CallAnswersOnce(kinds []int) { let mut input = []string{} let mut ids = []string{} for i, k in kinds { let method = match ((k % 3) + 3) % 3 { case 0: "tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":1}}" case 1: "tools/list\"" case _: "ping\"" } if k%2 == 0 { input = input.Append("{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"" + method + "}") ids = ids.Append("r\(i)") } else { input = input.Append("{\"jsonrpc\":\"2.0\",\"method\":\"" + method + "}") } } let t = New(input) if mcp.Serve(calc, t) != len(kinds) { return false } let sent = t.Sent() if len(sent) != len(ids) { return false } for i, line in sent { if !strings.HasPrefix(line, "{\"jsonrpc\":\"2.0\",\"id\":\"\(ids[i])\",\"result\":") { return false } } true }`,
	"mcp/httpmcp HttpMatchesMemory":              `HttpMatchesMemory(kinds []int) { let s = lawServer("m") let mut input = []string{} for i, k in kinds { let mode = ((k % 7) + 7) % 7 let msg = match mode { case 0: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}" case 1: "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" case 2: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/list\"}" case 3: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":2}}}" case 4: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i)}}}" case 5: "{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"tools/call\",\"params\":{\"name\":\"missing\",\"arguments\":{}}}" case _: "{\"jsonrpc\":\"2.0\",\"id\":\(i)," } input = input.Append(msg) } let mem = memory.New(input) if mcp.Serve(s, mem) != len(input) { return false } let ep = New(s, DefaultOptions()) let jsonClient = NewClient(lawInMemoryHandler(ep), "/mcp") let sseClient = NewSSEClient(lawInMemoryHandler(ep), "/mcp") let mut gotJSON = []string{} let mut gotSSE = []string{} for msg in input { let a = jsonClient.Call(msg) catch { case _: return false } match a { case Some[string] as line: gotJSON = gotJSON.Append(line.Value) case None: } let b = sseClient.Call(msg) catch { case _: return false } match b { case Some[string] as line: gotSSE = gotSSE.Append(line.Value) case None: } } let want = mem.Sent() gotJSON == want && gotSSE == want }`,
	"mcp/httpmcp ProtocolHeadersAndSecurity":     `ProtocolHeadersAndSecurity(seed int) { let s = lawServer("sec") let opts = Options{ AllowedOrigins: []string{"http://localhost:3000"}, MaxBodyBytes: 96, Sessions: Stateless, } let ep = New(s, opts) let ping = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}" let notif = "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}" let okReq = http.Request{ Method: "POST", Path: "/mcp", RawPath: "/mcp", Query: []http.Field{}, Header: []http.Field{ http.Field{Name: "Origin", Value: "HTTP://LOCALHOST:3000/"}, http.Field{Name: "Content-Type", Value: "application/json; charset=utf-8"}, http.Field{Name: "Accept", Value: "application/json"}, }, Body: http.Buffered{Text: ping}, } let okResp = Handle(ep, okReq) if okResp.Status != 200 || http.Header(okResp.Header, ProtocolVersionHeader) != (Some[string]{Value: mcp.Version}) { return false } let notifResp = Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: notif}}) if notifResp.Status != 202 || notifResp.Body != "" || http.Header(notifResp.Header, ProtocolVersionHeader) != (Some[string]{Value: mcp.Version}) { return false } let evilOrigin = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Origin", Value: "http://evil-\(seed).test"}, http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "application/json"}, }}) if evilOrigin.Status != 403 { return false } let badAccept = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "text/plain"}, }}) if badAccept.Status != 406 { return false } let badCT = Handle(ep, http.Request{..okReq, Header: []http.Field{ http.Field{Name: "Content-Type", Value: "text/plain"}, http.Field{Name: "Accept", Value: "application/json"}, }}) if badCT.Status != 415 { return false } let tooBig = Handle(ep, http.Request{..okReq, Body: http.Buffered{Text: strings.Repeat("a", 128)}}) if tooBig.Status != 413 { return false } let getResp = Handle(ep, http.Request{..okReq, Method: "GET"}) let delResp = Handle(ep, http.Request{..okReq, Method: "DELETE"}) getResp.Status == 405 && delResp.Status == 405 }`,
	"mcp/httpmcp SessionLifecycleAndIsolation":   `SessionLifecycleAndIsolation(seed int) { let store = NewSessions(rand.NewSeededSecure(uint64(seed) + 1)) let opts = Options{ AllowedOrigins: []string{}, MaxBodyBytes: 1048576, Sessions: Stateful{Store: store}, } let ep = NewDynamic(opts, func(req http.Request) Resolved { match http.Header(req.Header, "Authorization") { case Some[string] as auth: if auth.Value == "Bearer alice" { return Use{Server: lawServer("alice")} } if auth.Value == "Bearer bob" { return Use{Server: lawServer("bob")} } Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}} case None: Reject{Response: http.Response{Status: 401, Header: []http.Field{http.Field{Name: "WWW-Authenticate", Value: "Bearer"}}, Body: "unauthorized"}} } }) let net = lawInMemoryHandler(ep) let initMsg = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"c\",\"version\":\"1\"}}}" let whoMsg = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"who\",\"arguments\":{}}}" let anon = NewClient(net, "/mcp") let mut unauthOK = false let _ = anon.Call(initMsg) catch { case UnexpectedStatus as u: unauthOK = u.Status == 401 None case _: return false } if !unauthOK { return false } let alice = NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer alice"}}) let bob = NewClientWithHeaders(net, "/mcp", []http.Field{http.Field{Name: "Authorization", Value: "Bearer bob"}}) let mut missingSessionOK = false let _ = alice.Call(whoMsg) catch { case UnexpectedStatus as u: missingSessionOK = u.Status == 400 None case _: return false } if !missingSessionOK { return false } let _ = alice.Call(initMsg) catch { case _: return false } let _ = bob.Call(initMsg) catch { case _: return false } let aliceID = alice.SessionID() let bobID = bob.SessionID() if aliceID == "" || bobID == "" || aliceID == bobID || Count(store) != 2 { return false } let aliceWho = alice.Call(whoMsg) catch { case _: return false } let bobWho = bob.Call(whoMsg) catch { case _: return false } if aliceWho != (Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"alice\\\"\"}],\"isError\":false}}"}) { return false } if bobWho != (Some[string]{Value: "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"\\\"bob\\\"\"}],\"isError\":false}}"}) { return false } alice.Terminate() catch { case _: return false } if Has(store, aliceID) || !Has(store, bobID) { return false } let postDelete = Handle(ep, http.Request{ Method: "POST", Path: "/mcp", RawPath: "/mcp", Query: []http.Field{}, Header: []http.Field{ http.Field{Name: "Authorization", Value: "Bearer alice"}, http.Field{Name: "Content-Type", Value: "application/json"}, http.Field{Name: "Accept", Value: "application/json"}, http.Field{Name: SessionHeader, Value: aliceID}, }, Body: http.Buffered{Text: whoMsg}, }) postDelete.Status == 404 }`,
	"mcp/httpmcp BatchMatchesMemory":             `BatchMatchesMemory(kinds []int) { if len(kinds) == 0 { return true } let s = lawServer("batch") let mut items = []string{} for i, k in kinds { if k%2 == 0 { items = items.Append("{\"jsonrpc\":\"2.0\",\"id\":\"b\(i)\",\"method\":\"ping\"}") } else { items = items.Append("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}") } } let mem = memory.New(items) let _ = mcp.Serve(s, mem) let want = mem.Sent() let ep = New(s, DefaultOptions()) let client = NewClient(lawInMemoryHandler(ep), "/mcp") let batch = "[" + strings.Join(items, ",") + "]" let got = client.Call(batch) catch { case _: return false } if len(want) == 0 { return got == None } got == Some[string]{Value: "[" + strings.Join(want, ",") + "]"} }`,
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
				return httpmcp.Use{Server: loopbackServer("alice")}
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

	authed.Terminate() catch {
	case _:
		Console.Println("del:err")
		server.Stop()
		return
	}
	Console.Println("del:204")

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
// notification 202 Accepted, tools/call over JSON and SSE, DELETE termination
// (204), and post-DELETE rejection (404).
func TestStdMCPHttpLoopback(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdmcphttp"))
	want := "unauth:401\norigin:403\ninit:ok\nnotif:202\ncall:ok\nsse:ok\ndel:204\npostdel:404\n"
	if code != 0 || stdout != want {
		t.Fatalf("stdmcphttp: exit=%d\ngot:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, want, firstLines(stderr, 20))
	}
}

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
// Transport over io.InOver and io.HostOver), which TestStdMCPLawsRun
// runs with ergo test (stdioStopsWhenAsked).
const stdMCPLawsProbe = `import json
import mcp
import mcp/memory
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
	"mcp/memory CallAnswersOnce":              `CallAnswersOnce(kinds []int) { let mut input = []string{} let mut ids = []string{} for i, k in kinds { let method = match ((k % 3) + 3) % 3 { case 0: "tools/call\",\"params\":{\"name\":\"add\",\"arguments\":{\"a\":\(i),\"b\":1}}" case 1: "tools/list\"" case _: "ping\"" } if k%2 == 0 { input = input.Append("{\"jsonrpc\":\"2.0\",\"id\":\"r\(i)\",\"method\":\"" + method + "}") ids = ids.Append("r\(i)") } else { input = input.Append("{\"jsonrpc\":\"2.0\",\"method\":\"" + method + "}") } } let t = New(input) if mcp.Serve(calc, t) != len(kinds) { return false } let sent = t.Sent() if len(sent) != len(ids) { return false } for i, line in sent { if !strings.HasPrefix(line, "{\"jsonrpc\":\"2.0\",\"id\":\"\(ids[i])\",\"result\":") { return false } } true }`,
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
// Transport over io.InOver and io.HostOver: the law that stdio's
// Transport keeps, run under std's own context as a module's laws run,
// not restated by the probe (LAW-UNFED: a fed law stays fed). Mutant
// mcp-stdio-messages-ignore-stop.
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

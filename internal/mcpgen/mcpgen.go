// Package mcpgen writes the Ergo source that puts an operation type on the
// Model Context Protocol (`ergo gen mcp`; docs/spec/api.md, ergo gen mcp).
// The output is ordinary Ergo over std mcp (moved from modules/mcp, MCP0), std rpc's
// JSON and the contract's hub (`ergo gen api`), in the package <pkg>mcp
// beside the contract's package, in one file, mcp.ergo:
//
//   - Request, the hub's Request (one call has one type on every surface);
//   - a tool per operation: its name (the hub's Name), its description (the
//     hub's Doc: the operation's doc comment, or its signature), and the
//     JSON schema of its arguments (the hub's Schema, Input);
//   - Call(name, arguments) Request and Arguments(r), its other half, with
//     the laws ArgumentsRoundTrip and ArgumentsFitTheSchema (mcp.Conforms);
//   - Answer(r), which answers r through the hub's Call on the bound
//     provider: the value as JSON, a typed failure as an mcp.ToolError
//     whose message is the failure's tagged JSON (the wire's);
//   - Tools(), the mcp.Tool of each operation on the provider in force:
//     the tools share a held context (InForce). If its with built or lent
//     a resource, calling a tool after that with ended is the held-context
//     defect.
//
// An operation whose only parameter is a record takes that record's
// fields as its arguments; any other takes its parameters by their wire
// keys (rpcgen.Flat). The file carries the contract's digest, and
// `--check` regenerates and compares, as `ergo gen api` does.
package mcpgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// ModulePath is the import path of std mcp, which the generated package
// imports; it is std, so the contract's module requires nothing for it.
const ModulePath = "mcp"

// Files is the generated package: its folder, relative to the contract's
// package folder, and its files by name.
type Files = rpcgen.Files

// Generate writes the mcp package of the operation type opName of pkg,
// whose import path is path, over its hub at the import path apiPath.
func Generate(pkg *check.Package, path, opName, apiPath string) (Files, error) {
	if apiPath == "" {
		return Files{}, fmt.Errorf("the mcp package is written over the hub; run ergo gen api first")
	}
	ops, digest, err := rpcgen.Contract(pkg, path, opName)
	if err != nil {
		return Files{}, err
	}
	// The hub's Schema refuses what has no JSON schema; the tools need one.
	if _, err := rpcgen.GenerateAPI(pkg, path, opName); err != nil {
		return Files{}, err
	}
	pkgName := lastElem(path)
	api := lastElem(apiPath)
	g := &gen{pkg: pkg, path: path, op: opName, api: api, rq: api + ".", imports: map[string]string{"json": "json", "rpc": "rpc", "scope": "scope", ModulePath: "mcp", apiPath: api}}
	g.importOf(pkg)
	g.stdAPI = rpcgen.StdAPIName(g.imports)
	g.imports["api"] = g.stdAPI
	var tools []*tool
	names := map[string]string{}
	for _, op := range ops {
		t := &tool{op: op, name: rpcgen.Kebab(op.Name), flat: rpcgen.Flat(op)}
		if other, ok := names[t.name]; ok {
			return Files{}, fmt.Errorf("%s.%s and %s.%s have one tool name, %s", opName, other, opName, op.Name, t.name)
		}
		names[t.name] = op.Name
		tools = append(tools, t)
	}
	body := g.file(ops, tools)
	for _, name := range g.imports {
		if reserved()[name] {
			return Files{}, fmt.Errorf("%s: the package %s is one the generated code names as a local; rename it", opName, name)
		}
	}
	src := g.importBlock(body) + "\n" + body
	out, err := rpcgen.Source(src)
	if err != nil {
		return Files{}, fmt.Errorf("mcpgen: mcp.ergo does not format: %v\n%s", err, src)
	}
	return Files{Dir: pkgName + "mcp", Files: map[string]string{"mcp.ergo": out}, Marker: rpcgen.Marker("mcp", path+"."+opName, digest)}, nil
}

// reserved are the locals the generated functions use.
var reserved = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"name": true, "arguments": true, "r": true, "x": true, "v": true, "e": true, "t": true, "p": true, "text": true, "back": true, "a": true, "done": true, "rejected": true, "reply": true, "failure": true, "o": true, "c": true, "w": true, "g": true, "answer": true, "result": true, "tools": true, "server": true, "message": true, "carry": true, "toolOver": true}
})

func lastElem(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

type gen struct {
	pkg  *check.Package
	path string
	op   string
	api  string // the hub's package name
	rq   string // the hub's qualifier, "<pkg>api."
	// stdAPI is the name of std api.
	stdAPI string
	// imports are every package the file may name, by path, with the name
	// source uses for it.
	imports map[string]string
}

// importOf records p's import and is the name source uses for it.
func (g *gen) importOf(p *check.Package) string {
	if p == nil || p.Path == stdlib.PreludePath {
		return ""
	}
	if p == g.pkg {
		name := lastElem(g.path)
		g.imports[g.path] = name
		return name
	}
	g.imports[p.Path] = p.Name
	return p.Name
}

// expr is t written in source of the generated package.
func (g *gen) expr(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		return u.Name
	case *check.Named:
		if a := u.Alias(); a != nil {
			return g.expr(a)
		}
		name := u.Obj.Name
		if q := g.importOf(u.Obj.Pkg); q != "" {
			name = q + "." + name
		}
		if len(u.TArgs) > 0 {
			var args []string
			for _, a := range u.TArgs {
				args = append(args, g.expr(a))
			}
			name += "[" + strings.Join(args, ", ") + "]"
		}
		return name
	case *check.Slice:
		return "[]" + g.expr(u.Elem)
	case *check.Map:
		return "Map[" + g.expr(u.Key) + ", " + g.expr(u.Elem) + "]"
	case *check.Tuple:
		var parts []string
		for _, e := range u.Types {
			parts = append(parts, g.expr(e))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	return t.String()
}

func unalias(t check.Type) check.Type {
	for {
		n, ok := t.(*check.Named)
		if !ok || n.Alias() == nil {
			return t
		}
		t = n.Alias()
	}
}

// tool is the plan of one operation's tool.
type tool struct {
	op   rpcgen.Op
	name string
	// flat is the wire key of the operation's only parameter when it is a
	// record whose fields are the arguments; "" otherwise.
	flat string
}

// lit is s as an Ergo string literal: `\`, `"` and control characters
// escaped, and no `\(`, which would interpolate.
func lit(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// failTag is the "_tag" a record failure writes; "" for a union.
func failTag(t check.Type) string {
	n, ok := unalias(t).(*check.Named)
	if !ok {
		return ""
	}
	if _, ok := n.Underlying().(*check.Union); ok {
		return ""
	}
	return n.Obj.Name
}

// file is mcp.ergo's declarations.
func (g *gen) file(ops []rpcgen.Op, tools []*tool) string {
	capName := g.importOf(g.pkg) + "." + g.op
	var b strings.Builder
	fmt.Fprintf(&b, "// Request is one call of %s: the hub's Request.\ntype Request = %sRequest\n\n", capName, g.rq)
	b.WriteString("// valueOf is the JSON value text spells: the wire's text, so JSON.\nfunc valueOf(text string) json.Value {\njson.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {\ncase _:\njson.Null\n}\n}\n\n")
	b.WriteString("// textOf is v's JSON text.\nfunc textOf(v json.Value) string { json.Encode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, v) }\n\n")
	b.WriteString("// untagged is v without its \"_tag\" member: a variant's record as a\n// tool's arguments.\nfunc untagged(v json.Value) json.Value {\nmatch v {\ncase json.Object as o:\nlet mut members = []json.Member{}\nfor m in o.Members {\nif m.Key != \"_tag\" {\nmembers = members.Append(m)\n}\n}\njson.Object{Members: members}\ncase json.Null, json.Boolean, json.Num, json.Str, json.Array:\nv\n}\n}\n\n")

	var names []string
	for _, t := range tools {
		names = append(names, lit(t.name))
	}
	fmt.Fprintf(&b, "// Names are the tools' names, one per operation of %s, in its order.\nlet Names = []string{%s}\n\n", capName, strings.Join(names, ", "))

	b.WriteString("// Description is what the tool name does: its operation's doc comment,\n// or its signature (the hub's Doc).\nfunc Description(name string) string {\nmatch name {\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "case %s:\n%sDoc(%s%sOp)\n", lit(t.name), g.rq, g.rq, t.op.Name)
	}
	b.WriteString("case _:\n\"\"\n}\n}\n\n")

	b.WriteString("// Input is the JSON schema of the tool name's arguments (the hub's\n// Schema; an object schema with no properties for a name that is no\n// tool's).\nfunc Input(name string) json.Value {\nmatch name {\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "case %s:\n%sSchema(%s%sOp)\n", lit(t.name), g.rq, g.rq, t.op.Name)
	}
	b.WriteString("case _:\nvalueOf(\"{\\\"type\\\":\\\"object\\\",\\\"properties\\\":{}}\")\n}\n}\n\n")

	b.WriteString("// Call is the Request that a call of the tool name with arguments asks\n// for: arguments that are not the tool's fail ToolError, a name that is\n// no tool's UnknownTool.\nfunc Call(name string, arguments json.Value) Request fails mcp.ToolError | mcp.UnknownTool {\nlet text = match name {\n")
	for _, t := range tools {
		payload := "textOf(arguments)"
		if t.flat != "" {
			payload = fmt.Sprintf("%s + textOf(arguments) + \"}\"", lit("{"+quote(t.flat)+":"))
		}
		fmt.Fprintf(&b, "case %s:\nrpc.Tagged(%q, %s)\n", lit(t.name), t.op.Name+"Request", payload)
	}
	b.WriteString("case _:\nreturn mcp.UnknownTool{Name: name}\n}\n")
	b.WriteString("rpc.Decode[Request](\"arguments of \" + name, text) catch {\ncase rpc.Transport as e:\nreturn mcp.ToolError{Message: e.Reason}\n}\n}\n\n")

	b.WriteString("// Arguments is the tool and the arguments that ask for r: Call reads\n// them as r again.\nfunc Arguments(r Request) (string, json.Value) {\nmatch r {\n")
	for _, t := range tools {
		v := g.rq + t.op.Name + "Request"
		switch {
		case len(t.op.Params) == 0:
			fmt.Fprintf(&b, "case %s:\n(%s, json.Object{Members: []json.Member{}})\n", v, lit(t.name))
		case t.flat != "":
			fmt.Fprintf(&b, "case %s as x:\n(%s, untagged(valueOf(rpc.Encode(x.%s))))\n", v, lit(t.name), rpcgen.FieldName(t.op.Params[0].Name))
		default:
			fmt.Fprintf(&b, "case %s as x:\n(%s, untagged(valueOf(rpc.Encode(x))))\n", v, lit(t.name))
		}
	}
	b.WriteString("}\n}\n\n")

	fmt.Fprintf(&b, "// Show is the outcome o as a tool's answer: its value as JSON (null for\n// none), or its typed failure as a ToolError whose message is the\n// failure's tagged JSON.\nfunc Show(o %sOutcome) json.Value fails mcp.ToolError {\n", g.rq)
	fmt.Fprintf(&b, "match o {\ncase %sDone as done:\nmatch done.Reply {\n", g.rq)
	for _, t := range tools {
		reply := g.rq + t.op.Name + "Reply"
		if t.op.Result == nil {
			fmt.Fprintf(&b, "case %s:\njson.Null\n", reply)
		} else {
			fmt.Fprintf(&b, "case %s as reply:\nvalueOf(rpc.Encode(reply.Value))\n", reply)
		}
	}
	fmt.Fprintf(&b, "}\ncase %sRejected as rejected:\nmatch rejected.Failure {\n", g.rq)
	fails := rpcgen.Failures(ops)
	for _, f := range fails {
		fmt.Fprintf(&b, "case %s%s as failure:\n", g.rq, rpcgen.FailureVariant(ops, f))
		if tag := failTag(f); tag != "" {
			fmt.Fprintf(&b, "return mcp.ToolError{Message: rpc.Tagged(%q, rpc.Encode(failure.E))}\n", tag)
		} else {
			b.WriteString("return mcp.ToolError{Message: rpc.Encode(failure.E)}\n")
		}
	}
	if len(fails) == 0 {
		fmt.Fprintf(&b, "case %s%s:\nreturn mcp.ToolError{Message: \"the hub answered NoFailure, which no operation fails\"}\n", g.rq, rpcgen.NoFailure)
	}
	b.WriteString("}\n}\n}\n\n")

	fmt.Fprintf(&b, "// Answer answers r through the hub's Call on the bound %s (Show).\nfunc Answer(r Request) json.Value fails mcp.ToolError needs %s {\ntry Show(%sCall(r))\n}\n\n", capName, capName, g.rq)

	fmt.Fprintf(&b, "// ReadAnswer is the outcome of r that a tool's result says: its value's\n// JSON, or, when it is an error, an admitted failure's tagged JSON.\n// Invalid or missing replies fail Transport, outside the operation's own row.\nfunc ReadAnswer(r Request, result mcp.Called) %sOutcome fails rpc.Transport {\n", g.rq)
	b.WriteString("if result.Content.Len() != 1 {\nreturn rpc.Transport{Reason: \"a tool answered \\(result.Content.Len()) blocks, not one\"}\n}\nlet text = result.Content[0].Text\nif !result.IsError {\nmatch r {\n")
	for _, t := range tools {
		op := t.op
		req := g.rq + op.Name + "Request"
		reply := g.rq + op.Name + "Reply"
		if op.Result == nil {
			fmt.Fprintf(&b, "case %s:\nif text != \"null\" {\nreturn rpc.Transport{Reason: \"the tool %s answered \" + text + \", not null\"}\n}\n%sDone{Reply: %s}\n", req, t.name, g.rq, reply)
			continue
		}
		fmt.Fprintf(&b, "case %s:\nlet v = try rpc.Decode[%s](\"value\", text)\n%sDone{Reply: %s{Value: v}}\n", req, g.expr(op.Result), g.rq, reply)
	}
	b.WriteString("}\n} else {\nmatch r {\n")
	for _, t := range tools {
		op := t.op
		fmt.Fprintf(&b, "case %s%sRequest:\n", g.rq, op.Name)
		if len(op.Fails) != 0 {
			b.WriteString("match rpc.TagOf(text) {\n")
			for _, f := range op.Fails {
				var quoted []string
				for _, tag := range rpcgen.FailTags(unalias(f)) {
					quoted = append(quoted, fmt.Sprintf("%q", tag))
				}
				fmt.Fprintf(&b, "case %s:\nlet e = try rpc.Decode[%s](\"failure\", text)\n%sRejected{Failure: %s%s{E: e}}\n", strings.Join(quoted, ", "), g.expr(f), g.rq, g.rq, rpcgen.FailureVariant(ops, f))
			}
			b.WriteString("case _:\n")
		}
		fmt.Fprintf(&b, "return rpc.Transport{Reason: \"the tool %s answered an undeclared failure: \" + text}\n", t.name)
		if len(op.Fails) != 0 {
			b.WriteString("}\n")
		}
	}
	b.WriteString("}\n}\n}\n\n")

	fmt.Fprintf(&b, "// toolOver is the tool name answered by answer: it reads its arguments\n// (Call) and shows answer's outcome (Show).\nfunc toolOver(name string, answer own func(r Request) %sOutcome) mcp.Tool {\nmcp.Tool{Name: name, Description: Description(name), Input: Input(name), Call: func(arguments json.Value) json.Value fails mcp.ToolError {\nlet r = Call(name, arguments) catch {\ncase mcp.ToolError as e:\nreturn e\ncase mcp.UnknownTool as e:\nreturn mcp.ToolError{Message: \"no tool is named \" + e.Name}\n}\ntry Show(answer(r))\n}}\n}\n\n", g.rq)
	fmt.Fprintf(&b, "// toolsOver makes one tool per operation, in declaration order.\nfunc toolsOver(answer own func(r Request) %sOutcome) []mcp.Tool {\nlet mut out = []mcp.Tool{}\nfor name in Names {\nout = out.Append(toolOver(name, answer))\n}\nout\n}\n\n", g.rq)
	fmt.Fprintf(&b, "// Tools are the tools of the %s in force, one per operation, in its order.\nfunc Tools() []mcp.Tool needs %s {\nlet held = scope.InForce[%s]()\ntoolsOver(func(r Request) %sOutcome {\nwith held { %sCall(r) }\n})\n}\n\n", capName, capName, capName, g.rq, g.rq)

	fmt.Fprintf(&b, "// carry is c through the protocol: the tools/call message of its tool,\n// answered by module mcp's Handle over tools whose answer is answer, and\n// the result read back (ReadAnswer). Delivery can fail separately from\n// answer's outcome.\nfunc carry(c Request, answer own func(c Request) %sOutcome) %sOutcome fails rpc.Transport {\n", g.rq, g.rq)
	b.WriteString("let server = mcp.Server{Name: \"carry\", Version: \"1\", Tools: toolsOver(answer)}\nlet (name, arguments) = Arguments(c)\nlet message = \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":1,\\\"method\\\":\\\"tools/call\\\",\\\"params\\\":{\\\"name\\\":\" + rpc.Encode(name) + \",\\\"arguments\\\":\" + textOf(arguments) + \"}}\"\n")
	b.WriteString("match mcp.Handle(server, message) {\ncase mcp.Answered as a:\nlet result = json.Decode(json.Codec[mcp.Called]{MaxBytes: 0, MaxDepth: 0}, a.Result) catch {\ncase _:\nreturn rpc.Transport{Reason: \"a tool's result is not one: \" + a.Result}\n}\ntry ReadAnswer(c, result)\ncase mcp.Refused as e:\nreturn rpc.Transport{Reason: \"the server refused the call: \" + e.Message}\ncase mcp.Silent:\nreturn rpc.Transport{Reason: \"the server did not answer the call\"}\n}\n}\n\n")
	fmt.Fprintf(&b, "// Wire is the protocol as a transport of the hub's calls.\ntype Wire {} implements %s.Transport[Request, %sOutcome, rpc.Transport]\n\n", g.stdAPI, g.rq)
	fmt.Fprintf(&b, "// Carry is carry.\nfunc Carry(w Wire, c Request, answer own func(c Request) %sOutcome) %sOutcome fails rpc.Transport { try carry(c, answer) }\n\n", g.rq, g.rq)
	fmt.Fprintf(&b, "// Agrees: the protocol carries every call and every outcome the call\n// can have unchanged.\nlaw Agrees(r Request, g %sGiven) { %s.Agrees[Request, %sOutcome, rpc.Transport](Wire, r, %sOutcomeOf(r, g)) }\n\n", g.rq, g.stdAPI, g.rq, g.rq)

	b.WriteString("// RoundTrips reports whether Call reads what Arguments writes of r as r.\nfunc RoundTrips(r Request) bool {\nlet (name, arguments) = Arguments(r)\nlet back = Call(name, arguments) catch {\ncase mcp.ToolError, mcp.UnknownTool:\nreturn false\n}\nback == r\n}\n\n")
	b.WriteString("// FitsSchema reports whether the arguments that ask for r fit the\n// schema their tool lists.\nfunc FitsSchema(r Request) bool {\nlet (name, arguments) = Arguments(r)\nmcp.Conforms(Input(name), arguments)\n}\n\n")
	b.WriteString("// ArgumentsRoundTrip: Call reads what Arguments writes as the same Request.\nlaw ArgumentsRoundTrip(r Request) { RoundTrips(r) }\n\n")
	b.WriteString("// ArgumentsFitTheSchema: the arguments of every Request fit the schema\n// of their tool.\nlaw ArgumentsFitTheSchema(r Request) { FitsSchema(r) }\n")
	return b.String()
}

// quote is s as a JSON string.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// importBlock imports each package the declarations text names.
func (g *gen) importBlock(text string) string {
	var code strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			code.WriteString(line + "\n")
		}
	}
	src := stringLit().ReplaceAllString(code.String(), `""`)
	var paths []string
	for p, name := range g.imports {
		if regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(name) + `\.`).MatchString(src) {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		si, sj := !strings.Contains(paths[i], "/"), !strings.Contains(paths[j], "/")
		if si != sj {
			return si
		}
		return paths[i] < paths[j]
	})
	var b strings.Builder
	for _, p := range paths {
		if name := g.imports[p]; name != lastElem(p) {
			fmt.Fprintf(&b, "import %s %s\n", name, p)
		} else {
			fmt.Fprintf(&b, "import %s\n", p)
		}
	}
	return b.String()
}

var stringLit = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"(?:[^"\\]|\\.)*"`) }) // compiled on first use, not at process start (TestStartBuildsNoTables)
